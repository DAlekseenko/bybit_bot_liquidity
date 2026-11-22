package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/paperwire"
	"github.com/lib/pq"
)

// paperSchemaExec проверяет точный SQLSTATE, включая успешную операцию.
func paperSchemaExec(t *testing.T, db *sql.DB, code, query string, args ...any) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), query, args...)
	var pg *pq.Error
	if code == "" && err != nil || code != "" && (!errors.As(err, &pg) || string(pg.Code) != code) {
		t.Fatalf("SQLSTATE want=%q err=%v query=%s", code, err, query)
	}
}

// paperSchemaRun создаёт независимый опубликованный run/config fixture.
func paperSchemaRun(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	c := runRepositoryConfig(t, false)
	_, err := CreateOrReadRebalanceRun(context.Background(), db, RebalanceRunInput{
		RunID: id, AccountID: id, StrategyID: c.Values().StrategyID,
		ScheduledAt: time.Unix(1700000000, 0).UTC(), Config: c,
	})
	migrationResult(t, err, nil)
}

// TestPaperInputMigration проверяет свежую схему, точный upgrade и атомарный отказ.
func TestPaperInputMigration(t *testing.T) {
	ctx := context.Background()
	db := openTestPostgres(t)
	specs := momentumMigrations()
	if len(specs) != 15 || specs[14].version != 15 {
		t.Fatal("migration catalog changed")
	}
	migrationResult(t, migrateMomentumPostgres(ctx, db, specs[:14]), nil)
	paperSchemaRun(t, db, "preserved")
	before := migrationState(t, db)
	data := migrationValue(t, db, `SELECT row_to_json(r)::text || row_to_json(c)::text FROM momentum.rebalance_runs r JOIN momentum.config_snapshots c USING(config_hash)`)
	paperSchemaExec(t, db, "", "CREATE TABLE momentum.paper_run_inputs(collision text)")
	migrationResult(t, MigrateMomentumPostgres(ctx, db), ErrMigration)
	if migrationState(t, db) != before {
		t.Fatal("collision changed prefix history")
	}
	paperSchemaExec(t, db, "", "DROP TABLE momentum.paper_run_inputs")
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	prefix := migrationValue(t, db, `SELECT json_agg(r ORDER BY version)::text FROM (SELECT version,encode(checksum,'hex') AS checksum,applied_at FROM momentum.schema_migrations WHERE version<=14) r`)
	if prefix != before {
		t.Fatal("upgrade changed prefix/checksums/applied_at")
	}
	migrationValue(t, db, `SELECT row_to_json(r)::text || row_to_json(c)::text FROM momentum.rebalance_runs r JOIN momentum.config_snapshots c USING(config_hash)`, data)
	hash := sha256.Sum256([]byte(momentumPaperInputSchema()))
	migrationValue(t, db, `SELECT count(*)::text || ':' || max(version)::text || ':' || max(encode(checksum,'hex')) FILTER(WHERE version=15) FROM momentum.schema_migrations`, fmt.Sprintf("15:15:%x", hash))
	after := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	migrationValue(t, db, `SELECT row_to_json(r)::text || row_to_json(c)::text FROM momentum.rebalance_runs r JOIN momentum.config_snapshots c USING(config_hash)`, data)
	if migrationState(t, db) != after {
		t.Fatal("repeat changed history")
	}
	changed := append([]migrationSpec(nil), specs...)
	changed[14].sql += "\n"
	err := migrateMomentumPostgres(ctx, db, changed)
	migrationResult(t, err, ErrMigrationVersion)
	if errors.Is(err, ErrMigration) {
		t.Fatal("checksum error misclassified")
	}
	fresh := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(ctx, fresh), nil)
	migrationValue(t, fresh, `SELECT count(*) FROM pg_catalog.pg_trigger WHERE tgrelid='momentum.paper_run_inputs'::regclass AND NOT tgisinternal`, "2")
}

