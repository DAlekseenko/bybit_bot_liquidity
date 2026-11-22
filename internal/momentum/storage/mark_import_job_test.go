package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// markImportFixture создаёт набор данных и окно mark [lower, lower+3 интервала).
func markImportFixture(t *testing.T, interval string, lower time.Time) (*sql.DB, MarkImportWindow) {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(t.Context(), db), nil)
	_, err := db.Exec("INSERT INTO momentum.datasets VALUES ('testnet','mark-import','bybit_v5','linear',DEFAULT)")
	migrationResult(t, err, nil)
	step := 24 * time.Hour
	if interval == "60" {
		step = time.Hour
	}
	key := MarkImportWindow{MarkPriceStream{"testnet", "mark-import", "BTCUSDT", interval}, lower, lower.Add(3 * step)}
	migrationResult(t, InitMarkImport(t.Context(), db, key), nil)
	return db, key
}

// markImportPage строит ответ источника для запроса [lower, end] с указанными open.
func markImportPage(t *testing.T, key MarkImportWindow, end time.Time, close string, opens ...time.Time) *model.MarkPricePage {
	t.Helper()
	page := &model.MarkPricePage{Environment: "testnet", Source: "bybit_v5", Category: "linear", Symbol: key.Stream.Symbol,
		IntervalCode: key.Stream.IntervalCode, Endpoint: "https://api-testnet.bybit.com/v5/market/mark-price-kline",
		StartTime: key.LowerOpen, EndTime: end, ServerTime: key.UpperOpen.Add(time.Minute), Candles: []model.MarkPriceCandle{}}
	for _, open := range opens {
		row := model.MarkPriceCandle{OpenTime: open, Confirmed: true}
		for i, raw := range [...]string{"1", "2", "0.5", close} {
			value, err := model.ParsePrice(raw)
			migrationResult(t, err, nil)
			*[...]*model.Decimal{&row.Open, &row.High, &row.Low, &row.Close}[i] = value
		}
		page.Candles = append(page.Candles, row)
	}
	return page
}

// applyMarkImport применяет наблюдение в отдельной транзакции и фиксирует её при успехе.
func applyMarkImport(t *testing.T, db *sql.DB, key MarkImportWindow, expected CandleImportCheckpoint,
	page *model.MarkPricePage) (CandleImportCheckpoint, CandleImportBlockReason, error) {
	t.Helper()
	tx := candleWriterTx(t, db)
	next, reason, err := ApplyMarkImportObservation(context.Background(), tx, key, expected, page)
	if err == nil {
		migrationResult(t, tx.Commit(), nil)
	}
	return next, reason, err
}

// TestMarkImportLifecycle проверяет init, продвижение, рестарт через read и терминальное состояние.
func TestMarkImportLifecycle(t *testing.T) {
	day := 24 * time.Hour
	lower := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	db, key := markImportFixture(t, "D", lower)
	migrationResult(t, InitMarkImport(t.Context(), db, key), ErrMarkImportNotInserted)
	job, err := ReadMarkImport(t.Context(), db, key)
	migrationResult(t, err, nil)
	pending := CandleImportCheckpoint{0, key.UpperOpen.Add(-time.Millisecond), "pending"}
	if job.Checkpoint != pending || job.Reason != "" || job.LastServerTime != nil {
		t.Fatalf("pending job=%+v", job)
	}
	next, reason, err := applyMarkImport(t, db, key, pending, markImportPage(t, key, pending.NextEnd, "1.5", lower.Add(2*day), lower.Add(day)))
	migrationResult(t, err, nil)
	progress := CandleImportCheckpoint{1, lower.Add(day - time.Millisecond), "progress"}
	if next != progress || reason != "" {
		t.Fatalf("progress=%+v %q", next, reason)
	}
	job, err = ReadMarkImport(t.Context(), db, key)
	migrationResult(t, err, nil)
	_, _, err = applyMarkImport(t, db, key, pending, markImportPage(t, key, pending.NextEnd, "1.5", lower.Add(2*day)))
	migrationResult(t, err, ErrMarkImportStale)
	next, _, err = applyMarkImport(t, db, key, job.Checkpoint, markImportPage(t, key, job.Checkpoint.NextEnd, "1.5", lower))
	migrationResult(t, err, nil)
	if want := (CandleImportCheckpoint{2, lower.Add(-time.Millisecond), "complete"}); next != want {
		t.Fatalf("complete=%+v", next)
	}
	_, _, err = applyMarkImport(t, db, key, next, markImportPage(t, key, next.NextEnd, "1.5"))
	migrationResult(t, err, ErrMarkImportState)
	migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices", "3")
	migrationValue(t, db, "SELECT count(*) FROM momentum.candles", "0")
	migrationValue(t, db, "SELECT count(*) FROM momentum.candle_import_progress", "0")
}

