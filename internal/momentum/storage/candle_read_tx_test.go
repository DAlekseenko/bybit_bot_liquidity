package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// candleReadTx открывает caller-owned снимок с независимым завершением caller.
func candleReadTx(t *testing.T, db *sql.DB, options ...sql.TxOptions) *sql.Tx {
	t.Helper()
	option := sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	if len(options) != 0 {
		option = options[0]
	}
	tx, err := db.BeginTx(t.Context(), &option)
	migrationResult(t, err, nil)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// candleReadTxError проверяет точный набор sentinel и отсутствие частичных данных/утечек.
func candleReadTxError(t *testing.T, rows []StoredCandle, err, want error) {
	t.Helper()
	if rows != nil || err == nil || !errors.Is(err, ErrCandles) || !errors.Is(err, want) {
		t.Fatalf("rows=%+v error=%v want=%v", rows, err, want)
	}
	for _, kind := range []error{ErrCandleArgument, ErrCandleDataset, ErrCandleLimit, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, kind) != (want == kind) {
			t.Fatalf("unexpected error class %v: %v", kind, err)
		}
	}
	for _, forbidden := range []string{"secret-marker", "pq:", "momentum.candles", "postgres://"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("unsafe error: %v", err)
		}
	}
}

// TestReadCandleRangeTxArguments проверяет отказ до SQL и приоритет отмены.
func TestReadCandleRangeTxArguments(t *testing.T) {
	key := CandleStream{"testnet", "fixture", "BTCUSDT", "D"}
	start, end := time.Unix(0, 0), time.Unix(3*86400, 0)
	for _, tc := range []struct {
		ctx        context.Context
		tx         *sql.Tx
		key        CandleStream
		start, end time.Time
		limit      int
	}{
		{nil, nil, key, start, end, 2}, {t.Context(), nil, key, start, end, 2},
		{t.Context(), new(sql.Tx), key, start, end, 0}, {t.Context(), new(sql.Tx), key, start, end, 201},
		{t.Context(), new(sql.Tx), CandleStream{}, start, end, 2},
		{t.Context(), new(sql.Tx), key, start, start, 2}, {t.Context(), new(sql.Tx), key, end, start, 2},
		{t.Context(), new(sql.Tx), key, start.Add(time.Nanosecond), end, 2},
		{t.Context(), new(sql.Tx), key, start, end.Add(time.Hour), 2},
	} {
		rows, err := ReadCandleRangeTx(tc.ctx, tc.tx, tc.key, tc.start, tc.end, tc.limit)
		candleReadTxError(t, rows, err, ErrCandleArgument)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, err := ReadCandleRangeTx(ctx, nil, key, start, end, 2)
	candleReadTxError(t, rows, err, context.Canceled)
}

// TestReadCandleRangeTxOwnership проверяет режим, полноту, namespace и сохранённое владение.
func TestReadCandleRangeTxOwnership(t *testing.T) {
	db, key := candleReadDB(t)
	start, end := time.Unix(0, 0), time.Unix(3*86400, 0)
	for _, option := range []sql.TxOptions{
		{Isolation: sql.LevelReadCommitted, ReadOnly: true}, {Isolation: sql.LevelRepeatableRead},
		{Isolation: sql.LevelSerializable, ReadOnly: true},
	} {
		tx := candleReadTx(t, db, option)
		rows, err := ReadCandleRangeTx(t.Context(), tx, key, start, end, 2)
		candleReadTxError(t, rows, err, ErrCandleArgument)
		migrationResult(t, tx.Rollback(), nil)
	}
	tx := candleReadTx(t, db)
	for _, tc := range []struct {
		key   CandleStream
		limit int
		want  error
	}{
		{key, 1, ErrCandleLimit}, {key, 0, ErrCandleArgument},
		{CandleStream{"testnet", "secret-marker", "BTCUSDT", "D"}, 2, ErrCandleDataset},
	} {
		rows, err := ReadCandleRangeTx(t.Context(), tx, tc.key, start, end, tc.limit)
		candleReadTxError(t, rows, err, tc.want)
		var alive int
		migrationResult(t, tx.QueryRowContext(t.Context(), "SELECT 1").Scan(&alive), nil)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, err := ReadCandleRangeTx(ctx, tx, key, start, end, 2)
	candleReadTxError(t, rows, err, context.Canceled)
	rows, err = ReadCandleRangeTx(t.Context(), tx, key, start, end, 2)
	migrationResult(t, err, nil)
	want, err := ReadCandleRange(t.Context(), db, key, start, end, 2)
	if err != nil || !reflect.DeepEqual(rows, want) {
		t.Fatalf("shared projection changed: rows=%+v want=%+v error=%v", rows, want, err)
	}
	key.Symbol = "ETHUSDT"
	empty, err := ReadCandleRangeTx(t.Context(), tx, key, start, end, 2)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("known empty: %+v %v", empty, err)
	}
	migrationResult(t, tx.Commit(), nil)
	rows, err = ReadCandleRangeTx(t.Context(), tx, key, start, end, 2)
	candleReadTxError(t, rows, err, ErrCandles)
}

// TestReadCandleRangeTxFailures проверяет malformed row и настоящую SQL-ошибку без rollback reader.
func TestReadCandleRangeTxFailures(t *testing.T) {
	db, key := candleReadDB(t)
	start, end := time.Unix(0, 0), time.Unix(3*86400, 0)
	_, err := db.Exec("UPDATE momentum.candles SET open=1." + strings.Repeat("0", 64) + "1 WHERE open_time=TIMESTAMPTZ '1970-01-03+00'")
	migrationResult(t, err, nil)
	tx := candleReadTx(t, db)
	rows, err := ReadCandleRangeTx(t.Context(), tx, key, start, end, 2)
	candleReadTxError(t, rows, err, ErrCandles)
	migrationResult(t, tx.Rollback(), nil)
	tx = candleReadTx(t, openTestPostgres(t))
	rows, err = ReadCandleRangeTx(t.Context(), tx, key, start, end, 2)
	candleReadTxError(t, rows, err, ErrCandles)
	migrationResult(t, tx.Rollback(), nil)
}

// TestReadCandleRangeTxSnapshot проверяет 1080h двух trade рядов в одном снимке после late insert.
func TestReadCandleRangeTxSnapshot(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(t.Context(), db), nil)
	migrationResult(t, RegisterDataset(t.Context(), db, "testnet", "snapshot"), nil)
	start := time.Unix(86400, 0).UTC()
	end := start.Add(1080 * time.Hour)
	_, err := db.ExecContext(t.Context(), `INSERT INTO momentum.candles SELECT 'testnet','snapshot',s,'60',
 $1::timestamptz+n*INTERVAL '1 hour',9007199254740993.00000000000000000001,
 9007199254740993.00000000000000000001,9007199254740993.00000000000000000001,
 9007199254740993.00000000000000000001,0,n<>300
 FROM (VALUES('BTCUSDT'),('ETHUSDT')) symbols(s) CROSS JOIN generate_series(0,1079) n WHERE n<>500`, start)
	migrationResult(t, err, nil)
	tx := candleReadTx(t, db)
	key := CandleStream{"testnet", "snapshot", "BTCUSDT", "60"}
	tooMany, err := ReadCandleRangeTx(t.Context(), tx, key, start, end, 200)
	candleReadTxError(t, tooMany, err, ErrCandleLimit)
	first, err := ReadCandleRangeTx(t.Context(), tx, key, start, start.Add(200*time.Hour), 200)
	if err != nil || len(first) != 200 {
		t.Fatalf("first page: %d %v", len(first), err)
	}
	_, err = db.ExecContext(t.Context(), `INSERT INTO momentum.candles SELECT 'testnet','snapshot',s,'60',$1,
 9007199254740993.00000000000000000001,9007199254740993.00000000000000000001,
 9007199254740993.00000000000000000001,9007199254740993.00000000000000000001,0,true
 FROM (VALUES('BTCUSDT'),('ETHUSDT')) symbols(s)`, start.Add(500*time.Hour))
	migrationResult(t, err, nil)
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		key.Symbol = symbol
		var materialized []StoredCandle
		for lower := start; lower.Before(end); lower = lower.Add(200 * time.Hour) {
			upper := lower.Add(200 * time.Hour)
			if upper.After(end) {
				upper = end
			}
			rows, err := ReadCandleRangeTx(t.Context(), tx, key, lower, upper, 200)
			migrationResult(t, err, nil)
			materialized = append(materialized, rows...)
		}
		if len(materialized) != 1079 || materialized[300].Confirmed || materialized[500].OpenTime != start.Add(501*time.Hour) {
			t.Fatalf("snapshot/confirmed/gap changed for %s: %d", symbol, len(materialized))
		}
		value, err := materialized[0].Close.WireDecimal()
		if err != nil || value != "9007199254740993.00000000000000000001" || materialized[1078].OpenTime != end.Add(-time.Hour) {
			t.Fatalf("exact projection changed: %q %v", value, err)
		}
		current, err := ReadCandleRange(t.Context(), db, key, start.Add(500*time.Hour), start.Add(501*time.Hour), 1)
		if err != nil || len(current) != 1 {
			t.Fatalf("new snapshot must see late insert: %+v %v", current, err)
		}
	}
	migrationResult(t, tx.Commit(), nil)
}