// TestPaperInputConstraints проверяет реальные CHECK, FK, точные bytes и immutability.
func TestPaperInputConstraints(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	payload := []byte{0, 255, 1}
	hash := sha256.Sum256(payload)
	insert := `INSERT INTO momentum.paper_run_inputs(run_id,environment,account_id,strategy_id,sequence,previous_input_hash,previous_resolution_hash,input_hash,payload) VALUES($1,'paper',$2,$3,$4,$5,$6,$7,$8)`
	for _, scope := range [][2]string{{"account", "strategy"}, {"other-account", "strategy"}, {"account", "other-strategy"}} {
		for seq := 1; seq <= 2; seq++ {
			id := fmt.Sprint(scope, seq)
			paperSchemaRun(t, db, id)
			var previous any
			if seq == 2 {
				previous = hash[:]
			}
			paperSchemaExec(t, db, "", insert, id, scope[0], scope[1], seq, previous, previous, hash[:], payload)
		}
	}
	var saved, key []byte
	var created time.Time
	migrationResult(t, db.QueryRowContext(context.Background(), `SELECT payload,input_hash,created_at FROM momentum.paper_run_inputs WHERE account_id='account' AND strategy_id='strategy' AND sequence=1`).Scan(&saved, &key, &created), nil)
	if !bytes.Equal(saved, payload) || !bytes.Equal(key, hash[:]) || created.IsZero() {
		t.Fatal("bytes/hash/timestamp changed")
	}
	migrationValue(t, db, `SELECT bool_and(isfinite(created_at) AND predecessor_sequence IS NOT DISTINCT FROM CASE WHEN sequence>1 THEN sequence-1 END) FROM momentum.paper_run_inputs`, "true")
	// Каждый отказ имеет собственные свободные run и identity; меняется одно поле.
	for column, values := range map[string][]string{
		"environment":              {"NULL", "''", "' '", "'testnet'"},
		"account_id":               {"NULL", "''", "E' \\t\\n'"},
		"strategy_id":              {"NULL", "''", "E' \\t\\n'"},
		"sequence":                 {"NULL", "0", "-1", "1.4", "'NaN'", "'Infinity'", "'-Infinity'", "18446744073709551616"},
		"input_hash":               {"NULL", "decode(repeat('00',31),'hex')", "decode(repeat('00',33),'hex')", "decode(repeat('00',32),'hex')"},
		"payload":                  {"NULL", "''::bytea", fmt.Sprintf("decode(repeat('00',%d),'hex')", paperwire.MaxCanonicalBytes+1)},
		"created_at":               {"NULL", "'infinity'", "'-infinity'"},
		"previous_input_hash":      {"''::bytea", "decode(repeat('00',32),'hex')"},
		"previous_resolution_hash": {"''::bytea", "decode(repeat('00',32),'hex')"},
	} {
		for i, value := range values {
			t.Run(fmt.Sprintf("%s/%d", column, i), func(t *testing.T) {
				id := fmt.Sprintf("negative-%s-%d", column, i)
				paperSchemaRun(t, db, id)
				code := "23514"
				if value == "NULL" && column != "previous_input_hash" && column != "previous_resolution_hash" {
					code = "23502"
				}
				query := `WITH supplied AS (SELECT $1::text, $2::bytea, $3::bytea) INSERT INTO momentum.paper_run_inputs(run_id,environment,account_id,strategy_id,sequence,input_hash,payload,created_at,previous_input_hash,previous_resolution_hash) SELECT $1,`
				defaults := map[string]string{
					"environment":              "'paper'",
					"account_id":               "$1",
					"strategy_id":              "'s'",
					"sequence":                 "1",
					"input_hash":               "$2",
					"payload":                  "$3",
					"created_at":               "CURRENT_TIMESTAMP",
					"previous_input_hash":      "NULL",
					"previous_resolution_hash": "NULL",
				}
				defaults[column] = value
				for j, field := range []string{"environment", "account_id", "strategy_id", "sequence", "input_hash", "payload", "created_at", "previous_input_hash", "previous_resolution_hash"} {
					if j > 0 {
						query += ","
					}
					query += defaults[field]
				}
				paperSchemaExec(t, db, code, query, id, hash[:], payload)
			})
		}
	}
	for _, column := range []string{"previous_input_hash", "previous_resolution_hash"} {
		for i, value := range []any{nil, []byte{}, make([]byte, 31), make([]byte, 33)} {
			id := fmt.Sprintf("child-%s-%d", column, i)
			paperSchemaRun(t, db, id)
			parent := id + "-parent"
			paperSchemaRun(t, db, parent)
			paperSchemaExec(t, db, "", insert, parent, id, "s", 1, nil, nil, hash[:], payload)
			previous, resolution := any(hash[:]), any(hash[:])
			if column == "previous_input_hash" {
				previous = value
			} else {
				resolution = value
			}
			paperSchemaExec(t, db, "23514", insert, id, id, "s", 2, previous, resolution, hash[:], payload)
		}
	}
	for _, tc := range []struct {
		name, account, strategy, sequence, code string
		previous                                []byte
	}{
		{"missing-run", "missing", "s", "1", "23503", nil},
		{"duplicate-run", "new", "s", "1", "23505", nil},
		{"occupied", "account", "strategy", "1", "23505", nil},
		{"gap", "new-gap", "s", "3", "23503", hash[:]},
		{"missing-parent", "new-parent", "s", "2", "23503", hash[:]},
		{"wrong-hash", "account", "strategy", "3", "23503", make([]byte, 32)},
		{"cross-scope", "isolated", "s", "2", "23503", hash[:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.name
			if tc.name == "duplicate-run" {
				id = fmt.Sprint([2]string{"account", "strategy"}, 1)
			} else if tc.name != "missing-run" {
				paperSchemaRun(t, db, id)
			}
			paperSchemaExec(t, db, tc.code, insert, id, tc.account, tc.strategy, tc.sequence, tc.previous, tc.previous, hash[:], payload)
		})
	}
	for _, size := range []int{1, paperwire.MaxCanonicalBytes} {
		id := fmt.Sprint("boundary-", size)
		paperSchemaRun(t, db, id)
		raw := bytes.Repeat([]byte{255}, size)
		digest := sha256.Sum256(raw)
		paperSchemaExec(t, db, "", insert, id, id, "s", "1.0", nil, nil, digest[:], raw)
		migrationValue(t, db, fmt.Sprintf(`SELECT (sequence=1 AND predecessor_sequence IS NULL AND octet_length(payload)=%d AND input_hash=sha256(payload))::text FROM momentum.paper_run_inputs WHERE run_id='%s'`, size, id), "true")
	}
	state := migrationValue(t, db, `SELECT json_agg(r ORDER BY run_id)::text FROM momentum.paper_run_inputs r`)
	for _, query := range []string{"UPDATE momentum.paper_run_inputs SET payload=payload", "UPDATE momentum.paper_run_inputs SET created_at=CURRENT_TIMESTAMP", "DELETE FROM momentum.paper_run_inputs", "TRUNCATE momentum.paper_run_inputs"} {
		paperSchemaExec(t, db, "55000", query)
		migrationValue(t, db, `SELECT json_agg(r ORDER BY run_id)::text FROM momentum.paper_run_inputs r`, state)
	}
}

// TestPaperInputNumericBounds проверяет реальные CHECK без невозможной цепочки FK.
func TestPaperInputNumericBounds(t *testing.T) {
	db := openTestPostgres(t)
	db.SetMaxOpenConns(1)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	paperSchemaExec(t, db, "", `CREATE TEMP TABLE input_bounds (LIKE momentum.paper_run_inputs INCLUDING CONSTRAINTS INCLUDING GENERATED INCLUDING DEFAULTS)`)
	query := `INSERT INTO input_bounds(run_id,environment,account_id,strategy_id,sequence,previous_input_hash,previous_resolution_hash,input_hash,payload) VALUES('probe','paper','a','s',$1,$2,$2,sha256('x'::bytea),'x'::bytea)`
	for _, seq := range []string{"1.4", "18446744073709551616", "NaN", "Infinity", "-Infinity"} {
		paperSchemaExec(t, db, "23514", query, seq, make([]byte, 32))
	}
	paperSchemaExec(t, db, "", query, "18446744073709551615", make([]byte, 32))
	migrationValue(t, db, `SELECT sequence::text || ':' || predecessor_sequence::text FROM input_bounds`, "18446744073709551615:18446744073709551614")
}
