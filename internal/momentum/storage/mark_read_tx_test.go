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

	"bybit_bot_liquidity/internal/momentum/model"
)

// markReadTx открывает общий снимок, завершением которого управляет caller.
func markReadTx(t *testing.T, db *sql.DB, options ...sql.TxOptions) *sql.Tx {
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

// markReadTxError проверяет точные safe sentinel без частичного результата.
func markReadTxError(t *testing.T, rows []model.MarkPriceCandle, err, want error) {
	t.Helper()
	if rows != nil || err == nil || !errors.Is(err, ErrMarkPriceRead) || !errors.Is(err, want) {
		t.Fatalf("rows=%+v error=%v want=%v", rows, err, want)
	}
	for _, kind := range []error{ErrMarkPriceReadArgument, ErrMarkPriceReadDataset, ErrMarkPriceReadLimit, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, kind) != (want == kind) {
			t.Fatalf("unexpected class %v: %v", kind, err)
		}
	}
	for _, forbidden := range []string{"mark-read-secret", "pq:", "momentum.mark_prices", "postgres://"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("unsafe error: %v", err)
		}
	}
}

// TestReadMarkPriceRangeTxArguments проверяет nil, precision, bounds, key и лимит до SQL.
func TestReadMarkPriceRangeTxArguments(t *testing.T) {
	key := CandleStream{"testnet", "fixture", "BTCUSDT", "D"}
	start, end := time.Unix(0, 0), time.UnixMilli(1)
	for _, tc := range []struct {
		ctx        context.Context
		tx         *sql.Tx
		key        CandleStream
		start, end time.Time
		limit      int
	}{
		{nil, nil, key, start, end, 1}, {t.Context(), nil, key, start, end, 1},
		{t.Context(), new(sql.Tx), key, start, end, 0}, {t.Context(), new(sql.Tx), key, start, end, 201},
		{t.Context(), new(sql.Tx), CandleStream{}, start, end, 1},
		{t.Context(), new(sql.Tx), key, start, start, 1}, {t.Context(), new(sql.Tx), key, end, start, 1},
		{t.Context(), new(sql.Tx), key, start.Add(time.Nanosecond), end, 1},
	} {
		rows, err := ReadMarkPriceRangeTx(tc.ctx, tc.tx, tc.key, tc.start, tc.end, tc.limit)
		markReadTxError(t, rows, err, ErrMarkPriceReadArgument)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, err := ReadMarkPriceRangeTx(ctx, nil, key, start, end, 1)
	markReadTxError(t, rows, err, context.Canceled)
}

// TestReadMarkPriceRangeTxOwnership проверяет mode, D/60, off-grid bounds и caller lifecycle.
func TestReadMarkPriceRangeTxOwnership(t *testing.T) {
	db, key := markReadFixture(t)
	start, end := time.Unix(0, 0), time.Unix(4*86400, 0)
	for _, option := range []sql.TxOptions{
		{Isolation: sql.LevelReadCommitted, ReadOnly: true}, {Isolation: sql.LevelRepeatableRead},
		{Isolation: sql.LevelSerializable, ReadOnly: true},
	} {
		tx := markReadTx(t, db, option)
		rows, err := ReadMarkPriceRangeTx(t.Context(), tx, key, start, end, 3)
		markReadTxError(t, rows, err, ErrMarkPriceReadArgument)
		migrationResult(t, tx.Rollback(), nil)
	}
	tx := markReadTx(t, db)
	for _, tc := range []struct {
		key   CandleStream
		limit int
		want  error
	}{
		{key, 2, ErrMarkPriceReadLimit}, {key, 0, ErrMarkPriceReadArgument},
		{CandleStream{"testnet", "mark-read-secret", "BTCUSDT", "D"}, 3, ErrMarkPriceReadDataset},
	} {
		rows, err := ReadMarkPriceRangeTx(t.Context(), tx, tc.key, start, end, tc.limit)
		markReadTxError(t, rows, err, tc.want)
		var alive int
		migrationResult(t, tx.QueryRowContext(t.Context(), "SELECT 1").Scan(&alive), nil)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, err := ReadMarkPriceRangeTx(ctx, tx, key, start, end, 3)
	markReadTxError(t, rows, err, context.Canceled)
	for _, interval := range []string{"D", "60"} {
		key.IntervalCode = interval
		rows, err = ReadMarkPriceRangeTx(t.Context(), tx, key, start, end, 3)
		migrationResult(t, err, nil)
		want, err := ReadMarkPriceRange(t.Context(), db, key, start, end, 3)
		if err != nil || !reflect.DeepEqual(rows, want) {
			t.Fatalf("projection changed: %+v %+v %v", rows, want, err)
		}
	}
	key.IntervalCode = "D"
	rows, err = ReadMarkPriceRangeTx(t.Context(), tx, key, start, start.Add(time.Millisecond), 1)
	if err != nil || len(rows) != 1 || rows[0].OpenTime != start.UTC() {
		t.Fatalf("off-grid bounds: %+v %v", rows, err)
	}
	key.DatasetID = "empty"
	rows, err = ReadMarkPriceRangeTx(t.Context(), tx, key, start.Add(time.Millisecond), end, 1)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty mark-only scope: %+v %v", rows, err)
	}
	migrationResult(t, tx.Commit(), nil)
	rows, err = ReadMarkPriceRangeTx(t.Context(), tx, key, start, end, 3)
	markReadTxError(t, rows, err, ErrMarkPriceRead)
}

// TestReadMarkPriceRangeTxFailures проверяет malformed row, provenance и SQL failure без rollback.
func TestReadMarkPriceRangeTxFailures(t *testing.T) {
	db, key := markReadFixture(t)
	start, end := time.Unix(0, 0), time.Unix(4*86400, 0)
	marketStatement(t, db, "UPDATE momentum.mark_prices SET high=9."+strings.Repeat("0", 64)+"1 WHERE environment='testnet' AND dataset_id='fixture' AND open_time='1970-01-03+00'", true)
	tx := markReadTx(t, db)
	rows, err := ReadMarkPriceRangeTx(t.Context(), tx, key, start, end, 3)
	markReadTxError(t, rows, err, ErrMarkPriceRead)
	migrationResult(t, tx.Rollback(), nil)
	marketStatement(t, db, "ALTER TABLE momentum.datasets DROP CONSTRAINT datasets_source_check; UPDATE momentum.datasets SET source='mark-read-secret' WHERE environment='testnet' AND dataset_id='fixture'", true)
	tx = markReadTx(t, db)
	rows, err = ReadMarkPriceRangeTx(t.Context(), tx, key, start, end, 3)
	markReadTxError(t, rows, err, ErrMarkPriceReadDataset)
	migrationResult(t, tx.Rollback(), nil)
	tx = markReadTx(t, openTestPostgres(t))
	rows, err = ReadMarkPriceRangeTx(t.Context(), tx, key, start, end, 3)
	markReadTxError(t, rows, err, ErrMarkPriceRead)
	migrationResult(t, tx.Rollback(), nil)
}

// TestReadMarkPriceRangeTxSnapshot проверяет два mark ряда по1080h и late inserts в общем снимке.
func TestReadMarkPriceRangeTxSnapshot(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(t.Context(), db), nil)
	migrationResult(t, RegisterDataset(t.Context(), db, "testnet", "snapshot"), nil)
	start := time.Unix(86400, 0).UTC()
	end := start.Add(1080 * time.Hour)
	_, err := db.ExecContext(t.Context(), `INSERT INTO momentum.mark_prices SELECT 'testnet','snapshot',s,'60',
 $1::timestamptz+n*INTERVAL '1 hour',9007199254740993.00000000000000000001,
 9007199254740993.00000000000000000001,9007199254740993.00000000000000000001,
 9007199254740993.00000000000000000001,n<>300
 FROM (VALUES('BTCUSDT'),('ETHUSDT')) symbols(s) CROSS JOIN generate_series(0,1079) n WHERE n<>500`, start)
	migrationResult(t, err, nil)
	tx := markReadTx(t, db)
	key := CandleStream{"testnet", "snapshot", "BTCUSDT", "60"}
	rows, err := ReadMarkPriceRangeTx(t.Context(), tx, key, start, end, 200)
	markReadTxError(t, rows, err, ErrMarkPriceReadLimit)
	_, err = db.ExecContext(t.Context(), `INSERT INTO momentum.mark_prices SELECT 'testnet','snapshot',s,'60',$1,
 9007199254740993.00000000000000000001,9007199254740993.00000000000000000001,
 9007199254740993.00000000000000000001,9007199254740993.00000000000000000001,true
 FROM (VALUES('BTCUSDT'),('ETHUSDT')) symbols(s)`, start.Add(500*time.Hour))
	migrationResult(t, err, nil)
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		key.Symbol = symbol
		var materialized []model.MarkPriceCandle
		for lower := start; lower.Before(end); lower = lower.Add(200 * time.Hour) {
			upper := lower.Add(200 * time.Hour)
			if upper.After(end) {
				upper = end
			}
			rows, err := ReadMarkPriceRangeTx(t.Context(), tx, key, lower, upper, 200)
			migrationResult(t, err, nil)
			materialized = append(materialized, rows...)
		}
		if len(materialized) != 1079 || materialized[300].Confirmed || materialized[500].OpenTime != start.Add(501*time.Hour) {
			t.Fatalf("snapshot/confirmed/gap changed for %s: %d", symbol, len(materialized))
		}
		value, err := materialized[0].Close.WireDecimal()
		if err != nil || value != "9007199254740993.00000000000000000001" {
			t.Fatalf("exact NUMERIC changed: %q %v", value, err)
		}
		fresh, err := ReadMarkPriceRange(t.Context(), db, key, start.Add(500*time.Hour), start.Add(501*time.Hour), 1)
		if err != nil || len(fresh) != 1 {
			t.Fatalf("fresh snapshot must see insert: %+v %v", fresh, err)
		}
	}
	migrationResult(t, tx.Commit(), nil)
}

