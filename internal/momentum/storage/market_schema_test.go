package storage

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

// TestReferenceMigration проверяет upgrade v2, три checksum, repeat и отсутствие потери префикса.
func TestReferenceMigration(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(context.Background(), db, []migrationSpec{{1, momentumBootstrap}, {2, momentumMarketCandlesSchema}}), nil)
	prefix := migrationState(t, db)
	marketStatement(t, db, "INSERT INTO momentum.datasets VALUES ('testnet','saved','bybit_v5','linear',DEFAULT)", true)
	marketStatement(t, db, "INSERT INTO momentum.candles VALUES ('testnet','saved','BTCUSDT','D','2026-01-01 00:00+00',100,110,90,105,0,true)", true)
	saved := migrationValue(t, db, "SELECT row_to_json(c)::text FROM momentum.candles c")
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	if !strings.Contains(migrationState(t, db), strings.Trim(prefix, "[]")) {
		t.Fatal("upgrade changed committed v1/v2 checksum/timestamp")
	}
	for i, payload := range []string{momentumBootstrap, momentumMarketCandlesSchema, momentumMarketSchema} {
		checksum := sha256.Sum256([]byte(payload))
		migrationValue(t, db, fmt.Sprintf("SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=%d", i+1), fmt.Sprintf("%x", checksum))
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.schema_migrations", "3")
	migrationValue(t, db, "SELECT row_to_json(c)::text FROM momentum.candles c", saved)
	before := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	if migrationState(t, db) != before {
		t.Fatal("repeat changed exact committed history")
	}
	for _, table := range []string{"mark_prices", "funding"} {
		migrationValue(t, db, "SELECT count(*) FROM momentum."+table, "0")
	}
	collision := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(context.Background(), collision, []migrationSpec{{1, momentumBootstrap}, {2, momentumMarketCandlesSchema}}), nil)
	prefix = migrationState(t, collision)
	marketStatement(t, collision, "CREATE TABLE momentum.funding (owned_marker TEXT)", true)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), collision), ErrMigration)
	if migrationState(t, collision) != prefix {
		t.Fatal("failed v3 changed committed v1/v2 history")
	}
	migrationValue(t, collision, "SELECT to_regclass('momentum.mark_prices') IS NULL", "true")
	migrationValue(t, collision, "SELECT count(*) FROM information_schema.columns WHERE table_schema='momentum' AND table_name='funding' AND column_name='owned_marker'", "1")
}

