package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestReferenceMigration проверяет upgrade v2, пять checksum, repeat и отсутствие потери префикса.
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
	for i, payload := range []string{momentumBootstrap, momentumMarketCandlesSchema, momentumMarketSchema, momentumImportProgressSchema, momentumSignalJournalSchema} {
		checksum := sha256.Sum256([]byte(payload))
		migrationValue(t, db, fmt.Sprintf("SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=%d", i+1), fmt.Sprintf("%x", checksum))
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.schema_migrations", "8")
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

// TestImportProgressMigration проверяет настоящий upgrade v3, историю, repeat и rollback collision.
func TestImportProgressMigration(t *testing.T) {
	specs := []migrationSpec{{1, momentumBootstrap}, {2, momentumMarketCandlesSchema}, {3, momentumMarketSchema}}
	db := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(context.Background(), db, specs), nil)
	prefix := migrationState(t, db)
	marketStatement(t, db, "INSERT INTO momentum.datasets VALUES('testnet','d','bybit_v5','linear',DEFAULT)", true)
	marketStatement(t, db, "INSERT INTO momentum.candles VALUES('testnet','d','BTCUSDT','D','2026-01-01+00',1,2,0.5,1.5,0,true)", true)
	saved := migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.candles r")
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	if !strings.Contains(migrationState(t, db), strings.Trim(prefix, "[]")) {
		t.Fatal("v4 changed applied prefix")
	}
	checksum := sha256.Sum256([]byte(momentumImportProgressSchema))
	migrationValue(t, db, "SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=4", fmt.Sprintf("%x", checksum))
	migrationValue(t, db, "SELECT count(*) FROM momentum.schema_migrations", "8")
	migrationValue(t, db, "SELECT row_to_json(r)::text FROM momentum.candles r", saved)
	before := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	if migrationState(t, db) != before {
		t.Fatal("repeat changed history")
	}
	collision := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(context.Background(), collision, specs), nil)
	before = migrationState(t, collision)
	marketStatement(t, collision, "CREATE TABLE momentum.candle_import_progress(owned_marker TEXT)", true)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), collision), ErrMigration)
	if migrationState(t, collision) != before {
		t.Fatal("failed v4 changed prefix")
	}
	migrationValue(t, collision, "SELECT count(*) FROM information_schema.columns WHERE table_schema='momentum' AND table_name='candle_import_progress' AND column_name='owned_marker'", "1")
}

