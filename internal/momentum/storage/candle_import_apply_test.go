package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// TestApplyCandleImportObservationRouting проверяет классификацию и durability одного перехода.
func TestApplyCandleImportObservationRouting(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, state string
		reason      CandleImportBlockReason
		rows        int
	}{
		{"short-prefix", "progress", "", 1}, {"short-prefix-hourly", "progress", "", 1},
		{"lower-reaching", "complete", "", 3},
		{"leading-gap", "blocked", CandleImportBlockGap, 0}, {"interior-gap", "blocked", CandleImportBlockGap, 0},
		{"empty", "blocked", CandleImportBlockEmpty, 0}, {"early-empty", "blocked", CandleImportBlockUnconfirmed, 0},
		{"epoch", "blocked", CandleImportBlockSourceBoundary, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buckets := 3
			if tc.name == "epoch" {
				buckets = 4
			}
			interval := "D"
			if tc.name == "short-prefix-hourly" {
				interval = "60"
			}
			db, key, prior, page := importAdvanceFixture(t, interval, buckets)
			latest, lower := page.Candles[0].OpenTime, key.LowerOpen
			middle := lower.Add(24 * time.Hour)
			observed, cursor := page.ServerTime, prior.NextEnd
			switch tc.name {
			case "short-prefix", "short-prefix-hourly":
				cursor = latest.Add(-time.Millisecond)
			case "lower-reaching":
				page = importAdvanceOpens(page, latest, middle, lower)
				cursor = lower.Add(-time.Millisecond)
			case "leading-gap":
				page = importAdvanceOpens(page, middle)
			case "interior-gap":
				page = importAdvanceOpens(page, latest, lower)
			case "empty", "early-empty":
				page = importAdvanceOpens(page)
				if tc.name == "early-empty" {
					observed = key.UpperOpen.Add(-137 * time.Millisecond)
					page.ServerTime = observed
				}
			}
			observation := &CandleImportPageObservation{key.Stream.Symbol, key.Stream.IntervalCode, lower, prior.NextEnd, page}
			if tc.name == "epoch" {
				observation, observed = nil, time.Time{}
			}
			observer, err := db.Conn(ctx)
			migrationResult(t, err, nil)
			defer observer.Close()
			tx := candleWriterTx(t, db)
			got, reason, err := ApplyCandleImportObservation(ctx, tx, key, prior, observation)
			migrationResult(t, err, nil)
			want := CandleImportCheckpoint{1, cursor, tc.state}
			if got != want || reason != tc.reason {
				t.Fatalf("candidate=%+v reason=%q, want %+v/%q", got, reason, want, tc.reason)
			}
			importAdvanceDurable(t, observer, prior, 0, time.Time{})
			migrationResult(t, tx.Commit(), nil)
			importAdvanceDurable(t, observer, want, tc.rows, observed)
			job, err := ReadCandleImport(ctx, db, key)
			migrationResult(t, err, nil)
			if job.Reason != string(tc.reason) {
				t.Fatalf("stored reason=%q, want %q", job.Reason, tc.reason)
			}
		})
	}
}

// TestApplyCandleImportObservationInvalid доказывает rollback и нулевой результат для invalid evidence.
func TestApplyCandleImportObservationInvalid(t *testing.T) {
	ctx := context.Background()
	db, key, prior, page := importAdvanceFixture(t, "60", 3)
	for _, tc := range []string{"nil-observation", "nil-list", "wrong-origin", "wrong-endpoint", "wrong-range", "outside", "bad-ohlc", "duplicate", "wrong-symbol"} {
		t.Run(tc, func(t *testing.T) {
			input := &CandleImportPageObservation{key.Stream.Symbol, key.Stream.IntervalCode, key.LowerOpen, prior.NextEnd,
				importAdvanceOpens(page, page.Candles[0].OpenTime)}
			switch tc {
			case "nil-observation":
				input = nil
			case "nil-list":
				input.Page.Candles = nil
			case "wrong-origin":
				input.Page.Environment, input.Page.Endpoint = "mainnet", "https://api.bybit.com/v5/market/kline"
			case "wrong-endpoint":
				input.Page.Endpoint = "https://api.bybit.com/v5/market/kline"
			case "wrong-range":
				input.End = input.End.Add(-time.Millisecond)
			case "outside":
				input.Page.Candles[0].OpenTime = key.UpperOpen
			case "bad-ohlc":
				input.Page.Candles[0].High = input.Page.Candles[0].Low
			case "duplicate":
				input.Page.Candles = append(input.Page.Candles, input.Page.Candles[0])
			case "wrong-symbol":
				input.Symbol = "HYPEUSDT"
			}
			tx := candleWriterTx(t, db)
			got, reason, err := ApplyCandleImportObservation(ctx, tx, key, prior, input)
			importAdvanceFailure(t, tx, got, err, ErrCandleArgument)
			if reason != "" {
				t.Fatalf("invalid evidence returned reason %q", reason)
			}
			importAdvanceDurable(t, db, prior, 0, time.Time{})
		})
	}
}