// TestReadMarkPriceRangeTxRegistryAndTimezone проверяет оба повреждения registry и UTC projection.
func TestReadMarkPriceRangeTxRegistryAndTimezone(t *testing.T) {
	db, key := markReadFixture(t)
	start, end := time.Unix(0, 0), time.Unix(4*86400, 0)
	marketStatement(t, db, "ALTER TABLE momentum.datasets DROP CONSTRAINT datasets_source_check, DROP CONSTRAINT datasets_category_check", true)
	for _, tc := range []struct{ source, category string }{{"mark-read-secret", "linear"}, {"bybit_v5", "spot"}} {
		_, err := db.Exec("UPDATE momentum.datasets SET source=$1,category=$2 WHERE environment=$3 AND dataset_id=$4", tc.source, tc.category, key.Environment, key.DatasetID)
		migrationResult(t, err, nil)
		tx := markReadTx(t, db)
		rows, err := ReadMarkPriceRangeTx(t.Context(), tx, key, start, end, 3)
		markReadTxError(t, rows, err, ErrMarkPriceReadDataset)
		var alive int
		migrationResult(t, tx.QueryRowContext(t.Context(), "SELECT 1").Scan(&alive), nil)
		migrationResult(t, tx.Rollback(), nil)
	}
	marketStatement(t, db, "UPDATE momentum.datasets SET source='bybit_v5',category='linear' WHERE environment='testnet' AND dataset_id='fixture'", true)
	tx := markReadTx(t, db)
	_, err := tx.ExecContext(t.Context(), "SET LOCAL TIME ZONE 'Pacific/Honolulu'")
	migrationResult(t, err, nil)
	zone := time.FixedZone("east", 3*3600)
	lo, hi := start.In(zone), end.Add(-time.Millisecond).In(zone)
	beforeLo, beforeHi := lo, hi
	rows, err := ReadMarkPriceRangeTx(t.Context(), tx, key, lo, hi, 3)
	if err != nil || len(rows) != 3 || lo != beforeLo || hi != beforeHi {
		t.Fatalf("offset bounds changed: %+v %v", rows, err)
	}
	for i, day := range []int64{0, 2, 3} {
		if rows[i].OpenTime != time.Unix(day*86400, 0).UTC() {
			t.Fatal("session changed UTC instant", rows[i].OpenTime)
		}
	}
	migrationResult(t, tx.Commit(), nil)
}