// TestSignalJournalMigration проверяет upgrade v4, данные, repeat и DDL collision.
func TestSignalJournalMigration(t *testing.T) {
	specs := []migrationSpec{{1, momentumBootstrap}, {2, momentumMarketCandlesSchema}, {3, momentumMarketSchema}, {4, momentumImportProgressSchema}}
	db := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(context.Background(), db, specs), nil)
	prefix := migrationState(t, db)
	for _, query := range []string{
		"INSERT INTO momentum.datasets VALUES('testnet','d','bybit_v5','linear',DEFAULT)",
		"INSERT INTO momentum.candles VALUES('testnet','d','BTCUSDT','D','2026-01-01 00:00+00',1,2,0.5,1.5,0,true)",
		"INSERT INTO momentum.mark_prices VALUES('testnet','d','BTCUSDT','D','2026-01-01 00:00+00',1,2,0.5,1.5,true)",
		"INSERT INTO momentum.funding VALUES('testnet','d','BTCUSDT','2026-01-01 00:00+00',0)",
		"INSERT INTO momentum.candle_import_progress(environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end) VALUES('testnet','d','BTCUSDT','D','2026-01-01 00:00+00','2026-01-04 00:00+00','2026-01-03 23:59:59.999+00')",
	} {
		marketStatement(t, db, query, true)
	}
	saved := make(map[string]string)
	for _, table := range []string{"datasets", "candles", "mark_prices", "funding", "candle_import_progress"} {
		saved[table] = migrationValue(t, db, "SELECT json_agg(r)::text FROM momentum."+table+" r")
	}
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	if !strings.Contains(migrationState(t, db), strings.Trim(prefix, "[]")) {
		t.Fatal("v5 changed applied v1-v4 prefix")
	}
	for table, want := range saved {
		migrationValue(t, db, "SELECT json_agg(r)::text FROM momentum."+table+" r", want)
	}
	checksum := sha256.Sum256([]byte(momentumSignalJournalSchema))
	migrationValue(t, db, "SELECT encode(checksum,'hex') FROM momentum.schema_migrations WHERE version=5", fmt.Sprintf("%x", checksum))
	migrationValue(t, db, "SELECT count(*) FROM momentum.schema_migrations", "8")
	migrationValue(t, db, "SELECT count(*) FROM momentum.signal_batches", "0")
	payload := []byte{0, 255, '\n', '"', 128}
	hash := sha256.Sum256(payload)
	marketStatement(t, db, fmt.Sprintf("INSERT INTO momentum.signal_batches(batch_hash,payload) VALUES(decode('%x','hex'),decode('%x','hex'))", hash[:], payload), true)
	journal := migrationValue(t, db, "SELECT row_to_json(r)::text FROM (SELECT *,xmin::text AS version,ctid::text AS locator FROM momentum.signal_batches) r")
	before := migrationState(t, db)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	if migrationState(t, db) != before {
		t.Fatal("repeat changed exact committed history")
	}
	migrationValue(t, db, "SELECT encode(payload,'hex') FROM momentum.signal_batches", fmt.Sprintf("%x", payload))
	migrationValue(t, db, "SELECT row_to_json(r)::text FROM (SELECT *,xmin::text AS version,ctid::text AS locator FROM momentum.signal_batches) r", journal)
	collision := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(context.Background(), collision, specs), nil)
	before = migrationState(t, collision)
	marketStatement(t, collision, "CREATE TABLE momentum.signal_batches(owned_marker TEXT)", true)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), collision), ErrMigration)
	if migrationState(t, collision) != before {
		t.Fatal("failed v5 changed v1-v4 prefix")
	}
	migrationValue(t, collision, "SELECT count(*) FROM information_schema.columns WHERE table_schema='momentum' AND table_name='signal_batches' AND column_name='owned_marker'", "1")
}

