package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

const markProgressPending = `INSERT INTO momentum.mark_import_progress
 (environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end)
 VALUES('testnet','d','BTCUSDT','D','2026-01-01 00:00+00','2026-01-04 00:00+00','2026-01-03 23:59:59.999+00')`

// TestMarkImportProgressMigration проверяет upgrade, checksum, данные, repeat и rollback collision.
func TestMarkImportProgressMigration(t *testing.T) {
	ctx := context.Background()
	for _, collision := range []bool{false, true} {
		db := openTestPostgres(t)
		prefix := momentumMigrations()[:12]
		migrationResult(t, migrateMomentumPostgres(ctx, db, prefix), nil)
		before := migrationState(t, db)
		marketStatement(t, db, `INSERT INTO momentum.datasets(environment,dataset_id,source,category)
 VALUES('testnet','saved','bybit_v5','linear');
 INSERT INTO momentum.mark_prices VALUES('testnet','saved','BTCUSDT','D','2026-01-01+00',1,2,0.5,1.5,true);
 INSERT INTO momentum.candle_import_progress(environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end)
 VALUES('testnet','saved','BTCUSDT','D','2026-01-01+00','2026-01-02+00','2026-01-01 23:59:59.999+00')`, true)
		const rows = `SELECT json_build_object('dataset',(SELECT json_agg(r) FROM momentum.datasets r),
 'mark',(SELECT json_agg(r) FROM momentum.mark_prices r),
 'candle_progress',(SELECT json_agg(r) FROM momentum.candle_import_progress r))::text`
		saved := migrationValue(t, db, rows)
		if collision {
			marketStatement(t, db, "CREATE TABLE momentum.mark_import_progress(owned_marker TEXT)", true)
			migrationResult(t, MigrateMomentumPostgres(ctx, db), ErrMigration)
			migrationValue(t, db, "SELECT count(*) FROM information_schema.columns WHERE table_schema='momentum' AND table_name='mark_import_progress' AND column_name='owned_marker'", "1")
			if migrationState(t, db) != before {
				t.Fatal("collision changed migration history")
			}
		} else {
			migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
			migrationValue(t, db, "SELECT count(*) FROM momentum.mark_import_progress", "0")
			migrationValue(t, db, `SELECT json_agg(r ORDER BY version)::text FROM (
 SELECT version,encode(checksum,'hex') AS checksum,applied_at FROM momentum.schema_migrations WHERE version<=12) r`, before)
			sum := sha256.Sum256([]byte(momentumMarkImportProgressSchema))
			migrationValue(t, db, "SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=13", fmt.Sprintf("%x", sum))
			migrationValue(t, db, "SELECT count(*) FROM momentum.schema_migrations", fmt.Sprint(len(momentumMigrations())))
			after := migrationState(t, db)
			migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
			if migrationState(t, db) != after {
				t.Fatal("repeat changed migration history")
			}
		}
		migrationValue(t, db, rows, saved)
	}
	fresh := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(ctx, fresh), nil)
	migrationValue(t, fresh, "SELECT count(*) FROM momentum.schema_migrations", strconv.Itoa(len(momentumMigrations())))
}

// markProgressDatabase создаёт отдельную схему и оба допустимых dataset namespace.
func markProgressDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	for _, env := range []string{"testnet", "mainnet"} {
		for _, id := range []string{"d", "other"} {
			marketStatement(t, db, fmt.Sprintf("INSERT INTO momentum.datasets(environment,dataset_id,source,category) VALUES('%s','%s','bybit_v5','linear')", env, id), true)
		}
	}
	marketStatement(t, db, "INSERT INTO momentum.datasets(environment,dataset_id,source,category) VALUES('mainnet','mainonly','bybit_v5','linear')", true)
	return db
}