// TestApplyCandleImportObservationConflict проверяет существующий конфликт и откат префикса.
func TestApplyCandleImportObservationConflict(t *testing.T) {
	ctx := context.Background()
	db, key, prior, page := importAdvanceFixture(t, "60", 3)
	latest := page.Candles[0].OpenTime
	seed := importAdvanceOpens(page, latest.Add(-time.Hour))
	tx := candleWriterTx(t, db)
	migrationResult(t, AppendCandlePage(ctx, tx, key.Stream, seed), nil)
	migrationResult(t, tx.Commit(), nil)
	page = importAdvanceOpens(page, latest, seed.Candles[0].OpenTime)
	closePrice, err := model.ParsePrice("9007199254740995.04")
	migrationResult(t, err, nil)
	page.Candles[1].Close = closePrice
	input := &CandleImportPageObservation{key.Stream.Symbol, key.Stream.IntervalCode, key.LowerOpen, prior.NextEnd, page}
	tx = candleWriterTx(t, db)
	got, reason, err := ApplyCandleImportObservation(ctx, tx, key, prior, input)
	importAdvanceFailure(t, tx, got, err, ErrCandleConflict)
	if reason != "" {
		t.Fatalf("conflict reported blocked %q", reason)
	}
	importAdvanceDurable(t, db, prior, 1, time.Time{})
	rows, err := ReadCandleRange(ctx, db, key.Stream, key.LowerOpen, key.UpperOpen, 3)
	migrationResult(t, err, nil)
	if len(rows) != 1 || rows[0].OpenTime != seed.Candles[0].OpenTime {
		t.Fatalf("immutable row identity changed: %+v", rows)
	}
	cmp, err := rows[0].Close.Cmp(seed.Candles[0].Close)
	if err != nil || cmp != 0 {
		t.Fatalf("immutable close changed: %+v", rows[0].Close)
	}
}

// TestApplyCandleImportObservationTx проверяет typed failure по транзакции и отмене.
func TestApplyCandleImportObservationTx(t *testing.T) {
	db, key, prior, page := importAdvanceFixture(t, "60", 3)
	ctx, cancel := context.WithCancel(context.Background())
	input := &CandleImportPageObservation{key.Stream.Symbol, key.Stream.IntervalCode, key.LowerOpen, prior.NextEnd, page}
	got, reason, err := ApplyCandleImportObservation(ctx, nil, key, prior, input)
	if got != (CandleImportCheckpoint{}) || reason != "" || !errors.Is(err, ErrCandleTx) {
		t.Fatalf("nil tx result: %+v %q %v", got, reason, err)
	}
	tx := candleWriterTx(t, db)
	var nilContext context.Context
	got, reason, err = ApplyCandleImportObservation(nilContext, tx, key, prior, input)
	importAdvanceFailure(t, tx, got, err, ErrCandleArgument)
	if reason != "" {
		t.Fatalf("nil context returned reason %q", reason)
	}
	tx = candleWriterTx(t, db, sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	got, reason, err = ApplyCandleImportObservation(ctx, tx, key, prior, input)
	importAdvanceFailure(t, tx, got, err, ErrCandleTx)
	if reason != "" {
		t.Fatalf("tx failure returned reason %q", reason)
	}
	tx = candleWriterTx(t, db)
	cancel()
	got, reason, err = ApplyCandleImportObservation(ctx, tx, key, prior, input)
	importAdvanceFailure(t, tx, got, err, context.Canceled)
	if reason != "" {
		t.Fatalf("cancellation returned reason %q", reason)
	}
	importAdvanceDurable(t, db, prior, 0, time.Time{})
}