// TestReadCandleRangeTxRegistryAndTimezone проверяет provenance, offset bounds и timezone сессии.
func TestReadCandleRangeTxRegistryAndTimezone(t *testing.T) {
	db, key := candleReadDB(t)
	start, end := time.Unix(0, 0), time.Unix(3*86400, 0)
	_, err := db.Exec("ALTER TABLE momentum.datasets DROP CONSTRAINT datasets_source_check, DROP CONSTRAINT datasets_category_check")
	migrationResult(t, err, nil)
	for _, tc := range []struct{ column, value string }{{"source", "wrong-source"}, {"category", "spot"}} {
		_, err = db.Exec("UPDATE momentum.datasets SET "+tc.column+"=$1 WHERE environment=$2 AND dataset_id=$3", tc.value, key.Environment, key.DatasetID)
		migrationResult(t, err, nil)
		tx := candleReadTx(t, db)
		rows, err := ReadCandleRangeTx(t.Context(), tx, key, start, end, 2)
		candleReadTxError(t, rows, err, ErrCandles)
		var alive int
		migrationResult(t, tx.QueryRowContext(t.Context(), "SELECT 1").Scan(&alive), nil)
		migrationResult(t, tx.Rollback(), nil)
		_, err = db.Exec("UPDATE momentum.datasets SET "+tc.column+"=$1 WHERE environment=$2 AND dataset_id=$3", map[string]string{"source": "bybit_v5", "category": "linear"}[tc.column], key.Environment, key.DatasetID)
		migrationResult(t, err, nil)
	}
	db.SetMaxOpenConns(1)
	tx := candleReadTx(t, db)
	_, err = tx.ExecContext(t.Context(), "SET LOCAL TIME ZONE 'Pacific/Honolulu'")
	migrationResult(t, err, nil)
	zone := time.FixedZone("plus3", 3*3600)
	localStart, localEnd := start.In(zone), end.In(zone)
	beforeStart, beforeEnd := localStart, localEnd
	rows, err := ReadCandleRangeTx(t.Context(), tx, key, localStart, localEnd, 2)
	if err != nil || len(rows) != 2 || localStart != beforeStart || localEnd != beforeEnd {
		t.Fatalf("non-UTC range changed: rows=%+v err=%v bounds=%v..%v", rows, err, localStart, localEnd)
	}
	for i, row := range rows {
		if !row.OpenTime.Equal(time.Unix(int64(i+1)*86400, 0).UTC()) || row.OpenTime.Location() != time.UTC {
			t.Fatalf("session timezone changed UTC projection: %+v", row)
		}
	}
	migrationResult(t, tx.Commit(), nil)
}

// TestReadCandleRangeTxDeadline проверяет pre-expired и реально заблокированный SQL deadline.
func TestReadCandleRangeTxDeadline(t *testing.T) {
	db, key := candleReadDB(t)
	start, end := time.Unix(0, 0), time.Unix(3*86400, 0)
	tx := candleReadTx(t, db)
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	rows, err := ReadCandleRangeTx(ctx, tx, key, start, end, 2)
	candleReadTxError(t, rows, err, context.DeadlineExceeded)
	var alive int
	migrationResult(t, tx.QueryRowContext(t.Context(), "SELECT 1").Scan(&alive), nil)
	migrationResult(t, tx.Rollback(), nil)
	holder, err := db.BeginTx(t.Context(), nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.ExecContext(t.Context(), "LOCK TABLE momentum.candles IN ACCESS EXCLUSIVE MODE")
	migrationResult(t, err, nil)
	tx = candleReadTx(t, db)
	blocked, stop := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer stop()
	rows, err = ReadCandleRangeTx(blocked, tx, key, start, end, 2)
	candleReadTxError(t, rows, err, context.DeadlineExceeded)
	migrationResult(t, tx.Rollback(), driver.ErrBadConn)
}