// TestMarkImportProgressSchema проверяет состояния, ключи, UTC grid и SQL ограничения.
func TestMarkImportProgressSchema(t *testing.T) {
	db := markProgressDatabase(t)
	marketStatement(t, db, markProgressPending, true)
	migrationValue(t, db, "SELECT revision::text||':'||state||':'||reason||':'||(last_server_time IS NULL)::text||':'||(next_end=upper_open-INTERVAL '1 millisecond')::text FROM momentum.mark_import_progress", "0:pending::true:true")
	marketStatement(t, db, markProgressPending, false)
	marketStatement(t, db, markProgressPending+" ON CONFLICT DO NOTHING", true)
	for _, variant := range []string{
		strings.Replace(markProgressPending, "'testnet'", "'mainnet'", 1),
		strings.Replace(markProgressPending, "'d'", "'other'", 1),
		strings.Replace(markProgressPending, "'BTCUSDT'", "'ETHUSDT'", 1),
		strings.Replace(markProgressPending, "'D'", "'60'", 1),
		strings.Replace(markProgressPending, "'2026-01-01 00:00+00'", "'2026-01-02 00:00+00'", 1),
		strings.Replace(strings.Replace(markProgressPending, "'2026-01-04 00:00+00'", "'2026-01-05 00:00+00'", 1), "'2026-01-03 23:59:59.999+00'", "'2026-01-04 23:59:59.999+00'", 1),
	} {
		marketStatement(t, db, variant, true)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.mark_import_progress", "7")
	for _, tc := range []struct{ state, reason, observed, cursor string }{
		{"progress", "", "'2026-01-04+00'", "'2026-01-02 23:59:59.999+00'"},
		{"complete", "", "'2026-01-04+00'", "lower_open-INTERVAL '1 millisecond'"},
		{"blocked", "empty", "'2026-01-04+00'", "'2026-01-01 23:59:59.999+00'"},
		{"blocked", "gap", "'2026-01-04+00'", "'2026-01-01 23:59:59.999+00'"},
		{"blocked", "unconfirmed", "'2026-01-02+00'", "'2026-01-01 23:59:59.999+00'"},
	} {
		marketStatement(t, db, "UPDATE momentum.mark_import_progress SET revision=1,state='"+tc.state+"',reason='"+tc.reason+"',last_server_time="+tc.observed+",next_end="+tc.cursor+" WHERE environment='testnet' AND dataset_id='d' AND symbol='BTCUSDT' AND interval_code='D' AND lower_open='2026-01-01+00' AND upper_open='2026-01-04+00'", true)
	}
	marketStatement(t, db, `INSERT INTO momentum.mark_import_progress
 (environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end,revision,state,reason)
 VALUES('testnet','d','BTCUSDT','D','epoch','1970-01-02+00','1970-01-01 23:59:59.999+00',1,'blocked','source_boundary')`, true)
	migrationValue(t, db, "SELECT state||':'||reason||':'||(last_server_time IS NULL)::text FROM momentum.mark_import_progress WHERE lower_open='epoch'", "blocked:source_boundary:true")
	conn, err := db.Conn(context.Background())
	migrationResult(t, err, nil)
	defer conn.Close()
	marketStatement(t, conn, "SET TIME ZONE 'Pacific/Honolulu'", true)
	marketStatement(t, conn, `INSERT INTO momentum.mark_import_progress
 (environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end)
 VALUES('testnet','d','ETHUSDT','60','2026-01-01 01:00+00','2026-01-01 03:00+00','2026-01-01 02:59:59.999+00')`, true)
	var utcGrid bool
	err = conn.QueryRowContext(context.Background(), "SELECT lower_open=TIMESTAMPTZ '2026-01-01 01:00+00' FROM momentum.mark_import_progress WHERE symbol='ETHUSDT' AND interval_code='60'").Scan(&utcGrid)
	migrationResult(t, err, nil)
	if !utcGrid {
		t.Fatal("hourly grid changed with session timezone")
	}
	marketStatement(t, conn, "INSERT INTO momentum.mark_import_progress(environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end) VALUES('testnet','d','XRPUSDT','60','2026-01-01 01:00+00','2026-01-01 03:00+00','2026-01-01 02:59:59.999+00')", true)
	migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices", "0")
	migrationValue(t, db, "SELECT count(*) FROM momentum.candles", "0")
	migrationValue(t, db, "SELECT count(*) FROM momentum.candle_import_progress", "0")
	for _, column := range []string{"environment", "dataset_id", "symbol", "interval_code", "lower_open", "upper_open", "next_end", "revision", "state", "reason"} {
		runSchemaSQLState(t, db, "23502", "UPDATE momentum.mark_import_progress SET "+column+"=NULL")
	}
	for _, tc := range []struct{ code, set string }{
		{"23503", "dataset_id='missing'"}, {"23503", "environment='testnet',dataset_id='mainonly'"},
		{"23514", "symbol='btcusdt'"}, {"23514", "interval_code='1'"},
		{"23514", "lower_open='-infinity'"}, {"23514", "lower_open='1969-12-31 23:59:59.999+00'"},
		{"23514", "lower_open=lower_open+INTERVAL '1 microsecond'"},
		{"23514", "upper_open='infinity'"}, {"23514", "upper_open=lower_open"},
		{"23514", "upper_open=upper_open+INTERVAL '1 microsecond'"},
		{"23514", "next_end='infinity'"}, {"23514", "next_end=lower_open-INTERVAL '2 milliseconds'"},
		{"23514", "next_end=upper_open"}, {"23514", "next_end=next_end+INTERVAL '1 microsecond'"},
		{"23514", "revision=-1"}, {"22003", "revision=9223372036854775808"},
		{"23514", "state='unknown'"}, {"23514", "reason='unknown'"},
		{"23514", "state='pending'"}, {"23514", "last_server_time='infinity'"},
		{"23514", "last_server_time='1969-12-31 23:59:59.999+00'"},
		{"23514", "last_server_time='-infinity'"},
		{"23514", "last_server_time='2026-01-04 00:00:00.000001+00'"},
		{"23514", "state='complete',revision=1,last_server_time=NULL"},
		{"23514", "state='complete',revision=1,last_server_time=upper_open,next_end=lower_open"},
		{"23514", "state='blocked',revision=1,reason='empty',last_server_time=lower_open,next_end=lower_open"},
		{"23514", "state='blocked',revision=1,reason='source_boundary',last_server_time=upper_open"},
		{"23514", "lower_open='2026-01-02+00',upper_open='2026-01-02+00'"},
	} {
		if got := migrationValue(t, db, "SELECT count(*) FROM momentum.mark_import_progress WHERE environment='testnet' AND dataset_id='d' AND symbol='BTCUSDT' AND interval_code='D' AND lower_open='2026-01-01+00' AND upper_open='2026-01-04+00'"); got != "1" {
			t.Fatalf("missing constraint fixture before %s: %s", tc.set, got)
		}
		runSchemaSQLState(t, db, tc.code, "UPDATE momentum.mark_import_progress SET "+tc.set+" WHERE environment='testnet' AND dataset_id='d' AND symbol='BTCUSDT' AND interval_code='D' AND lower_open='2026-01-01+00' AND upper_open='2026-01-04+00'")
	}
	marketStatement(t, db, "UPDATE momentum.mark_import_progress SET revision=9223372036854775807,state='progress',reason='',last_server_time=upper_open,next_end=lower_open+INTERVAL '1 day'-INTERVAL '1 millisecond' WHERE environment='testnet' AND dataset_id='d' AND symbol='BTCUSDT' AND interval_code='D' AND lower_open='2026-01-01+00' AND upper_open='2026-01-04+00'", true)
	migrationValue(t, db, "SELECT revision::text FROM momentum.mark_import_progress WHERE environment='testnet' AND dataset_id='d' AND symbol='BTCUSDT' AND interval_code='D' AND lower_open='2026-01-01+00' AND upper_open='2026-01-04+00'", "9223372036854775807")
}
