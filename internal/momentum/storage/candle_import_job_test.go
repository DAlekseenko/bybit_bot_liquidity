package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// candleImportJobFixture создаёт зарегистрированный источник без задания.
func candleImportJobFixture(t *testing.T) (*sql.DB, CandleImportWindow) {
	t.Helper()
	db, stream, page := candleWriterFixture(t)
	upper := page.ServerTime
	return db, CandleImportWindow{stream, upper.Add(-48 * time.Hour), upper}
}

// TestCandleImportJobLifecycle проверяет init, restart через точный read и сохранение терминального состояния.
func TestCandleImportJobLifecycle(t *testing.T) {
	db, stream, page := candleWriterFixture(t)
	tz := time.FixedZone("non-UTC", 3*3600)
	key := CandleImportWindow{stream, page.ServerTime.Add(-48 * time.Hour).In(tz), page.ServerTime.In(tz)}
	ctx := context.Background()
	// Свеча вне записи о прогрессе не означает, что job уже существует.
	tx := candleWriterTx(t, db)
	migrationResult(t, AppendCandlePage(ctx, tx, stream, page), nil)
	migrationResult(t, tx.Commit(), nil)
	_, err := ReadCandleImport(ctx, db, key)
	migrationResult(t, err, ErrCandleImportMissing)
	migrationResult(t, InitCandleImport(ctx, db, key), nil)
	row, err := ReadCandleImport(ctx, db, key)
	migrationResult(t, err, nil)
	want := CandleImportCheckpoint{0, key.UpperOpen.Add(-time.Millisecond).UTC(), "pending"}
	if row.Checkpoint != want || row.LastServerTime != nil || row.Reason != "" ||
		row.Window.LowerOpen != key.LowerOpen.UTC() || row.Window.UpperOpen != key.UpperOpen.UTC() {
		t.Fatalf("pending: %+v", row)
	}
	for _, state := range []string{"progress", "complete"} {
		tx := candleWriterTx(t, db)
		checkpoint, err := AdvanceCandleImportPage(ctx, tx, key, row.Checkpoint, page)
		migrationResult(t, err, nil)
		beforeCommit, err := ReadCandleImport(ctx, db, key)
		migrationResult(t, err, nil)
		if !reflect.DeepEqual(beforeCommit, row) {
			t.Fatal("read observed uncommitted advance")
		}
		migrationResult(t, tx.Commit(), nil)
		xmin := migrationValue(t, db, "SELECT xmin::text FROM momentum.candle_import_progress")
		migrationResult(t, InitCandleImport(ctx, db, key), ErrCandleImportNotInserted)
		row, err = ReadCandleImport(ctx, db, key)
		migrationResult(t, err, nil)
		if row.Checkpoint != checkpoint || row.Checkpoint.State != state ||
			row.LastServerTime == nil || *row.LastServerTime != page.ServerTime || row.Reason != "" {
			t.Fatalf("stored checkpoint: %+v", row)
		}
		if migrationValue(t, db, "SELECT xmin::text FROM momentum.candle_import_progress") != xmin {
			t.Fatal("init rewrote durable job")
		}
		beforeInit := row
		migrationResult(t, InitCandleImport(ctx, db, key), ErrCandleImportNotInserted)
		row, err = ReadCandleImport(ctx, db, key)
		migrationResult(t, err, nil)
		if !reflect.DeepEqual(row, beforeInit) {
			t.Fatal("repeat init changed durable snapshot")
		}
		page.Candles[0].OpenTime = key.LowerOpen.UTC()
	}
}