// TestMarkImportBlocked проверяет видимые пробелы, пустой и ранний ответ и отказ при чужом ряде.
func TestMarkImportBlocked(t *testing.T) {
	lower := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, interval string
		reason         CandleImportBlockReason
	}{
		{"daily-gap", "D", CandleImportBlockGap}, {"hourly-gap", "60", CandleImportBlockGap},
		{"empty", "D", CandleImportBlockEmpty}, {"early", "D", CandleImportBlockUnconfirmed},
		{"wrong-symbol", "D", ""}, {"wrong-environment", "D", ""}, {"wrong-request", "D", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, key := markImportFixture(t, tc.interval, lower)
			step := key.UpperOpen.Sub(lower) / 3
			pending := CandleImportCheckpoint{0, key.UpperOpen.Add(-time.Millisecond), "pending"}
			page := markImportPage(t, key, pending.NextEnd, "1.5", lower.Add(2*step), lower)
			switch tc.name {
			case "empty":
				page.Candles = page.Candles[:0]
			case "early":
				page = markImportPage(t, key, pending.NextEnd, "1.5")
				page.ServerTime = key.UpperOpen.Add(-time.Second)
			case "wrong-symbol":
				page.Symbol = "ETHUSDT"
			case "wrong-environment":
				page.Environment, page.Endpoint = "mainnet", "https://api.bybit.com/v5/market/mark-price-kline"
			case "wrong-request":
				page.StartTime = lower.Add(step)
			}
			next, reason, err := applyMarkImport(t, db, key, pending, page)
			if tc.reason == "" {
				migrationResult(t, err, ErrMarkPriceArgument)
				job, err := ReadMarkImport(t.Context(), db, key)
				migrationResult(t, err, nil)
				if job.Checkpoint != pending {
					t.Fatalf("rejected page changed job: %+v", job)
				}
				return
			}
			migrationResult(t, err, nil)
			if reason != tc.reason || next != (CandleImportCheckpoint{1, pending.NextEnd, "blocked"}) {
				t.Fatalf("blocked=%+v %q", next, reason)
			}
			job, err := ReadMarkImport(t.Context(), db, key)
			migrationResult(t, err, nil)
			if job.Reason != string(tc.reason) || job.LastServerTime == nil || !job.LastServerTime.Equal(page.ServerTime) {
				t.Fatalf("stored=%+v", job)
			}
			migrationValue(t, db, "SELECT count(*) FROM momentum.mark_prices", "0")
		})
	}
}

// TestMarkImportReimport проверяет идемпотентный повторный импорт и отказ при расхождении цены.
func TestMarkImportReimport(t *testing.T) {
	day := 24 * time.Hour
	lower := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	db, key := markImportFixture(t, "D", lower)
	pending := CandleImportCheckpoint{0, key.UpperOpen.Add(-time.Millisecond), "pending"}
	opens := []time.Time{lower.Add(2 * day), lower.Add(day), lower}
	_, _, err := applyMarkImport(t, db, key, pending, markImportPage(t, key, pending.NextEnd, "1.5", opens...))
	migrationResult(t, err, nil)
	again := MarkImportWindow{key.Stream, lower.Add(day), key.UpperOpen}
	migrationResult(t, InitMarkImport(t.Context(), db, again), nil)
	page := markImportPage(t, again, pending.NextEnd, "1.5", opens[:2]...)
	next, _, err := applyMarkImport(t, db, again, pending, page)
	migrationResult(t, err, nil)
	if next.State != "complete" {
		t.Fatalf("reimport=%+v", next)
	}
	other := MarkImportWindow{key.Stream, lower.Add(2 * day), key.UpperOpen}
	migrationResult(t, InitMarkImport(t.Context(), db, other), nil)
	_, _, err = applyMarkImport(t, db, other, pending, markImportPage(t, other, pending.NextEnd, "1.25", opens[0]))
	migrationResult(t, err, ErrMarkPriceConflict)
	job, err := ReadMarkImport(t.Context(), db, other)
	migrationResult(t, err, nil)
	if job.Checkpoint != pending {
		t.Fatalf("conflict changed job: %+v", job)
	}
	migrationValue(t, db, "SELECT count(*)||'/'||sum(close) FROM momentum.mark_prices", "3/4.5")
}

// TestMarkImportSourceBoundary проверяет остановку на эпохе без наблюдения и отказ без страницы в иных окнах.
func TestMarkImportSourceBoundary(t *testing.T) {
	db, key := markImportFixture(t, "D", time.UnixMilli(0).UTC())
	pending := CandleImportCheckpoint{0, key.UpperOpen.Add(-time.Millisecond), "pending"}
	next, reason, err := applyMarkImport(t, db, key, pending, nil)
	migrationResult(t, err, nil)
	if reason != CandleImportBlockSourceBoundary || next.State != "blocked" {
		t.Fatalf("boundary=%+v %q", next, reason)
	}
	db, key = markImportFixture(t, "D", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	_, _, err = applyMarkImport(t, db, key, CandleImportCheckpoint{0, key.UpperOpen.Add(-time.Millisecond), "pending"}, nil)
	migrationResult(t, err, ErrMarkPriceArgument)
	_, err = ReadMarkImport(t.Context(), db, MarkImportWindow{key.Stream, key.LowerOpen, key.UpperOpen.Add(24 * time.Hour)})
	migrationResult(t, err, ErrMarkImportMissing)
}
