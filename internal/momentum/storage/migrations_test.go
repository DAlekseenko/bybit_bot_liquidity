package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
)

// migrationValue читает scalar и при указанном ожидании сразу сверяет его.
func migrationValue(t *testing.T, db *sql.DB, query string, want ...string) string {
	t.Helper()
	var got string
	if err := db.QueryRowContext(context.Background(), query).Scan(&got); err != nil || len(want) != 0 && got != want[0] {
		t.Fatalf("query=%s got=%q want=%v err=%v", query, got, want, err)
	}
	return got
}

// migrationState сохраняет всю историю либо отсутствие новой схемы.
func migrationState(t *testing.T, db *sql.DB) string {
	t.Helper()
	if migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname='momentum'") == "0" {
		return ""
	}
	return migrationValue(t, db, `SELECT coalesce(json_agg(r ORDER BY version)::text, '[]')
 FROM (SELECT version, encode(checksum,'hex') AS checksum, applied_at FROM momentum.schema_migrations) r`)
}

// migrationResult проверяет ожидаемую категорию и отсутствие сырых диагностик.
func migrationResult(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || regexp.MustCompile(`secret-marker|sensitive_sql_marker|postgres://|pq:`).MatchString(fmt.Sprint(err)) {
		t.Fatalf("got=%v want=%v", err, want)
	}
}

// TestMigrationTransactions проверяет реальную блокировку, два pool, bootstrap и повтор.
func TestMigrationTransactions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	migrationResult(t, MigrateMomentumPostgres(context.Background(), nil), ErrMigration)
	migrationResult(t, MigrateMomentumPostgres(ctx, nil), context.Canceled)
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), context.Canceled)
	migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname='momentum'", "0")
	before := migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'")
	holder, err := db.BeginTx(context.Background(), nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.ExecContext(context.Background(), "SELECT pg_catalog.pg_advisory_xact_lock(651010)")
	migrationResult(t, err, nil)
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = MigrateMomentumPostgres(ctx, db)
	migrationResult(t, err, ErrMigration)
	migrationResult(t, err, context.DeadlineExceeded)
	if time.Since(start) > 2*time.Second {
		t.Fatal("lock cancellation exceeded bound")
	}
	migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname='momentum'", "0")
	migrationResult(t, holder.Rollback(), nil)
	u, _ := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN")) // Исходный opt-in уже проверен helper.
	u.Path, u.RawPath = "/"+migrationValue(t, db, "SELECT current_database()"), ""
	db.SetMaxOpenConns(1) // Явный retry обязан получить обратно единственное соединение.
	second, err := OpenMomentumPostgres(context.Background(), u.String())
	migrationResult(t, err, nil)
	defer second.Close()
	retry, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	barrier, results := make(chan struct{}), make(chan error, 2)
	for _, pool := range []*sql.DB{db, second} {
		// Оба worker ожидают один барьер и используют независимые pools той же БД.
		go func(pool *sql.DB) {
			<-barrier
			results <- MigrateMomentumPostgres(retry, pool)
		}(pool)
	}
	close(barrier)
	for range 2 {
		migrationResult(t, <-results, nil)
	}
	checksum := sha256.Sum256([]byte(momentumBootstrap))
	migrationValue(t, db, `SELECT count(*)::text || ':' || min(version)::text || ':' || min(encode(checksum,'hex')) FILTER (WHERE version=1) || ':' ||
 bool_and(applied_at <> TIMESTAMPTZ '0001-01-01 00:00:00+00' AND isfinite(applied_at))::text FROM momentum.schema_migrations`, fmt.Sprintf("6:1:%x:true", checksum))
	initial := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	if migrationState(t, db) != initial || migrationState(t, second) != initial {
		t.Fatal("repeat or independent pool changed exact history")
	}
	migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'", before)
	migrationResult(t, db.Close(), nil)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), ErrMigration)
	unavailable, err := sql.Open("postgres", "postgres://u:secret-marker@127.0.0.1:1/momentum_test_unavailable?sslmode=disable&connect_timeout=1")
	migrationResult(t, err, nil)
	defer unavailable.Close()
	bounded, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	migrationResult(t, MigrateMomentumPostgres(bounded, unavailable), ErrMigration)
}

// TestMigrationRejections проверяет validator, оба rollback и реальное подключение metadata verifier.
func TestMigrationRejections(t *testing.T) {
	for _, tc := range []struct {
		name, change, pk string
		prefix, fail     bool
		want             error
	}{
		{"checksum", "UPDATE momentum.schema_migrations SET checksum=decode(repeat('00',32),'hex')", "1", true, false, ErrMigrationVersion},
		{"fresh", "", "0", false, true, ErrMigration},
		{"prefix", "", "1", true, true, ErrMigration},
		{"metadata", "ALTER TABLE momentum.schema_migrations DROP CONSTRAINT schema_migrations_pkey", "0", true, false, ErrMigration},
	} {
		db := openTestPostgres(t)
		if tc.prefix {
			migrationResult(t, migrateMomentumPostgres(context.Background(), db, []migrationSpec{{1, momentumBootstrap}}), nil)
		}
		if tc.change != "" {
			_, err := db.ExecContext(context.Background(), tc.change)
			migrationResult(t, err, nil)
		}
		before := migrationState(t, db)
		specs := []migrationSpec{{1, momentumBootstrap}, {2, "CREATE TABLE momentum.pending_marker(id bigint)"}}
		valid := specs
		if tc.fail {
			specs = append(specs, migrationSpec{3, "sensitive_sql_marker INVALID SQL"})
		}
		migrationResult(t, migrateMomentumPostgres(context.Background(), db, specs), tc.want)
		if migrationState(t, db) != before {
			t.Fatalf("case=%s changed prefix or leaked bootstrap", tc.name)
		}
		migrationValue(t, db, "SELECT to_regclass('momentum.pending_marker') IS NULL", "true")
		migrationValue(t, db, "SELECT count(*) FROM pg_catalog.pg_constraint WHERE conrelid=to_regclass('momentum.schema_migrations') AND contype='p'", tc.pk)
		if tc.fail {
			migrationResult(t, migrateMomentumPostgres(context.Background(), db, valid), nil)
			migrationValue(t, db, "SELECT count(*)=2 AND max(version)=2 AND to_regclass('momentum.pending_marker') IS NOT NULL FROM momentum.schema_migrations", "true")
			if tc.prefix && !strings.Contains(migrationState(t, db), strings.Trim(before, "[]")) {
				t.Fatal("retry changed prefix/checksum/timestamp")
			}
		}
	}
}