// TestCandleImportJobKeys проверяет точное составное равенство и пустой namespace.
func TestCandleImportJobKeys(t *testing.T) {
	db, key := candleImportJobFixture(t)
	ctx := context.Background()
	migrationResult(t, InitCandleImport(ctx, db, key), nil)
	variants := []CandleImportWindow{key, key, key, key, key, key}
	variants[0].Stream.Environment = "mainnet"
	variants[1].Stream.DatasetID = "different"
	variants[2].Stream.Symbol = "ETHUSDT"
	variants[3].Stream.IntervalCode = "60"
	variants[4].LowerOpen = key.LowerOpen.Add(24 * time.Hour)
	variants[5].UpperOpen = key.UpperOpen.Add(24 * time.Hour)
	for i, other := range variants {
		_, err := ReadCandleImport(ctx, db, other)
		migrationResult(t, err, ErrCandleImportMissing)
		if i == 1 {
			migrationValue(t, db, "SELECT count(*) FROM momentum.datasets WHERE environment='testnet' AND dataset_id='different'", "0")
			migrationResult(t, InitCandleImport(ctx, db, other), ErrCandleImportNotInserted)
			migrationValue(t, db, "SELECT count(*) FROM momentum.datasets WHERE environment='testnet' AND dataset_id='different'", "0")
			continue
		}
		migrationResult(t, InitCandleImport(ctx, db, other), nil)
		got, err := ReadCandleImport(ctx, db, other)
		migrationResult(t, err, nil)
		if got.Window != (CandleImportWindow{other.Stream, other.LowerOpen.UTC(), other.UpperOpen.UTC()}) ||
			got.Checkpoint.NextEnd != other.UpperOpen.Add(-time.Millisecond) {
			t.Fatalf("key %d: %+v", i, got)
		}
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.candle_import_progress", "6")
	migrationValue(t, db, "SELECT count(*) FROM momentum.candles", "0")
}

// TestCandleImportJobBlocked проверяет валидные blocked записи и явное отсутствие ответа источника.
func TestCandleImportJobBlocked(t *testing.T) {
	db, key := candleImportJobFixture(t)
	ctx := context.Background()
	key.LowerOpen = time.UnixMilli(0).UTC()
	migrationResult(t, InitCandleImport(ctx, db, key), nil)
	for _, tc := range []struct {
		reason, observed string
		hasObservation   bool
		serverTime       *time.Time
	}{
		{"source_boundary", "NULL", false, nil},
		{"empty", "upper_open", true, ptrTime(key.UpperOpen)},
		{"gap", "upper_open+INTERVAL '1 millisecond'", true, ptrTime(key.UpperOpen.Add(time.Millisecond))},
		{"unconfirmed", "lower_open", true, ptrTime(key.LowerOpen)},
	} {
		marketStatement(t, db, "UPDATE momentum.candle_import_progress SET state='blocked',revision=1,reason='"+tc.reason+"',last_server_time="+tc.observed, true)
		migrationResult(t, InitCandleImport(ctx, db, key), ErrCandleImportNotInserted)
		got, err := ReadCandleImport(ctx, db, key)
		migrationResult(t, err, nil)
		if got.Checkpoint.State != "blocked" || got.Reason != tc.reason || got.Checkpoint.Revision != 1 ||
			(got.LastServerTime != nil) != tc.hasObservation || got.Checkpoint.NextEnd != key.UpperOpen.Add(-time.Millisecond) ||
			!reflect.DeepEqual(got.LastServerTime, tc.serverTime) {
			t.Fatalf("%s: %+v", tc.reason, got)
		}
	}
}

// TestCandleImportJobCorrupt проверяет чтение непригодных строк и разделение типов ошибок.
func TestCandleImportJobCorrupt(t *testing.T) {
	db, key := candleImportJobFixture(t)
	ctx := context.Background()
	migrationResult(t, InitCandleImport(ctx, db, key), nil)
	marketStatement(t, db, `DO $$
	 DECLARE rule RECORD;
	 BEGIN
	  FOR rule IN SELECT conname FROM pg_constraint WHERE contype='c'
	   AND conrelid='momentum.candle_import_progress'::regclass LOOP
	   EXECUTE format('ALTER TABLE momentum.candle_import_progress DROP CONSTRAINT %I',rule.conname);
	  END LOOP;
	 END $$`, true)
	for _, invalid := range []string{
		"revision=-1", "revision=1,state='progress'", "revision=1,state='complete',last_server_time=upper_open",
		"revision=1,state='blocked',reason='source_boundary'", "state='unexpected'", "reason='unexpected'",
		"next_end='infinity'", "next_end=upper_open-INTERVAL '2 milliseconds'", "last_server_time='infinity'",
		"revision=1,state='blocked',reason='unconfirmed',last_server_time=upper_open+INTERVAL '1 microsecond'",
	} {
		marketStatement(t, db, "UPDATE momentum.candle_import_progress SET "+invalid, true)
		got, err := ReadCandleImport(ctx, db, key)
		if !errors.Is(err, ErrCandleImportCorrupt) || !reflect.DeepEqual(got, StoredCandleImport{}) {
			t.Fatalf("invalid row %s: %+v, %v", invalid, got, err)
		}
		migrationResult(t, InitCandleImport(ctx, db, key), ErrCandleImportNotInserted)
		marketStatement(t, db, "UPDATE momentum.candle_import_progress SET revision=0,state='pending',reason='',last_server_time=NULL,next_end=upper_open-INTERVAL '1 millisecond'", true)
	}
	marketStatement(t, db, "ALTER TABLE momentum.candle_import_progress ALTER COLUMN state DROP NOT NULL", true)
	marketStatement(t, db, "UPDATE momentum.candle_import_progress SET state=NULL", true)
	_, err := ReadCandleImport(ctx, db, key)
	migrationResult(t, err, ErrCandleImportCorrupt)
	marketStatement(t, db, "UPDATE momentum.candle_import_progress SET state='pending'", true)
	marketStatement(t, db, "ALTER TABLE momentum.datasets DROP CONSTRAINT datasets_source_check", true)
	marketStatement(t, db, "UPDATE momentum.datasets SET source='unexpected' WHERE environment='testnet'", true)
	_, err = ReadCandleImport(ctx, db, key)
	migrationResult(t, err, ErrCandleImportCorrupt)
}

// TestCandleImportJobFailure проверяет ошибки без SQL диагностики и отменённый контекст.
func TestCandleImportJobFailure(t *testing.T) {
	db, key := candleImportJobFixture(t)
	ctx := context.Background()
	call, cancel := context.WithCancel(ctx)
	cancel()
	migrationResult(t, InitCandleImport(call, db, key), context.Canceled)
	_, err := ReadCandleImport(call, db, key)
	migrationResult(t, err, context.Canceled)
	migrationResult(t, InitCandleImport(ctx, nil, key), ErrCandleArgument)
	bad := key
	bad.LowerOpen = key.LowerOpen.Add(time.Millisecond)
	_, err = ReadCandleImport(ctx, db, bad)
	migrationResult(t, err, ErrCandleArgument)
	marketStatement(t, db, `CREATE FUNCTION momentum.test_reject_init() RETURNS trigger LANGUAGE plpgsql AS $$
	 BEGIN RAISE EXCEPTION 'sensitive_sql_marker'; END $$;
	 CREATE TRIGGER test_reject_init BEFORE INSERT ON momentum.candle_import_progress
	 FOR EACH ROW EXECUTE FUNCTION momentum.test_reject_init()`, true)
	err = InitCandleImport(ctx, db, key)
	if !errors.Is(err, ErrCandleImportInitUnknown) || !errors.Is(err, ErrCandles) || strings.Contains(err.Error(), "sensitive_sql_marker") {
		t.Fatalf("insert error: %v", err)
	}
	_, err = ReadCandleImport(ctx, db, key)
	migrationResult(t, err, ErrCandleImportMissing)
}

// TestCandleImportJobConcurrent наблюдает блокировку и проверяет оба исхода конкурентной вставки.
func TestCandleImportJobConcurrent(t *testing.T) {
	for _, mode := range []string{"commit", "rollback", "cancel"} {
		db, key := candleImportJobFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		dsn, err := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
		migrationResult(t, err, nil)
		dsn.Path, dsn.RawPath = "/"+migrationValue(t, db, "SELECT current_database()"), ""
		other, err := OpenMomentumPostgres(ctx, dsn.String())
		migrationResult(t, err, nil)
		t.Cleanup(func() { _ = other.Close() })
		other.SetMaxOpenConns(1)
		var pid int
		migrationResult(t, other.QueryRow("SELECT pg_backend_pid()").Scan(&pid), nil)
		winner := candleWriterTx(t, db)
		_, err = winner.Exec(`INSERT INTO momentum.candle_import_progress
		 (environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end)
		 VALUES($1,$2,$3,$4,$5,$6,$7)`, key.Stream.Environment, key.Stream.DatasetID,
			key.Stream.Symbol, key.Stream.IntervalCode, key.LowerOpen, key.UpperOpen, key.UpperOpen.Add(-time.Millisecond))
		migrationResult(t, err, nil)
		call, stop := context.WithCancel(ctx)
		defer stop()
		result := make(chan error, 1)
		go func() { result <- InitCandleImport(call, other, key) }()
		for {
			var waiting bool
			err := db.QueryRowContext(ctx, "SELECT cardinality(pg_blocking_pids($1::int))>0", pid).Scan(&waiting)
			migrationResult(t, err, nil)
			if waiting {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		switch mode {
		case "commit":
			migrationResult(t, winner.Commit(), nil)
			migrationResult(t, <-result, ErrCandleImportNotInserted)
		case "rollback":
			migrationResult(t, winner.Rollback(), nil)
			migrationResult(t, <-result, nil)
		case "cancel":
			stop()
			got := <-result
			migrationResult(t, got, context.Canceled)
			migrationResult(t, got, ErrCandleImportInitUnknown)
			migrationResult(t, winner.Commit(), nil)
		}
		got, err := ReadCandleImport(ctx, other, key)
		migrationResult(t, err, nil)
		if got.Checkpoint != (CandleImportCheckpoint{0, key.UpperOpen.Add(-time.Millisecond), "pending"}) ||
			got.Reason != "" || got.LastServerTime != nil {
			t.Fatalf("concurrent init did not preserve exact pending state: %+v", got)
		}
		migrationValue(t, db, "SELECT count(*) FROM momentum.candle_import_progress", "1")
	}
}

// ptrTime создаёт ожидаемый указатель времени для сравнения снимков.
func ptrTime(stamp time.Time) *time.Time {
	return &stamp
}