// TestReferenceSchema проверяет provenance, ключи, точность и все семейства CHECK/NOT NULL/FK.
func TestReferenceSchema(t *testing.T) {
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
	marketStatement(t, conn, "INSERT INTO momentum.datasets VALUES ('mainnet','mainonly','bybit_v5','linear',DEFAULT)", true)
	inserts := map[string]string{
		"mark_prices": "('%s','d','BTCUSDT','D','2026-01-01 00:00:00+00',100,110,90,105,true)",
		"funding":     "('%s','d','BTCUSDT','2026-01-01 01:23:45.123+00',0)",
	}
	for table, values := range inserts {
		for _, environment := range []string{"mainnet", "testnet"} {
			query := "INSERT INTO momentum." + table + " VALUES " + fmt.Sprintf(values, environment)
			marketStatement(t, conn, query, true)
			before := migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum."+table+" r WHERE environment='"+environment+"'")
			marketStatement(t, conn, query, false)
			marketStatement(t, conn, query+" ON CONFLICT DO NOTHING", true)
			migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum."+table+" r WHERE environment='"+environment+"'", before)
		}
		migrationValue(t, db, "SELECT count(*) FROM momentum."+table, "2")
		for _, change := range []string{"environment='paper'", "dataset_id='missing'", "symbol='btcusdt'"} {
			marketStatement(t, conn, "UPDATE momentum."+table+" SET "+change, false)
		}
	}
	for _, column := range []string{"open", "high", "low", "close"} {
		for _, value := range []string{"0", "-1", "'NaN'", "'Infinity'", "'-Infinity'"} {
			marketStatement(t, conn, "UPDATE momentum.mark_prices SET "+column+"="+value, false)
		}
	}
	for _, change := range []string{"interval_code='1'", "open_time='infinity'", "open_time='-infinity'", "open_time='1969-01-01 00:00:00+00'", "open_time='2026-01-01 01:00+00'", "open_time='2026-01-01 00:00+03'", "high=101", "low=101"} {
		marketStatement(t, conn, "UPDATE momentum.mark_prices SET "+change, false)
	}
	marketStatement(t, conn, "UPDATE momentum.mark_prices SET open_time='2026-01-02 03:00+03'", true)
	marketStatement(t, conn, "UPDATE momentum.mark_prices SET interval_code='60',open_time='2026-01-01 01:00+00'", true)
	marketStatement(t, conn, "UPDATE momentum.mark_prices SET open_time='2026-01-01 01:01+00'", false)
	marketStatement(t, conn, "UPDATE momentum.mark_prices SET confirmed=false", true)
	for _, change := range []string{"rate='NaN'", "rate='Infinity'", "rate='-Infinity'", "funding_time='infinity'", "funding_time='-infinity'", "funding_time='1969-01-01 00:00:00+00'", "funding_time='2026-01-01 00:00:00.123456+00'"} {
		marketStatement(t, conn, "UPDATE momentum.funding SET "+change, false)
	}
	for _, value := range []string{"-0.001", "0.001", "0"} {
		marketStatement(t, conn, "UPDATE momentum.funding SET rate="+value, true)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding WHERE funding_time='2026-01-01 00:00+00'", "0")
	migrationValue(t, db, "SELECT rate::text FROM momentum.funding LIMIT 1", "0")
	marketStatement(t, conn, "UPDATE momentum.mark_prices SET close=107", true)
	migrationValue(t, db, "SELECT close::text FROM momentum.mark_prices LIMIT 1", "107")
	marketStatement(t, conn, "INSERT INTO momentum.candles VALUES ('mainnet','d','BTCUSDT','D','2026-01-01 00:00+00',100,110,90,105,0,true)", true)
	migrationValue(t, db, "SELECT close::text FROM momentum.candles LIMIT 1", "105")
	exact := "0." + strings.Repeat("12345678", 8)
	marketStatement(t, conn, "UPDATE momentum.mark_prices SET close="+exact+",low=0.1", true)
	migrationValue(t, db, "SELECT close::text FROM momentum.mark_prices LIMIT 1", exact)
	for table, columns := range map[string][]string{
		"mark_prices": {"environment", "dataset_id", "symbol", "interval_code", "open_time", "open", "high", "low", "close", "confirmed"},
		"funding":     {"environment", "dataset_id", "symbol", "funding_time", "rate"},
	} {
		for _, column := range columns {
			marketStatement(t, conn, "UPDATE momentum."+table+" SET "+column+"=NULL", false)
		}
	}
	for _, table := range []string{"mark_prices", "funding"} {
		migrationValue(t, db, "SELECT bool_and(a.atttypmod = -1) FROM pg_catalog.pg_attribute a WHERE a.attrelid='momentum."+table+"'::regclass AND a.atttypid='numeric'::regtype", "true")
	}
	for table, values := range inserts {
		missing := strings.Replace(fmt.Sprintf(values, "testnet"), "'d'", "'mainonly'", 1)
		marketStatement(t, conn, "INSERT INTO momentum."+table+" VALUES "+missing, false)
	}
	marketStatement(t, conn, "INSERT INTO momentum.datasets VALUES ('mainnet','k','bybit_v5','linear',DEFAULT),('mainnet','other','bybit_v5','linear',DEFAULT),('testnet','k','bybit_v5','linear',DEFAULT)", true)
	for _, key := range []struct{ environment, dataset, symbol, interval, stamp string }{
		{"mainnet", "k", "BTCUSDT", "D", "2026-01-01 03:00+03"},
		{"testnet", "k", "BTCUSDT", "D", "2026-01-01 00:00+00"},
		{"mainnet", "other", "BTCUSDT", "D", "2026-01-01 00:00+00"},
		{"mainnet", "k", "ETHUSDT", "D", "2026-01-01 00:00+00"},
		{"mainnet", "k", "BTCUSDT", "60", "2026-01-01 00:00+00"},
		{"mainnet", "k", "BTCUSDT", "D", "2026-01-02 00:00+00"},
	} {
		marketStatement(t, conn, fmt.Sprintf("INSERT INTO momentum.mark_prices VALUES ('%s','%s','%s','%s','%s',1,2,0.5,1.5,true)", key.environment, key.dataset, key.symbol, key.interval, key.stamp), true)
		if key.interval != "60" {
			marketStatement(t, conn, fmt.Sprintf("INSERT INTO momentum.funding VALUES ('%s','%s','%s','%s',0)", key.environment, key.dataset, key.symbol, key.stamp), true)
		}
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices WHERE dataset_id IN ('k','other')", "6")
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding WHERE dataset_id IN ('k','other')", "5")
	marketStatement(t, conn, "INSERT INTO momentum.mark_prices VALUES ('mainnet','k','BTCUSDT','D','2026-01-03 00:00-10',1,2,0.5,1.5,true)", false)
}
