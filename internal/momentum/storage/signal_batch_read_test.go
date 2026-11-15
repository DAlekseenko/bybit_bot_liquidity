package storage

import (
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/signals"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// readFixture строит канонический пакет отдельно от репозитория записи.
func readFixture(t *testing.T) signals.Batch {
	t.Helper()
	cfg, err := config.Load("../../../configs/momentum.paper.yaml")
	migrationResult(t, err, nil)
	cutoff := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	start := cutoff.AddDate(0, 0, -31)
	price, err := model.ParsePrice("100000000000000001")
	migrationResult(t, err, nil)
	var windows []signals.Window
	for _, symbol := range cfg.Values().Symbols {
		window := signals.Window{Symbol: symbol, Environment: "testnet", Source: "bybit_v5", Category: "linear",
			Endpoint: "https://api-testnet.bybit.com/v5/market/kline", StartMS: start.UnixMilli(),
			EndMS: cutoff.UnixMilli() - 1, ServerTime: cutoff}
		for day := range 31 {
			window.Candles = append(window.Candles, data.DailyClose{Symbol: symbol, OpenTime: start.AddDate(0, 0, day), Close: price, Confirmed: true})
		}
		windows = append(windows, window)
	}
	batch, err := signals.NewBatch(cfg, cutoff, windows)
	migrationResult(t, err, nil)
	return batch
}

// readBatchError проверяет категорию, безопасную диагностику и отсутствие частичного пакета.
func readBatchError(t *testing.T, ctx context.Context, db *sql.DB, hash string, want error) {
	t.Helper()
	got, err := ReadSignalBatch(ctx, db, hash)
	migrationResult(t, err, want)
	if (errors.Is(want, context.Canceled) || errors.Is(want, context.DeadlineExceeded)) && !errors.Is(err, ErrSignalBatchUnavailable) {
		t.Fatal("context error lost unavailable category")
	}
	dsn := os.Getenv("MOMENTUM_TEST_POSTGRES_DSN")
	if (hash != "" && strings.Contains(err.Error(), hash)) || (dsn != "" && strings.Contains(err.Error(), dsn)) || strings.Contains(err.Error(), "sensitive_sql_marker") {
		t.Fatal("read diagnostic exposed key, payload or DSN")
	}
	if got != (StoredSignalBatch{}) {
		t.Fatal("read returned partial data")
	}
}

// TestReadSignalBatch проверяет exact-key, полный replay и время в UTC.
func TestReadSignalBatch(t *testing.T) {
	db := openTestPostgres(t)
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	batch := readFixture(t)
	readBatchError(t, ctx, db, batch.Hash(), ErrSignalBatchUnavailable)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	const rawInsert = `INSERT INTO momentum.signal_batches VALUES (pg_catalog.sha256($1), $1, $2)`
	const serverTime = "1970-01-01 05:30:00.123456+05:30"
	_, err := db.ExecContext(ctx, rawInsert, batch.Payload(), serverTime)
	migrationResult(t, err, nil)
	marketStatement(t, db, "SET TimeZone TO 'Asia/Kolkata'; SET default_transaction_read_only TO on", true)
	got, err := ReadSignalBatch(ctx, db, batch.Hash())
	migrationResult(t, err, nil)
	if got.Batch.Hash() != batch.Hash() || !bytes.Equal(got.Batch.Payload(), batch.Payload()) ||
		got.CreatedAt != time.Unix(0, 123456000).UTC() {
		t.Fatal("stored key, payload or UTC instant differed")
	}
	readBatchError(t, ctx, db, strings.Repeat("a", 64), ErrSignalBatchAbsent)
	marketStatement(t, db, "SET default_transaction_read_only TO off", true)
	for _, payload := range [][]byte{[]byte("sensitive_sql_marker"), append(batch.Payload(), ' '),
		bytes.Replace(batch.Payload(), []byte("roc30_nonpositive"), []byte("roc30_positive"), 1)} {
		sum := sha256.Sum256(payload)
		_, err = db.ExecContext(ctx, rawInsert, payload, serverTime)
		migrationResult(t, err, nil)
		readBatchError(t, ctx, db, hex.EncodeToString(sum[:]), ErrSignalBatchCorrupt)
	}
	marketStatement(t, db, `ALTER TABLE momentum.signal_batches DROP CONSTRAINT signal_batches_check,
 DROP CONSTRAINT signal_batches_created_at_check, ALTER COLUMN created_at DROP NOT NULL`, true)
	filter := " WHERE batch_hash = decode('" + batch.Hash() + "','hex')"
	for _, stamp := range []string{"NULL", "'infinity'", "'-infinity'", "'1969-12-31 23:59:59.999999+00'"} {
		marketStatement(t, db, "UPDATE momentum.signal_batches SET created_at = "+stamp+filter, true)
		readBatchError(t, ctx, db, batch.Hash(), ErrSignalBatchCorrupt)
	}
	const wrongKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	marketStatement(t, db, "UPDATE momentum.signal_batches SET created_at = 'epoch', batch_hash = decode('"+wrongKey+"','hex')"+filter, true)
	readBatchError(t, ctx, db, wrongKey, ErrSignalBatchCorrupt)
}

// TestReadSignalBatchErrors проверяет ошибки ввода, pool и блокирующего SELECT.
func TestReadSignalBatchErrors(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	for _, invalid := range []string{"", hash[1:], strings.ToUpper(hash), strings.Repeat("g", 64), hash + " "} {
		readBatchError(t, ctx, db, invalid, ErrSignalBatchReadArgument)
	}
	readBatchError(t, nil, db, hash, ErrSignalBatchReadArgument)
	readBatchError(t, ctx, nil, hash, ErrSignalBatchReadArgument)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	readBatchError(t, canceled, db, hash, context.Canceled)
	expired, stop := context.WithDeadline(ctx, time.Time{})
	defer stop()
	readBatchError(t, expired, db, hash, context.DeadlineExceeded)
	holder, err := db.BeginTx(ctx, nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	marketStatement(t, holder, "LOCK TABLE momentum.signal_batches IN ACCESS EXCLUSIVE MODE", true)
	var pid int
	migrationResult(t, holder.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid), nil)
	wait, cancelWait := context.WithTimeout(ctx, 3*time.Second)
	defer cancelWait()
	done := make(chan error, 1)
	// SELECT ожидает блокировку таблицы в отдельном соединении.
	go func() {
		_, err := ReadSignalBatch(wait, db, hash)
		done <- err
	}()
	for blocked := false; !blocked; time.Sleep(5 * time.Millisecond) {
		migrationResult(t, db.QueryRowContext(wait, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
 WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid))
 AND query LIKE 'SELECT batch_hash, payload,%')`, pid).Scan(&blocked), nil)
	}
	select {
	case err := <-done:
		migrationResult(t, err, context.DeadlineExceeded)
		migrationResult(t, err, ErrSignalBatchUnavailable)
	case <-time.After(5 * time.Second):
		t.Fatal("SELECT did not return")
	}
	migrationResult(t, holder.Rollback(), nil)
	migrationResult(t, db.Close(), nil)
	readBatchError(t, ctx, db, hash, ErrSignalBatchUnavailable)
}
