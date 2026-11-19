package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// marketStatement проверяет успешность одного независимого SQL statement.
func marketStatement(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, query string, wantOK bool) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), query)
	if (err == nil) != wantOK {
		t.Fatalf("unexpected statement success=%v, want=%v: %v", err == nil, wantOK, err)
	}
}

// TestMarketCandlesMigration проверяет upgrade v1, два checksum, repeat и отсутствие потери префикса.
func TestMarketCandlesMigration(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(context.Background(), db, []migrationSpec{{1, momentumBootstrap}}), nil)
	prefix := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	if !strings.Contains(migrationState(t, db), strings.Trim(prefix, "[]")) {
		t.Fatal("upgrade changed committed v1 checksum/timestamp")
	}
	for i, payload := range []string{momentumBootstrap, momentumMarketCandlesSchema} {
		checksum := sha256.Sum256([]byte(payload))
		migrationValue(t, db, fmt.Sprintf("SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=%d", i+1), fmt.Sprintf("%x", checksum))
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.schema_migrations", "8")
	before := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	if migrationState(t, db) != before {
		t.Fatal("repeat changed exact committed history")
	}
	for _, table := range []string{"datasets", "candles"} {
		migrationValue(t, db, "SELECT count(*) FROM momentum."+table, "0")
	}
	collision := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(context.Background(), collision, []migrationSpec{{1, momentumBootstrap}}), nil)
	prefix = migrationState(t, collision)
	marketStatement(t, collision, "CREATE TABLE momentum.candles (owned_marker TEXT)", true)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), collision), ErrMigration)
	if migrationState(t, collision) != prefix {
		t.Fatal("failed v2 changed committed v1 history")
	}
	migrationValue(t, collision, "SELECT to_regclass('momentum.datasets') IS NULL", "true")
	migrationValue(t, collision, "SELECT count(*) FROM information_schema.columns WHERE table_schema='momentum' AND table_name='candles' AND column_name='owned_marker'", "1")
}

