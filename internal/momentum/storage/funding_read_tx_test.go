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

// fundingReadTx открывает caller-owned снимок без передачи владения reader.
func fundingReadTx(t *testing.T, db *sql.DB, options ...sql.TxOptions) *sql.Tx {
	t.Helper()
	option := sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	if len(options) > 0 {
		option = options[0]
	}
	tx, err := db.BeginTx(t.Context(), &option)
	migrationResult(t, err, nil)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// fundingReadTxError проверяет ровно ожидаемые категории, nil rows и редактирование диагностик.
func fundingReadTxError(t *testing.T, rows []model.FundingEvent, err, want error) {
	t.Helper()
	if rows != nil || err == nil || !errors.Is(err, ErrFunding) || !errors.Is(err, want) {
		t.Fatalf("rows=%v err=%v want=%v", rows, err, want)
	}
	for _, kind := range []error{ErrFundingArgument, ErrFundingDataset, ErrFundingLimit, ErrFundingTx, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, kind) != (kind == want) {
			t.Fatalf("unexpected class %v: %v", kind, err)
		}
	}
	for _, marker := range []string{"secret-marker", "pq:", "postgres://", "momentum.funding"} {
		if strings.Contains(err.Error(), marker) {
			t.Fatal("unsafe diagnostic")
		}
	}
}

