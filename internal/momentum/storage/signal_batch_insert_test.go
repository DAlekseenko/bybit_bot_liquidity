package storage

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/signals"
)

// insertFixture собирает валидный пакет через публичный конструктор.
func insertFixture(t *testing.T, delta time.Duration) signals.Batch {
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
			EndMS: cutoff.UnixMilli() - 1, ServerTime: cutoff.Add(delta)}
		for day := range 31 {
			window.Candles = append(window.Candles, data.DailyClose{Symbol: symbol, OpenTime: start.AddDate(0, 0, day), Close: price, Confirmed: true})
		}
		windows = append(windows, window)
	}
	batch, err := signals.NewBatch(cfg, cutoff, windows)
	migrationResult(t, err, nil)
	return batch
}

// TestInsertSignalBatch проверяет точные байты, неподвижный повтор и локальные ошибки.
func TestInsertSignalBatch(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	batch := insertFixture(t, 0)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	expired, stop := context.WithDeadline(ctx, time.Time{})
	defer stop()
	migrationResult(t, InsertSignalBatch(nil, db, batch), ErrSignalBatchInsertArgument) //nolint:staticcheck // проверяется обязательное отклонение nil context до SQL.
	migrationResult(t, InsertSignalBatch(ctx, nil, batch), ErrSignalBatchInsertArgument)
	migrationResult(t, InsertSignalBatch(ctx, db, signals.Batch{}), ErrSignalBatchInsertArgument)
	if got := InsertSignalBatch(canceled, db, batch); got != context.Canceled {
		t.Fatal("canceled context was not rejected before connection acquisition")
	}
	if got := InsertSignalBatch(expired, db, batch); got != context.DeadlineExceeded {
		t.Fatal("expired context was not rejected before connection acquisition")
	}
	migrationResult(t, InsertSignalBatch(ctx, db, batch), ErrSignalBatchInsertUnknown)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	migrationResult(t, InsertSignalBatch(ctx, db, batch), nil)
	other := insertFixture(t, time.Millisecond)
	migrationResult(t, InsertSignalBatch(ctx, db, other), nil)
	keyed := " WHERE batch_hash = decode('" + batch.Hash() + "','hex')"
	migrationValue(t, db, "SELECT encode(batch_hash,'hex') || ':' || encode(payload,'hex') FROM momentum.signal_batches"+keyed, batch.Hash()+":"+hex.EncodeToString(batch.Payload()))
	const snapshot = "SELECT row_to_json(r)::text FROM (SELECT *, xmin::text, ctid::text FROM momentum.signal_batches) r"
	before := migrationValue(t, db, snapshot+keyed)
	migrationResult(t, InsertSignalBatch(ctx, db, batch), ErrSignalBatchNotInserted)
	migrationValue(t, db, snapshot+keyed, before)
	migrationResult(t, db.Close(), nil)
	migrationResult(t, InsertSignalBatch(ctx, db, batch), ErrSignalBatchInsertUnavailable)
}

// TestInsertSignalBatchCommitFailure проверяет ошибки statement и неявного commit с безопасной диагностикой.
func TestInsertSignalBatchCommitFailure(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	marketStatement(t, db, `CREATE FUNCTION public.reject_signal() RETURNS trigger LANGUAGE plpgsql
 AS $$ BEGIN RAISE EXCEPTION 'sensitive_sql_marker'; END; $$;
 CREATE CONSTRAINT TRIGGER reject_signal AFTER INSERT ON momentum.signal_batches
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.reject_signal()`, true)
	migrationResult(t, InsertSignalBatch(ctx, db, insertFixture(t, 0)), ErrSignalBatchInsertUnknown)
	marketStatement(t, db, "CREATE TRIGGER reject_immediately AFTER INSERT ON momentum.signal_batches FOR EACH ROW EXECUTE FUNCTION public.reject_signal()", true)
	migrationResult(t, InsertSignalBatch(ctx, db, insertFixture(t, 0)), ErrSignalBatchInsertUnknown)
	migrationValue(t, db, "SELECT count(*) FROM momentum.signal_batches", "0")
}

// TestInsertSignalBatchConcurrent проверяет конкуренцию, откат победителя и отмену ожидающей вставки.
func TestInsertSignalBatchConcurrent(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	for i, tc := range []struct {
		action string
		err    error
		count  string
	}{
		{"commit", ErrSignalBatchNotInserted, "1"},
		{"rollback", nil, "2"},
		{"cancel", context.Canceled, "2"},
		{"deadline", context.DeadlineExceeded, "2"},
	} {
		batch := insertFixture(t, time.Duration(i)*time.Millisecond)
		key, err := hex.DecodeString(batch.Hash())
		migrationResult(t, err, nil)
		holder, err := db.BeginTx(ctx, nil)
		migrationResult(t, err, nil)
		defer holder.Rollback()
		_, err = holder.ExecContext(ctx, insertSignalBatchSQL, key, batch.Payload())
		migrationResult(t, err, nil)
		var pid int
		migrationResult(t, holder.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid), nil)
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		done := make(chan error, 1)
		// Вторая сессия ждёт коммит или откат строки, удерживаемой holder.
		go func() { done <- InsertSignalBatch(call, db, batch) }()
		for {
			var waiting bool
			migrationResult(t, db.QueryRowContext(call, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
 WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid))
 AND query LIKE 'INSERT INTO momentum.signal_batches%')`, pid).Scan(&waiting), nil)
			if waiting {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		switch tc.action {
		case "commit":
			migrationResult(t, holder.Commit(), nil)
		case "rollback":
			migrationResult(t, holder.Rollback(), nil)
		case "cancel":
			cancel()
		}
		select {
		case got := <-done:
			migrationResult(t, got, tc.err)
			if tc.action == "cancel" || tc.action == "deadline" {
				migrationResult(t, got, ErrSignalBatchInsertUnknown)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("INSERT did not return")
		}
		if tc.action == "cancel" || tc.action == "deadline" {
			migrationResult(t, holder.Rollback(), nil)
		}
		migrationValue(t, db, "SELECT count(*) FROM momentum.signal_batches", tc.count)
	}
}