// TestConfigSnapshotMigration проверяет применение нового суффикса без потери ранее записанного.
func TestConfigSnapshotMigration(t *testing.T) {
	ctx := context.Background()
	prefix := []migrationSpec{
		{1, momentumBootstrap}, {2, momentumMarketCandlesSchema}, {3, momentumMarketSchema},
		{4, momentumImportProgressSchema}, {5, momentumSignalJournalSchema},
	}
	db := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(ctx, db, prefix), nil)
	history := migrationState(t, db)
	marketStatement(t, db, "INSERT INTO momentum.signal_batches(batch_hash,payload) VALUES(sha256('kept'::bytea),'kept'::bytea)", true)
	saved := migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.signal_batches r")
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	migrationValue(t, db, `SELECT json_agg(r ORDER BY version)::text FROM (
 SELECT version,encode(checksum,'hex') AS checksum,applied_at FROM momentum.schema_migrations WHERE version<=5) r`, history)
	checksum := sha256.Sum256([]byte(momentumConfigSnapshotSchema))
	migrationValue(t, db, "SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=6", fmt.Sprintf("%x", checksum))
	migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.signal_batches r", saved)
	migrationValue(t, db, "SELECT count(*) FROM momentum.config_snapshots", "0")
	after := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	if migrationState(t, db) != after {
		t.Fatal("repeat changed the committed migration history")
	}

	collision := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(ctx, collision, prefix), nil)
	before := migrationState(t, collision)
	marketStatement(t, collision, "CREATE TABLE momentum.config_snapshots(owned_marker TEXT); INSERT INTO momentum.config_snapshots VALUES ('kept')", true)
	migrationResult(t, MigrateMomentumPostgres(ctx, collision), ErrMigration)
	if migrationState(t, collision) != before {
		t.Fatal("failed migration changed the committed prefix")
	}
	migrationValue(t, collision, "SELECT owned_marker FROM momentum.config_snapshots", "kept")
}

// TestConfigSnapshotSchema проверяет точное сохранение снимка и локальные ограничения целостности.
func TestConfigSnapshotSchema(t *testing.T) {
	ctx := context.Background()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	raw, err := os.ReadFile("../../../configs/momentum.paper.yaml")
	migrationResult(t, err, nil)
	for _, document := range []string{string(raw), strings.Replace(string(raw), "next_monday", "now", 1)} {
		parsed, err := config.Parse([]byte(document))
		migrationResult(t, err, nil)
		payload, digest, err := parsed.Snapshot()
		migrationResult(t, err, nil)
		key, err := hex.DecodeString(digest)
		migrationResult(t, err, nil)
		_, err = db.ExecContext(ctx, "INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES($1,$2)", key, payload)
		migrationResult(t, err, nil)
		var stored []byte
		err = db.QueryRowContext(ctx, "SELECT payload FROM momentum.config_snapshots WHERE config_hash=$1", key).Scan(&stored)
		migrationResult(t, err, nil)
		if string(stored) != string(payload) {
			t.Fatal("snapshot bytes changed")
		}
		reparsed, err := config.Parse(stored)
		migrationResult(t, err, nil)
		_, again, err := reparsed.Snapshot()
		migrationResult(t, err, nil)
		if again != digest {
			t.Fatal("snapshot hash changed on reparse")
		}
		_, err = db.ExecContext(ctx, "INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES($1,$2)", key, payload)
		if err == nil {
			t.Fatal("duplicate config hash inserted")
		}
	}
	for _, values := range []string{
		"(NULL,'new'::bytea,DEFAULT)",
		"('x'::bytea,'new'::bytea,DEFAULT)",
		"(sha256('different'::bytea),'new'::bytea,DEFAULT)",
		"(sha256(''::bytea),''::bytea,DEFAULT)",
		"(sha256('new'::bytea),NULL,DEFAULT)",
		"(sha256('new'::bytea),'new'::bytea,NULL)",
		"(sha256('new'::bytea),'new'::bytea,'infinity')",
		"(sha256('new'::bytea),'new'::bytea,'-infinity')",
		"(sha256('new'::bytea),'new'::bytea,'1969-12-31 23:59:59+00')",
	} {
		marketStatement(t, db, "INSERT INTO momentum.config_snapshots VALUES "+values, false)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.config_snapshots", "2")
	migrationValue(t, db, "SELECT bool_and(isfinite(created_at)) FROM momentum.config_snapshots", "true")
}