// TestReadMarkPriceRangeTxDeadline наблюдает SQL lock и точный deadline без завершения reader.
func TestReadMarkPriceRangeTxDeadline(t *testing.T) {
	db, key := markReadFixture(t)
	start, end := time.Unix(0, 0), time.Unix(4*86400, 0)
	tx := markReadTx(t, db)
	expired, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	rows, err := ReadMarkPriceRangeTx(expired, tx, key, start, end, 3)
	markReadTxError(t, rows, err, context.DeadlineExceeded)
	var alive int
	migrationResult(t, tx.QueryRowContext(t.Context(), "SELECT 1").Scan(&alive), nil)
	migrationResult(t, tx.Rollback(), nil)
	holder, err := db.BeginTx(t.Context(), nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.ExecContext(t.Context(), "LOCK TABLE momentum.mark_prices IN ACCESS EXCLUSIVE MODE")
	migrationResult(t, err, nil)
	tx = markReadTx(t, db)
	var pid int
	migrationResult(t, tx.QueryRowContext(t.Context(), "SELECT pg_backend_pid()").Scan(&pid), nil)
	blocked, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		rows, err := ReadMarkPriceRangeTx(blocked, tx, key, start, end, 3)
		if rows != nil {
			finished <- errors.New("partial blocked read")
			return
		}
		finished <- err
	}()
	for {
		var waiting bool
		migrationResult(t, db.QueryRowContext(blocked, "SELECT cardinality(pg_blocking_pids($1::int))>0", pid).Scan(&waiting), nil)
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	markReadTxError(t, nil, <-finished, context.DeadlineExceeded)
	migrationResult(t, tx.Rollback(), driver.ErrBadConn)
}