// TestReadFundingRangeTxArguments проверяет pure validation и приоритет отмены без SQL.
func TestReadFundingRangeTxArguments(t *testing.T) {
	key := FundingStream{"testnet", "fixture", "BTCUSDT"}
	start, end := time.UnixMilli(0), time.UnixMilli(9631)
	for _, tc := range []struct {
		ctx        context.Context
		tx         *sql.Tx
		key        FundingStream
		start, end time.Time
		limit      int
	}{
		{nil, nil, key, start, end, 3}, {t.Context(), nil, key, start, end, 3},
		{t.Context(), new(sql.Tx), key, start, end, 0}, {t.Context(), new(sql.Tx), key, start, end, 201},
		{t.Context(), new(sql.Tx), FundingStream{}, start, end, 3},
		{t.Context(), new(sql.Tx), key, end, start, 3}, {t.Context(), new(sql.Tx), key, time.UnixMilli(-1), end, 3},
		{t.Context(), new(sql.Tx), key, start.Add(time.Nanosecond), end, 3},
		{t.Context(), new(sql.Tx), key, start, end.Add(time.Nanosecond), 3},
		{t.Context(), new(sql.Tx), key, start, time.Date(294277, 1, 1, 0, 0, 0, 0, time.UTC), 3},
	} {
		rows, err := ReadFundingRangeTx(tc.ctx, tc.tx, tc.key, tc.start, tc.end, tc.limit)
		fundingReadTxError(t, rows, err, ErrFundingArgument)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, err := ReadFundingRangeTx(ctx, nil, FundingStream{}, time.Time{}, time.Time{}, 0)
	fundingReadTxError(t, rows, err, context.Canceled)
}

// TestReadFundingRangeTxOwnership проверяет mode, inclusive parity, namespace и сохранённую Tx.
func TestReadFundingRangeTxOwnership(t *testing.T) {
	db, key := fundingReadFixture(t)
	start, end := time.UnixMilli(0), time.UnixMilli(9631)
	for _, option := range []sql.TxOptions{{Isolation: sql.LevelReadCommitted, ReadOnly: true}, {Isolation: sql.LevelRepeatableRead}, {Isolation: sql.LevelSerializable, ReadOnly: true}} {
		tx := fundingReadTx(t, db, option)
		rows, err := ReadFundingRangeTx(t.Context(), tx, key, start, end, 3)
		fundingReadTxError(t, rows, err, ErrFundingArgument)
		migrationResult(t, tx.Rollback(), nil)
	}
	tx := fundingReadTx(t, db)
	for _, other := range []FundingStream{key, {"mainnet", "fixture", "BTCUSDT"}, {"testnet", "other", "BTCUSDT"}, {"testnet", "fixture", "ETHUSDT"}, {"testnet", "empty", "BTCUSDT"}} {
		rows, err := ReadFundingRangeTx(t.Context(), tx, other, start.In(time.FixedZone("east", 10800)), end, 3)
		migrationResult(t, err, nil)
		want, err := ReadFundingRange(t.Context(), db, other, start, end, 3)
		if err != nil || !reflect.DeepEqual(rows, want) || rows == nil {
			t.Fatal("existing projection changed")
		}
	}
	one, err := ReadFundingRangeTx(t.Context(), tx, key, start, start, 1)
	if err != nil || len(one) != 1 || one[0].FundingTime != start.UTC() {
		t.Fatal("equal inclusive bounds rejected")
	}
	rate, err := one[0].Rate.WireDecimal()
	if err != nil || rate != "0" {
		t.Fatal("lost explicit zero")
	}
	for _, tc := range []struct {
		key   FundingStream
		limit int
		want  error
	}{{key, 1, ErrFundingLimit}, {key, 0, ErrFundingArgument}, {FundingStream{"testnet", "secret-marker", "BTCUSDT"}, 3, ErrFundingDataset}} {
		rows, err := ReadFundingRangeTx(t.Context(), tx, tc.key, start, end, tc.limit)
		fundingReadTxError(t, rows, err, tc.want)
		var alive int
		migrationResult(t, tx.QueryRow("SELECT 1").Scan(&alive), nil)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, err := ReadFundingRangeTx(ctx, tx, key, start, end, 3)
	fundingReadTxError(t, rows, err, context.Canceled)
	rows, err = ReadFundingRangeTx(t.Context(), tx, key, start, end, 3)
	if err != nil || len(rows) != 3 {
		t.Fatal("reader ended caller transaction")
	}
	migrationResult(t, tx.Commit(), nil)
	rows, err = ReadFundingRangeTx(t.Context(), tx, key, start, end, 3)
	fundingReadTxError(t, rows, err, ErrFunding)
}

// TestReadFundingRangeTxCap проверяет точный opaque ID и overflow200/201 без partial rows.
func TestReadFundingRangeTxCap(t *testing.T) {
	db, _ := fundingReadFixture(t)
	id := strings.Repeat("я", 128)
	migrationResult(t, RegisterDataset(t.Context(), db, "testnet", id), nil)
	_, err := db.Exec(`INSERT INTO momentum.funding SELECT 'testnet',$1,'BTCUSDT','epoch'::timestamptz+n*INTERVAL '1 millisecond',n FROM generate_series(0,200)n`, id)
	migrationResult(t, err, nil)
	tx := fundingReadTx(t, db)
	key := FundingStream{"testnet", id, "BTCUSDT"}
	rows, err := ReadFundingRangeTx(t.Context(), tx, key, time.UnixMilli(0), time.UnixMilli(199), 200)
	if err != nil || len(rows) != 200 || rows[199].FundingTime != time.UnixMilli(199).UTC() {
		t.Fatal("invalid 200 boundary")
	}
	rows, err = ReadFundingRangeTx(t.Context(), tx, key, time.UnixMilli(0), time.UnixMilli(200), 200)
	fundingReadTxError(t, rows, err, ErrFundingLimit)
	migrationResult(t, tx.Commit(), nil)
}

// TestReadFundingRangeTxFailures проверяет parse/provenance/SQL отказы без reader rollback.
func TestReadFundingRangeTxFailures(t *testing.T) {
	db, key := fundingReadFixture(t)
	start, end := time.UnixMilli(0), time.UnixMilli(9631)
	_, err := db.Exec(`UPDATE momentum.funding SET rate=('0.'||repeat('0',64)||'1')::numeric WHERE environment='testnet' AND dataset_id='fixture' AND symbol='BTCUSDT' AND funding_time='1970-01-01 00:00:09.631+00'`)
	migrationResult(t, err, nil)
	tx := fundingReadTx(t, db)
	rows, err := ReadFundingRangeTx(t.Context(), tx, key, start, end, 3)
	fundingReadTxError(t, rows, err, ErrFunding)
	migrationResult(t, tx.Commit(), nil)
	_, err = db.Exec(`ALTER TABLE momentum.datasets DROP CONSTRAINT datasets_source_check;UPDATE momentum.datasets SET source='secret-marker' WHERE environment='testnet' AND dataset_id='fixture'`)
	migrationResult(t, err, nil)
	tx = fundingReadTx(t, db)
	rows, err = ReadFundingRangeTx(t.Context(), tx, key, start, end, 3)
	fundingReadTxError(t, rows, err, ErrFundingDataset)
	migrationResult(t, tx.Rollback(), nil)
	tx = fundingReadTx(t, openTestPostgres(t))
	rows, err = ReadFundingRangeTx(t.Context(), tx, key, start, end, 3)
	fundingReadTxError(t, rows, err, ErrFunding)
	migrationResult(t, tx.Rollback(), nil)
}

// TestReadFundingRangeTxSnapshot сохраняет два irregular ряда после атомарного late insert.
func TestReadFundingRangeTxSnapshot(t *testing.T) {
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(t.Context(), db), nil)
	migrationResult(t, RegisterDataset(t.Context(), db, "testnet", "shared"), nil)
	_, err := db.Exec(`INSERT INTO momentum.funding SELECT 'testnet','shared',s,
 'epoch'::timestamptz+((n*n+n)*1234+7)*INTERVAL '1 millisecond',
 CASE WHEN n%3=0 THEN 0 ELSE -9007199254740993.000000000000000001 END
 FROM (VALUES('BTCUSDT'),('ETHUSDT'))symbols(s) CROSS JOIN generate_series(0,134)n WHERE n<>57`)
	migrationResult(t, err, nil)
	start, end := time.UnixMilli(7), time.UnixMilli((134*135)*1234+7)
	key := FundingStream{"testnet", "shared", "BTCUSDT"}
	tx := fundingReadTx(t, db)
	before, err := ReadFundingRangeTx(t.Context(), tx, key, start, end, 200)
	if err != nil || len(before) != 134 {
		t.Fatal("bad irregular fixture")
	}
	late := time.UnixMilli((57*58)*1234 + 7)
	_, err = db.Exec(`INSERT INTO momentum.funding SELECT 'testnet','shared',s,$1,0 FROM (VALUES('BTCUSDT'),('ETHUSDT'))symbols(s)`, late)
	migrationResult(t, err, nil)
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		key.Symbol = symbol
		rows, err := ReadFundingRangeTx(t.Context(), tx, key, start, end, 200)
		if err != nil || !reflect.DeepEqual(rows, before) || rows[133].FundingTime != end.UTC() {
			t.Fatal("shared snapshot changed")
		}
		gap, err := ReadFundingRangeTx(t.Context(), tx, key, late, late, 1)
		if err != nil || gap == nil || len(gap) != 0 {
			t.Fatal("late event leaked into old snapshot")
		}
		current, err := ReadFundingRange(t.Context(), db, key, late, late, 1)
		if err != nil || len(current) != 1 {
			t.Fatal("fresh snapshot missed committed zero event")
		}
		value, err := rows[1].Rate.WireDecimal()
		if err != nil || value != "-9007199254740993.000000000000000001" {
			t.Fatal("rate rounded")
		}
	}
	migrationResult(t, tx.Commit(), nil)
}