// TestMarketCandlesSchema проверяет datasets/candles, точность и UTC на закреплённом соединении.
func TestMarketCandlesSchema(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	conn, err := db.Conn(context.Background())
	migrationResult(t, err, nil)
	defer conn.Close()
	marketStatement(t, conn, "SET TIME ZONE 'Pacific/Honolulu'", true)
	var zone string
	if err := conn.QueryRowContext(context.Background(), "SHOW TIME ZONE").Scan(&zone); err != nil || zone != "Pacific/Honolulu" {
		t.Fatalf("wrong pinned session: zone=%q err=%v", zone, err)
	}
	for _, environment := range []string{"mainnet", "testnet"} {
		marketStatement(t, conn, fmt.Sprintf("INSERT INTO momentum.datasets VALUES ('%s','d','bybit_v5','linear',DEFAULT)", environment), true)
	}
	for _, change := range []string{"environment='paper'", "dataset_id=''", "dataset_id=' d'", "source='other'", "category='spot'", "created_at='infinity'", "created_at='-infinity'", "created_at='1969-01-01 00:00:00+00'"} {
		marketStatement(t, conn, "UPDATE momentum.datasets SET "+change+" WHERE environment='mainnet'", false)
	}
	marketStatement(t, conn, "UPDATE momentum.datasets SET dataset_id='"+strings.Repeat("x", 129)+"' WHERE environment='mainnet'", false)
	marketStatement(t, conn, "INSERT INTO momentum.datasets VALUES ('mainnet','mainonly','bybit_v5','linear',DEFAULT)", true)
	marketStatement(t, conn, "INSERT INTO momentum.datasets SELECT * FROM momentum.datasets", false)
	marketStatement(t, conn, "INSERT INTO momentum.datasets SELECT * FROM momentum.datasets ON CONFLICT DO NOTHING", true)
	for _, environment := range []string{"mainnet", "testnet"} {
		query := fmt.Sprintf("INSERT INTO momentum.candles VALUES ('%s','d','BTCUSDT','D','2026-01-01 00:00:00+00',100,110,90,105,0,true)", environment)
		marketStatement(t, conn, query, true)
		before := migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.candles r WHERE environment='"+environment+"'")
		marketStatement(t, conn, query, false)
		marketStatement(t, conn, query+" ON CONFLICT DO NOTHING", true)
		migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.candles r WHERE environment='"+environment+"'", before)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.candles", "2")
	for _, change := range []string{"environment='paper'", "dataset_id='missing'", "symbol='btcusdt'", "symbol='BTCUSDC'"} {
		marketStatement(t, conn, "UPDATE momentum.candles SET "+change, false)
	}
	for _, column := range []string{"open", "high", "low", "close"} {
		for _, value := range []string{"0", "-1", "'NaN'", "'Infinity'", "'-Infinity'"} {
			marketStatement(t, conn, "UPDATE momentum.candles SET "+column+"="+value, false)
		}
	}
	for _, change := range []string{"interval_code='1'", "open_time='infinity'", "open_time='-infinity'", "open_time='1969-01-01 00:00:00+00'", "open_time='2026-01-01 01:00+00'", "open_time='2026-01-01 00:00+03'", "high=101", "low=101"} {
		marketStatement(t, conn, "UPDATE momentum.candles SET "+change, false)
	}
	marketStatement(t, conn, "UPDATE momentum.candles SET open_time='2026-01-02 03:00+03'", true)
	marketStatement(t, conn, "UPDATE momentum.candles SET interval_code='60',open_time='2026-01-01 01:00+00'", true)
	marketStatement(t, conn, "UPDATE momentum.candles SET open_time='2026-01-01 01:01+00'", false)
	marketStatement(t, conn, "UPDATE momentum.candles SET confirmed=false", true)
	for _, value := range []string{"-1", "'NaN'", "'Infinity'", "'-Infinity'"} {
		marketStatement(t, conn, "UPDATE momentum.candles SET volume="+value, false)
	}
	exact := "0." + strings.Repeat("12345678", 8)
	marketStatement(t, conn, "UPDATE momentum.candles SET close="+exact+",low=0.1", true)
	migrationValue(t, db, "SELECT close::text FROM momentum.candles LIMIT 1", exact)
	for table, columns := range map[string][]string{
		"datasets": {"environment", "dataset_id", "source", "category", "created_at"},
		"candles":  {"environment", "dataset_id", "symbol", "interval_code", "open_time", "open", "high", "low", "close", "volume", "confirmed"},
	} {
		for _, column := range columns {
			marketStatement(t, conn, "UPDATE momentum."+table+" SET "+column+"=NULL", false)
		}
	}
	migrationValue(t, db, "SELECT bool_and(a.atttypmod = -1) FROM pg_catalog.pg_attribute a WHERE a.attrelid='momentum.candles'::regclass AND a.atttypid='numeric'::regtype", "true")
	// Таблица candles не может ссылаться на dataset другой среды вместо существующего своего.
	marketStatement(t, conn, "INSERT INTO momentum.candles SELECT 'testnet','mainonly',symbol,interval_code,open_time,open,high,low,close,volume,confirmed FROM momentum.candles LIMIT 1", false)
	marketStatement(t, conn, "INSERT INTO momentum.datasets VALUES ('mainnet','k','bybit_v5','linear',DEFAULT),('mainnet','other','bybit_v5','linear',DEFAULT),('testnet','k','bybit_v5','linear',DEFAULT)", true)
	for _, key := range []struct{ environment, dataset, symbol, interval, stamp string }{
		{"mainnet", "k", "BTCUSDT", "D", "2026-01-01 03:00+03"},
		{"testnet", "k", "BTCUSDT", "D", "2026-01-01 00:00+00"},
		{"mainnet", "other", "BTCUSDT", "D", "2026-01-01 00:00+00"},
		{"mainnet", "k", "ETHUSDT", "D", "2026-01-01 00:00+00"},
		{"mainnet", "k", "BTCUSDT", "60", "2026-01-01 00:00+00"},
		{"mainnet", "k", "BTCUSDT", "D", "2026-01-02 00:00+00"},
	} {
		marketStatement(t, conn, fmt.Sprintf("INSERT INTO momentum.candles VALUES ('%s','%s','%s','%s','%s',1,2,0.5,1.5,0,true)", key.environment, key.dataset, key.symbol, key.interval, key.stamp), true)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.candles WHERE dataset_id IN ('k','other')", "6")
	marketStatement(t, conn, "INSERT INTO momentum.candles VALUES ('mainnet','k','BTCUSDT','D','2026-01-03 00:00-10',1,2,0.5,1.5,0,true)", false)
}