// TestSignalJournalSchema проверяет byte integrity/PK/time и границы семантики/WORM.
func TestSignalJournalSchema(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	migrationValue(t, db, "SELECT count(*) FROM momentum.signal_batches", "0")
	migrationValue(t, db, "SELECT count(*) FROM pg_extension WHERE extname='pgcrypto'", "0")
	migrationValue(t, db, "SELECT encode(pg_catalog.sha256('abc'::bytea),'hex')", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	payload := []byte{0, 255, '\n', '"', 128}
	hash := sha256.Sum256(payload)
	var defaulted bool
	err := db.QueryRow(`INSERT INTO momentum.signal_batches(batch_hash,payload) VALUES($1,$2)
 RETURNING created_at=CURRENT_TIMESTAMP AND isfinite(created_at)`, hash[:], payload).Scan(&defaulted)
	migrationResult(t, err, nil)
	if !defaulted {
		t.Fatal("created_at did not use transaction timestamp")
	}
	var got []byte
	migrationResult(t, db.QueryRow("SELECT payload FROM momentum.signal_batches WHERE batch_hash=$1", hash[:]).Scan(&got), nil)
	if !bytes.Equal(got, payload) {
		t.Fatal("binary payload changed, including NUL/non-UTF8")
	}
	snapshot := `SELECT row_to_json(r)::text FROM (SELECT encode(batch_hash,'hex') AS hash,
 encode(payload,'hex') AS payload,created_at,xmin::text AS version,ctid::text AS locator FROM momentum.signal_batches) r`
	before := migrationValue(t, db, snapshot)
	marketStatement(t, db, "INSERT INTO momentum.signal_batches SELECT * FROM momentum.signal_batches", false)
	marketStatement(t, db, "INSERT INTO momentum.signal_batches SELECT * FROM momentum.signal_batches ON CONFLICT(batch_hash) DO NOTHING", true)
	migrationValue(t, db, snapshot, before)
	for _, change := range []string{"batch_hash=NULL", "payload=NULL", "created_at=NULL",
		"batch_hash=decode('','hex')", "batch_hash=decode(repeat('00',31),'hex')", "batch_hash=decode(repeat('00',33),'hex')",
		"batch_hash=set_byte(batch_hash,0,get_byte(batch_hash,0)#1)", "payload=payload||decode('01','hex')",
		"payload=''::bytea,batch_hash=pg_catalog.sha256(''::bytea)",
		"created_at='infinity'", "created_at='-infinity'", "created_at='1969-12-31 23:59:59.999+00'", "created_at='294277-01-01 00:00+00'"} {
		marketStatement(t, db, "UPDATE momentum.signal_batches SET "+change, false)
	}
	migrationValue(t, db, snapshot, before)
	// SQL не проверяет JSON/93 inputs/source/replay и разрешает согласованный raw UPDATE.
	marketStatement(t, db, "UPDATE momentum.signal_batches SET payload=decode('ff00','hex'),batch_hash=pg_catalog.sha256(decode('ff00','hex')),created_at='1970-01-01 00:00+00'", true)
	migrationValue(t, db, "SELECT encode(payload,'hex') FROM momentum.signal_batches", "ff00")
	marketStatement(t, db, "UPDATE momentum.signal_batches SET created_at='2099-01-01 00:00+00'", true)
	conn, err := db.Conn(context.Background())
	migrationResult(t, err, nil)
	defer conn.Close()
	marketStatement(t, conn, "SET TimeZone='Pacific/Honolulu'", true)
	marketStatement(t, conn, "UPDATE momentum.signal_batches SET created_at='1970-01-01 03:00+03'", true)
	var created time.Time
	migrationResult(t, conn.QueryRowContext(context.Background(), "SELECT created_at FROM momentum.signal_batches").Scan(&created), nil)
	if !created.Equal(time.Unix(0, 0)) {
		t.Fatal("timestamp instant depended on session zone")
	}
}

// TestImportProgressSchema проверяет все локальные ограничения и честную границу переходов.
func TestImportProgressSchema(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	marketStatement(t, db, "INSERT INTO momentum.datasets VALUES('testnet','d','bybit_v5','linear',DEFAULT),('mainnet','d','bybit_v5','linear',DEFAULT),('testnet','other','bybit_v5','linear',DEFAULT),('mainnet','mainonly','bybit_v5','linear',DEFAULT)", true)
	insert := "INSERT INTO momentum.candle_import_progress(environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end) VALUES('testnet','d','BTCUSDT','D','2026-01-01+00','2026-01-04+00','2026-01-03 23:59:59.999+00')"
	marketStatement(t, db, insert, true)
	marketStatement(t, db, insert, false)
	marketStatement(t, db, insert+" ON CONFLICT DO NOTHING", true)
	for _, change := range []string{"environment='paper'", "dataset_id='missing'", "dataset_id='mainonly'", "symbol='btcusdt'", "symbol='BTCUSDC'", "interval_code='1'",
		"lower_open='infinity'", "lower_open='-infinity'", "lower_open='1969-01-01+00'", "lower_open='2026-01-01 01:00+00'",
		"upper_open='infinity'", "upper_open='-infinity'", "upper_open=lower_open", "upper_open=lower_open-INTERVAL '1 day'", "upper_open='2026-01-04 00:01+00'",
		"next_end='infinity'", "next_end='-infinity'", "next_end=upper_open", "next_end=lower_open-INTERVAL '2 milliseconds'", "next_end=upper_open-INTERVAL '1 microsecond'",
		"next_end=upper_open-INTERVAL '2 milliseconds'", "revision=-1", "revision=9223372036854775808", "revision=1", "state='unknown'", "reason='unknown'", "reason='empty'",
		"lower_open='294277-01-01 00:00+00'", "upper_open='294277-01-01 00:00+00'",
		"next_end='294277-01-01 00:00+00'", "last_server_time='294277-01-01 00:00+00'",
		"last_server_time='infinity'", "last_server_time='-infinity'", "last_server_time='1969-01-01+00'", "last_server_time='2026-01-04 00:00:00.000001+00'"} {
		marketStatement(t, db, "UPDATE momentum.candle_import_progress SET "+change, false)
	}
	for _, column := range []string{"environment", "dataset_id", "symbol", "interval_code", "lower_open", "upper_open", "next_end", "revision", "state", "reason"} {
		marketStatement(t, db, "UPDATE momentum.candle_import_progress SET "+column+"=NULL", false)
	}
	for _, change := range []string{
		"state='progress',revision=1,next_end='2026-01-02 23:59:59.999+00'",
		"state='progress',revision=1,last_server_time='2026-01-04+00'",
		"state='progress',revision=1,last_server_time='2026-01-03+00',next_end='2026-01-02 23:59:59.999+00'",
		"state='complete',revision=1,last_server_time='2026-01-04+00'",
		"state='complete',revision=1,next_end='2025-12-31 23:59:59.999+00'",
		"state='blocked',revision=1,last_server_time='2026-01-04+00'",
		"state='blocked',revision=1,reason='empty'",
		"state='blocked',revision=1,reason='gap',last_server_time='2026-01-03+00'",
		"state='blocked',revision=1,reason='source_boundary'",
		"state='blocked',revision=1,reason='empty',last_server_time='2026-01-04+00',next_end='2025-12-31 23:59:59.999+00'"} {
		marketStatement(t, db, "UPDATE momentum.candle_import_progress SET "+change, false)
	}
	for _, change := range []string{
		"state='progress',revision=1,last_server_time='2026-01-04+00',next_end='2026-01-02 23:59:59.999+00'",
		"state='complete',revision=2,last_server_time='2026-01-04+00',next_end='2025-12-31 23:59:59.999+00'",
		"state='blocked',reason='empty',revision=3,last_server_time='2026-01-04+00',next_end='2026-01-02 23:59:59.999+00'",
		"reason='gap'", "reason='unconfirmed',last_server_time='2026-01-03+00'"} {
		marketStatement(t, db, "UPDATE momentum.candle_import_progress SET "+change, true)
	}
	// SQL CHECK проверяет строку, но не запрещает rollback cursor/revision между валидными состояниями.
	marketStatement(t, db, "UPDATE momentum.candle_import_progress SET state='pending',reason='',revision=0,last_server_time=NULL,next_end=upper_open-INTERVAL '1 millisecond'", true)
	for _, change := range []string{"environment='mainnet'", "dataset_id='other'", "symbol='ETHUSDT'", "interval_code='60'", "lower_open='2025-12-31+00'", "upper_open='2026-01-05+00',next_end='2026-01-04 23:59:59.999+00'"} {
		marketStatement(t, db, "INSERT INTO momentum.candle_import_progress SELECT * FROM momentum.candle_import_progress LIMIT 1", false)
		marketStatement(t, db, "INSERT INTO momentum.candle_import_progress SELECT * FROM momentum.candle_import_progress ON CONFLICT DO NOTHING", true)
		tx, err := db.BeginTx(context.Background(), nil)
		migrationResult(t, err, nil)
		marketStatement(t, tx, "UPDATE momentum.candle_import_progress SET "+change, true)
		marketStatement(t, tx, insert, true)
		migrationResult(t, tx.Rollback(), nil)
	}
	marketStatement(t, db, "UPDATE momentum.candle_import_progress SET lower_open='1970-01-01+00'", true)
	marketStatement(t, db, "UPDATE momentum.candle_import_progress SET state='blocked',reason='source_boundary',revision=1", true)
	marketStatement(t, db, "UPDATE momentum.candle_import_progress SET last_server_time='2026-01-04+00'", false)
	marketStatement(t, db, "UPDATE momentum.candle_import_progress SET state='pending',reason='',revision=0", true)
	marketStatement(t, db, "UPDATE momentum.candle_import_progress SET interval_code='60',lower_open='2026-01-01 01:00+00',upper_open='2026-01-01 03:00+00',next_end='2026-01-01 02:59:59.999+00'", true)
	marketStatement(t, db, "UPDATE momentum.candle_import_progress SET lower_open='2026-01-01 01:01+00'", false)
	marketStatement(t, db, "UPDATE momentum.candle_import_progress SET upper_open='2026-01-01 03:01+00'", false)
	conn, err := db.Conn(context.Background())
	migrationResult(t, err, nil)
	defer conn.Close()
	marketStatement(t, conn, "SET TimeZone='Pacific/Honolulu'", true)
	marketStatement(t, conn, "UPDATE momentum.candle_import_progress SET lower_open='2026-01-01 04:00+03'", true)
	migrationValue(t, db, "SELECT lower_open=TIMESTAMPTZ '2026-01-01 01:00+00' FROM momentum.candle_import_progress", "true")
	fresh := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), fresh), nil)
	migrationValue(t, fresh, "SELECT count(*) FROM momentum.candle_import_progress", "0")
}