// TestReadFundingRangeTxDeadline сохраняет context error реального ожидающего SQL.
func TestReadFundingRangeTxDeadline(t *testing.T) {
	db, key := fundingReadFixture(t)
	holder, err := db.BeginTx(t.Context(), nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.Exec("LOCK TABLE momentum.funding IN ACCESS EXCLUSIVE MODE")
	migrationResult(t, err, nil)
	tx := fundingReadTx(t, db)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	rows, err := ReadFundingRangeTx(ctx, tx, key, time.UnixMilli(0), time.UnixMilli(0), 1)
	fundingReadTxError(t, rows, err, context.DeadlineExceeded)
	_ = tx.Rollback()
	migrationResult(t, holder.Rollback(), nil)
	tx = fundingReadTx(t, db)
	rows, err = ReadFundingRangeTx(t.Context(), tx, key, time.UnixMilli(0), time.UnixMilli(0), 1)
	if err != nil || len(rows) != 1 {
		t.Fatal("retry after caller release failed")
	}
	migrationResult(t, tx.Commit(), nil)
}

// fundingTxFailedDriver моделирует отказ mode-query без SQL сервера и повторной попытки.
type fundingTxFailedDriver struct{}

// Connect предоставляет независимый fake connection.
func (fundingTxFailedDriver) Connect(context.Context) (driver.Conn, error) {
	return fundingTxFailedDriver{}, nil
}

// Driver возвращает тот же stateless fake transport.
func (d fundingTxFailedDriver) Driver() driver.Driver { return d }

// Open предоставляет transport через стандартный Driver API.
func (fundingTxFailedDriver) Open(string) (driver.Conn, error) { return fundingTxFailedDriver{}, nil }

// Prepare возвращает необработанную диагностическую строку только внутрь reader.
func (fundingTxFailedDriver) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("secret-marker")
}

// Close завершает fake connection без внешних действий.
func (fundingTxFailedDriver) Close() error { return nil }

// Begin отдаёт fake transaction вызывающей стороне.
func (fundingTxFailedDriver) Begin() (driver.Tx, error) { return fundingTxFailedDriver{}, nil }

// Commit завершает fake transaction без внешних действий.
func (fundingTxFailedDriver) Commit() error { return nil }

// Rollback подтверждает независимое завершение транзакции caller.
func (fundingTxFailedDriver) Rollback() error { return nil }

// TestReadFundingRangeTxDriverFailure проверяет редактирование mode-query error без владения Tx.
func TestReadFundingRangeTxDriverFailure(t *testing.T) {
	db := sql.OpenDB(fundingTxFailedDriver{})
	defer db.Close()
	tx, err := db.BeginTx(t.Context(), nil)
	migrationResult(t, err, nil)
	rows, err := ReadFundingRangeTx(t.Context(), tx, FundingStream{"testnet", "fixture", "BTCUSDT"}, time.UnixMilli(0), time.UnixMilli(0), 1)
	fundingReadTxError(t, rows, err, ErrFunding)
	migrationResult(t, tx.Rollback(), nil)
}
