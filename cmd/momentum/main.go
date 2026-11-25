package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bybit_bot_liquidity/internal/momentum/collect"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/signals"
	"bybit_bot_liquidity/internal/momentum/storage"
)

type success struct {
	BatchHash string `json:"batch_hash"`
	CutoffUTC string `json:"cutoff_utc"`
}

// main запускает публичный сбор или офлайн-бэктест без торговых вызовов.
func main() {
	if err := run(os.Args[1:], os.Stdout, nil); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run запускает выбранную команду, сохраняя существующий путь сбора.
func run(args []string, stdout io.Writer, transport http.RoundTripper) error {
	return dispatch(context.Background(), args, stdout, paperRuntime{now: func() time.Time { return time.Now().UTC() }, wait: waitPaper, transport: transport})
}

// dispatch сохраняет независимые маршруты публичного сбора, backtest и виртуального цикла.
func dispatch(parent context.Context, args []string, stdout io.Writer, rt paperRuntime) error {
	if len(args) > 0 && args[0] == "paper" {
		ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
		defer stop()
		if len(args) > 1 {
			switch args[1] {
			case "status", "pause", "resume", "close-all":
				return runOperator(ctx, args[1:], stdout)
			}
		}
		err := rt.serve(ctx, args[1:])
		if err == context.Canceled && parent.Err() == nil && ctx.Err() != nil {
			return nil
		}
		return err
	}
	if len(args) > 0 && args[0] == "backtest" {
		return runBacktest(parent, args[1:], stdout)
	}
	return runWithJournal(args, stdout, rt.transport, storage.OpenMomentumPostgres)
}

// runWithJournal проверяет аргументы, собирает снимок и только затем открывает журнал.
func runWithJournal(args []string, stdout io.Writer, transport http.RoundTripper, open func(context.Context, string) (*sql.DB, error)) error {
	if len(args) == 0 || args[0] != "collect-once" {
		return errors.New("usage: momentum collect-once -config FILE -timeout DURATION")
	}
	flags := flag.NewFlagSet("collect-once", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "")
	timeout := flags.Duration("timeout", 0, "")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *configPath == "" || *timeout <= 0 {
		return errors.New("invalid collect-once arguments")
	}
	dsn := os.Getenv("MOMENTUM_POSTGRES_DSN")
	if dsn == "" {
		return errors.New("missing PostgreSQL configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cfg, err := config.Load(*configPath)
	if err != nil {
		return errors.New("invalid Momentum configuration")
	}
	batch, cutoff, err := collect.ReadBatch(ctx, cfg, transport)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	db, err := open(ctx, dsn)
	if err != nil {
		return safeJournalError(ctx, "cannot open local signal journal", err)
	}
	defer db.Close()
	if err := storage.MigrateMomentumPostgres(ctx, db); err != nil {
		return safeJournalError(ctx, "cannot migrate local signal journal", err)
	}
	if err := insertAndVerify(ctx, db, batch, storage.InsertSignalBatch, storage.ReadSignalBatch); err != nil {
		return safeJournalError(ctx, "signal batch readback was not verified", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	line, err := json.Marshal(success{batch.Hash(), cutoff.Format(time.RFC3339)})
	if err != nil {
		return errors.New("cannot encode collection result")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, string(line)); err != nil {
		return errors.New("cannot write collection result")
	}
	return nil
}

// insertAndVerify выполняет одну вставку и проверяет exact-key canonical replay.
func insertAndVerify(ctx context.Context, db *sql.DB, batch signals.Batch, insert func(context.Context, *sql.DB, signals.Batch) error, read func(context.Context, *sql.DB, string) (storage.StoredSignalBatch, error)) error {
	insertErr := insert(ctx, db, batch)
	if insertErr != nil && !errors.Is(insertErr, storage.ErrSignalBatchNotInserted) && !errors.Is(insertErr, storage.ErrSignalBatchInsertUnknown) {
		return insertErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stored, err := read(ctx, db, batch.Hash())
	if err != nil {
		return err
	}
	if string(stored.Batch.Payload()) != string(batch.Payload()) {
		return errors.New("stored signal batch differs")
	}
	return ctx.Err()
}

// safeJournalError сохраняет безопасные context sentinels и скрывает детали подключения и записи.
func safeJournalError(ctx context.Context, message string, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New(message)
}
