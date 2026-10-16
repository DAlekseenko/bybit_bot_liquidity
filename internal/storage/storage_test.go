package storage

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
	"bybit_bot_liquidity/internal/papertrade"
	"bybit_bot_liquidity/internal/strategy"
)

// TestOpenValidatesConfig проверяет обязательные параметры подключения.
func TestOpenValidatesConfig(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		driver string
		dsn    string
	}{
		{name: "missing driver", dsn: ":memory:"},
		{name: "missing dsn", driver: DriverName},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := Open(test.driver, test.dsn)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Open() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

// TestStoreMigrateAndSaveSignal проверяет миграцию и сохранение сигнала.
func TestStoreMigrateAndSaveSignal(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	if err := store.SaveSignal(ctx, strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalLong, Reason: "before repeat migration"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	var preserved int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM signals WHERE reason = 'before repeat migration'`).Scan(&preserved); err != nil || preserved != 1 {
		t.Fatalf("repeated migration preserved rows = %d, %v", preserved, err)
	}

	signal := strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalLong, Reason: "test reason"}
	if err := store.SaveSignal(ctx, signal); err != nil {
		t.Fatalf("SaveSignal() error = %v", err)
	}

	var symbol, signalType, reason, createdAt string
	err := store.db.QueryRowContext(ctx, `SELECT symbol, signal_type, reason, created_at FROM signals WHERE reason = ?`, signal.Reason).Scan(
		&symbol,
		&signalType,
		&reason,
		&createdAt,
	)
	if err != nil {
		t.Fatalf("query signal: %v", err)
	}
	if symbol != signal.Symbol || signalType != string(signal.Type) || reason != signal.Reason {
		t.Fatalf("stored signal = %q/%q/%q, want %q/%q/%q", symbol, signalType, reason, signal.Symbol, signal.Type, signal.Reason)
	}
	if _, err := time.Parse(time.RFC3339Nano, createdAt); err != nil {
		t.Fatalf("created_at = %q: %v", createdAt, err)
	}
}

// TestLatestSignalBySymbol читает последнее состояние, фильтрует символ и сохраняет результат после открытия БД.
func TestLatestSignalBySymbol(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "latest.db")
	store, err := Open(DriverName, path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ symbol, kind, reason string }{{"BTCUSDT", "LONG", "old"}, {"ETHUSDT", "SHORT", "other"}, {"BTCUSDT", "NONE", "latest"}} {
		if err := store.SaveSignal(ctx, strategy.Signal{Symbol: row.symbol, Type: strategy.SignalType(row.kind), Reason: row.reason}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.LatestSignalBySymbol(ctx, "BTCUSDT")
	if err != nil || got == nil || got.Type != "NONE" || got.Reason != "latest" || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("LatestSignalBySymbol() = %#v, %v", got, err)
	}
	if other, err := store.LatestSignalBySymbol(ctx, "XRPUSDT"); err != nil || other != nil {
		t.Fatalf("missing signal = %#v, %v", other, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LatestSignalBySymbol(ctx, "BTCUSDT"); err == nil {
		t.Fatal("LatestSignalBySymbol() succeeded after database close")
	}
	store, err = Open(DriverName, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err = store.LatestSignalBySymbol(ctx, "BTCUSDT")
	if err != nil || got == nil || got.Reason != "latest" {
		t.Fatalf("reopened signal = %#v, %v", got, err)
	}
}

// TestStoreOpenVirtualSignalsFailClosed проверяет пустое, однозначное и неоднозначное состояние.
func TestStoreOpenVirtualSignalsFailClosed(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		rows int
		want int
	}{
		{name: "none"},
		{name: "one", rows: 1, want: 1},
		{name: "multiple", rows: 2, want: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t)
			ctx := context.Background()
			if err := store.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `INSERT INTO signals (symbol, signal_type, reason, created_at, signal_candle_at) VALUES ('BTCUSDT','NONE','indicator-only','2026-01-01','2026-01-01T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `INSERT INTO signals (symbol, signal_type, reason, created_at, signal_candle_at, hypothetical_entry, stop_loss, take_profit, estimated_fees, estimated_slippage, virtual_qty, virtual_closed_at) VALUES ('BTCUSDT','LONG','closed','2026-01-01','2026-01-01T00:00:00Z',100,99,102,0.1,0.2,3,'2026-01-02T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < test.rows; i++ {
				_, err := store.db.ExecContext(ctx, `INSERT INTO signals (symbol, signal_type, reason, created_at, signal_candle_at, hypothetical_entry, stop_loss, take_profit, estimated_fees, estimated_slippage, virtual_qty) VALUES ('BTCUSDT','LONG','paper','2026-01-01','2026-01-01T00:00:00Z',100,99,102,0.1,0.2,3)`)
				if err != nil {
					t.Fatal(err)
				}
			}
			open, err := store.OpenVirtualSignals(ctx)
			if (test.want < 0) != (err != nil) || (err == nil && len(open) != test.want) {
				t.Fatalf("OpenVirtualSignals() = %d rows, %v; want %d", len(open), err, test.want)
			}
			if test.want == 1 {
				item := open[0]
				if item.Symbol != "BTCUSDT" || item.Side != "LONG" || item.Plan.SignalCandleAt.IsZero() || item.Plan.HypotheticalEntry != 100 || item.Plan.StopLoss != 99 || item.Plan.TakeProfit != 102 || item.Plan.EstimatedFees != 0.1 || item.Plan.EstimatedSlippage != 0.2 || item.Plan.Qty != 3 {
					t.Fatalf("OpenVirtualSignals() item = %#v", item)
				}
			}
		})
	}
}

// TestStoreOpenVirtualSignalsRejectsMalformedRows проверяет отказ при NULL и неверном времени.
func TestStoreOpenVirtualSignalsRejectsMalformedRows(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		row  string
	}{
		{name: "null plan value", row: `NULL,'2026-01-01T00:00:00Z',100,99,102,0.2,3`},
		{name: "invalid timestamp", row: `0.1,'invalid',100,99,102,0.2,3`},
		{name: "null entry with other plan evidence", row: `0.1,'2026-01-01T00:00:00Z',NULL,99,102,0.2,3`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t)
			ctx := context.Background()
			if err := store.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			_, err := store.db.ExecContext(ctx, `INSERT INTO signals (symbol, signal_type, reason, created_at, estimated_fees, signal_candle_at, hypothetical_entry, stop_loss, take_profit, estimated_slippage, virtual_qty) VALUES ('BTCUSDT','LONG','paper','2026-01-01',`+test.row+`)`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.OpenVirtualSignals(ctx); err == nil {
				t.Fatal("OpenVirtualSignals() accepted malformed row")
			}
		})
	}
}

// TestPaperEngineRestoreRejectsInvertedLevels проверяет порядок уровней восстановленной позиции.
func TestPaperEngineRestoreRejectsInvertedLevels(t *testing.T) {
	engine, err := papertrade.New(papertrade.Config{VirtualBalanceUSD: 1000, Cooldown: time.Minute, MaxTradesPerDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range []papertrade.Plan{
		{Side: exchange.SideLong, HypotheticalEntry: 100, StopLoss: 101, TakeProfit: 102, Qty: 1, SignalCandleAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Side: exchange.SideShort, HypotheticalEntry: 100, StopLoss: 99, TakeProfit: 98, Qty: 1, SignalCandleAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if err := engine.Restore(1, plan); err == nil {
			t.Fatalf("Restore() accepted inverted %s levels", plan.Side)
		}
	}
}

// TestStoreMigrateRejectsNewerSchema проверяет отказ до миграции неизвестной схемы.
func TestStoreMigrateRejectsNewerSchema(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations VALUES (2, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err == nil {
		t.Fatal("Migrate() accepted schema version 2")
	}
	var tables int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='signals'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("signals table created for unsupported schema: %d", tables)
	}
}

// TestStoreCheck проверяет read-only проверку целостности SQLite.
func TestStoreCheck(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Check(ctx); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
}

// TestStoreSavesAndClosesVirtualSignal проверяет жизненный цикл виртуального сигнала.
func TestStoreSavesAndClosesVirtualSignal(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	signalPrice := 100.0
	signalCandleAt := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	record := SignalRecord{
		Signal:      strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalLong, Reason: "analysis"},
		SignalPrice: &signalPrice,
		Virtual: &VirtualTrade{
			SignalCandleAt:    signalCandleAt,
			HypotheticalEntry: 100.04,
			StopLoss:          99.23968,
			TakeProfit:        101.64064,
			EstimatedFees:     1.15,
			EstimatedSlippage: 0.42,
			Qty:               10.5,
		},
	}
	signalID, err := store.SaveSignalRecord(ctx, record)
	if err != nil {
		t.Fatal(err)
	}

	var gotSignalPrice float64
	var gotEntry float64
	var gotStopLoss float64
	var gotTakeProfit float64
	var gotFees float64
	var gotSlippage float64
	var gotQty float64
	var gotSignalCandleAt string
	err = store.db.QueryRowContext(ctx, `
SELECT signal_price, hypothetical_entry, stop_loss, take_profit,
       estimated_fees, estimated_slippage, virtual_qty, signal_candle_at
FROM signals WHERE id = ?`, signalID).Scan(
		&gotSignalPrice,
		&gotEntry,
		&gotStopLoss,
		&gotTakeProfit,
		&gotFees,
		&gotSlippage,
		&gotQty,
		&gotSignalCandleAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertFloat(t, gotSignalPrice, signalPrice)
	assertFloat(t, gotEntry, record.Virtual.HypotheticalEntry)
	assertFloat(t, gotStopLoss, record.Virtual.StopLoss)
	assertFloat(t, gotTakeProfit, record.Virtual.TakeProfit)
	assertFloat(t, gotFees, record.Virtual.EstimatedFees)
	assertFloat(t, gotSlippage, record.Virtual.EstimatedSlippage)
	assertFloat(t, gotQty, record.Virtual.Qty)
	if gotSignalCandleAt != signalCandleAt.Format(time.RFC3339Nano) {
		t.Fatalf("signal_candle_at = %q", gotSignalCandleAt)
	}

	closedAt := signalCandleAt.Add(2 * time.Minute)
	outcome := VirtualOutcome{
		ExitPrice:  record.Virtual.TakeProfit,
		ExitReason: "TAKE_PROFIT",
		PnL:        15.2,
		ClosedAt:   closedAt,
	}
	if err := store.CloseVirtualSignal(ctx, signalID, outcome); err != nil {
		t.Fatal(err)
	}

	var gotExitPrice float64
	var gotExitReason string
	var gotPnL float64
	var gotClosedAt string
	err = store.db.QueryRowContext(ctx, `
SELECT virtual_exit_price, exit_reason, virtual_pnl, virtual_closed_at
FROM signals WHERE id = ?`, signalID).Scan(
		&gotExitPrice,
		&gotExitReason,
		&gotPnL,
		&gotClosedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertFloat(t, gotExitPrice, outcome.ExitPrice)
	if gotExitReason != outcome.ExitReason {
		t.Fatalf("exit_reason = %q, want %q", gotExitReason, outcome.ExitReason)
	}
	assertFloat(t, gotPnL, outcome.PnL)
	if gotClosedAt != closedAt.Format(time.RFC3339Nano) {
		t.Fatalf("virtual_closed_at = %q", gotClosedAt)
	}

	if err := store.CloseVirtualSignal(ctx, signalID, outcome); !errors.Is(err, ErrVirtualSignalNotOpen) {
		t.Fatalf("second close error = %v, want ErrVirtualSignalNotOpen", err)
	}
}

// TestStoreSavesSignalIndicators проверяет сохранение снимка индикаторов.
func TestStoreSavesSignalIndicators(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	evaluatedAt := time.Date(2026, 7, 28, 15, 5, 0, 0, time.UTC)
	previousOIAt := evaluatedAt.Add(-5 * time.Minute)
	signalCandleAt := evaluatedAt.Add(-time.Minute)
	indicators := strategy.Indicators{
		Complete:                     true,
		ConfirmedCandles:             21,
		RequiredCandles:              21,
		PreviousOpenInterest:         1000,
		CurrentOpenInterest:          1004,
		PreviousOpenInterestAt:       previousOIAt,
		CurrentOpenInterestAt:        evaluatedAt,
		OIGrowthPercent:              0.4,
		OIGrowthThresholdPercent:     0.5,
		OIConditionMet:               false,
		CurrentVolume:                250,
		VolumeSMA:                    100,
		VolumeRatio:                  2.5,
		VolumeRatioAvailable:         true,
		VolumeThreshold:              200,
		VolumeMultiplier:             2,
		VolumeConditionMet:           true,
		CandleOpen:                   100,
		CandleClose:                  105,
		CandleDirection:              strategy.CandleBullish,
		SignalCandleAt:               signalCandleAt,
		ShortLiquidationsUSD:         150000,
		LongLiquidationsUSD:          50000,
		LiquidationThresholdUSD:      100000,
		ShortLiquidationConditionMet: true,
		LongLiquidationConditionMet:  false,
		LongSetupMet:                 false,
		ShortSetupMet:                false,
		EvaluatedAt:                  evaluatedAt,
		LiquidationWindowStart:       previousOIAt,
	}
	signalPrice := 105.0
	signalID, err := store.SaveSignalRecord(ctx, SignalRecord{
		Signal: strategy.Signal{
			Symbol: "BTCUSDT",
			Type:   strategy.SignalNone,
			Reason: "open interest growth below threshold",
		},
		SignalPrice: &signalPrice,
		Indicators:  &indicators,
	})
	if err != nil {
		t.Fatal(err)
	}

	var complete, confirmedCandles, requiredCandles int
	var previousOI, currentOI float64
	var gotPreviousOIAt, gotCurrentOIAt string
	var oiGrowth, oiThreshold float64
	var oiMet int
	var currentVolume, volumeSMA, volumeRatio, volumeThreshold, volumeMultiplier float64
	var volumeMet int
	var candleOpen, candleClose float64
	var candleDirection string
	var shortLiquidations, longLiquidations, liquidationThreshold float64
	var shortLiquidationsMet, longLiquidationsMet, longSetupMet, shortSetupMet int
	var gotEvaluatedAt, gotWindowStart, gotSignalCandleAt string
	err = store.db.QueryRowContext(ctx, `
SELECT
	indicators_complete,
	confirmed_candles,
	required_candles,
	previous_open_interest,
	current_open_interest,
	previous_oi_at,
	current_oi_at,
	oi_growth_percent,
	oi_growth_threshold_percent,
	oi_condition_met,
	current_volume,
	volume_sma,
	volume_ratio,
	volume_threshold,
	volume_multiplier,
	volume_condition_met,
	candle_open,
	candle_close,
	candle_direction,
	short_liquidations_usd,
	long_liquidations_usd,
	liquidation_threshold_usd,
	short_liquidation_condition_met,
	long_liquidation_condition_met,
	long_setup_met,
	short_setup_met,
	evaluated_at,
	liquidation_window_start,
	signal_candle_at
FROM signals
WHERE id = ?`, signalID).Scan(
		&complete,
		&confirmedCandles,
		&requiredCandles,
		&previousOI,
		&currentOI,
		&gotPreviousOIAt,
		&gotCurrentOIAt,
		&oiGrowth,
		&oiThreshold,
		&oiMet,
		&currentVolume,
		&volumeSMA,
		&volumeRatio,
		&volumeThreshold,
		&volumeMultiplier,
		&volumeMet,
		&candleOpen,
		&candleClose,
		&candleDirection,
		&shortLiquidations,
		&longLiquidations,
		&liquidationThreshold,
		&shortLiquidationsMet,
		&longLiquidationsMet,
		&longSetupMet,
		&shortSetupMet,
		&gotEvaluatedAt,
		&gotWindowStart,
		&gotSignalCandleAt,
	)
	if err != nil {
		t.Fatal(err)
	}

	if complete != 1 || confirmedCandles != 21 || requiredCandles != 21 {
		t.Fatalf("candle counts = complete:%d confirmed:%d required:%d", complete, confirmedCandles, requiredCandles)
	}
	assertFloat(t, previousOI, indicators.PreviousOpenInterest)
	assertFloat(t, currentOI, indicators.CurrentOpenInterest)
	assertFloat(t, oiGrowth, indicators.OIGrowthPercent)
	assertFloat(t, oiThreshold, indicators.OIGrowthThresholdPercent)
	if oiMet != 0 || gotPreviousOIAt != previousOIAt.Format(time.RFC3339Nano) || gotCurrentOIAt != evaluatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("OI diagnostics = met:%d previous_at:%s current_at:%s", oiMet, gotPreviousOIAt, gotCurrentOIAt)
	}
	assertFloat(t, currentVolume, indicators.CurrentVolume)
	assertFloat(t, volumeSMA, indicators.VolumeSMA)
	assertFloat(t, volumeRatio, indicators.VolumeRatio)
	assertFloat(t, volumeThreshold, indicators.VolumeThreshold)
	assertFloat(t, volumeMultiplier, indicators.VolumeMultiplier)
	if volumeMet != 1 {
		t.Fatalf("volume_condition_met = %d, want 1", volumeMet)
	}
	assertFloat(t, candleOpen, indicators.CandleOpen)
	assertFloat(t, candleClose, indicators.CandleClose)
	if candleDirection != string(strategy.CandleBullish) {
		t.Fatalf("candle_direction = %q", candleDirection)
	}
	assertFloat(t, shortLiquidations, indicators.ShortLiquidationsUSD)
	assertFloat(t, longLiquidations, indicators.LongLiquidationsUSD)
	assertFloat(t, liquidationThreshold, indicators.LiquidationThresholdUSD)
	if shortLiquidationsMet != 1 || longLiquidationsMet != 0 || longSetupMet != 0 || shortSetupMet != 0 {
		t.Fatalf(
			"setup flags = short_liq:%d long_liq:%d long:%d short:%d",
			shortLiquidationsMet,
			longLiquidationsMet,
			longSetupMet,
			shortSetupMet,
		)
	}
	if gotEvaluatedAt != evaluatedAt.Format(time.RFC3339Nano) ||
		gotWindowStart != previousOIAt.Format(time.RFC3339Nano) ||
		gotSignalCandleAt != signalCandleAt.Format(time.RFC3339Nano) {
		t.Fatalf("times = evaluated:%s window:%s candle:%s", gotEvaluatedAt, gotWindowStart, gotSignalCandleAt)
	}
}

// TestStoreMigrateUpgradesExistingSignalsTable проверяет добавление колонок старой таблице.
func TestStoreMigrateUpgradesExistingSignalsTable(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	ctx := context.Background()
	_, err := store.db.ExecContext(ctx, `
CREATE TABLE signals (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	symbol TEXT NOT NULL,
	signal_type TEXT NOT NULL,
	reason TEXT NOT NULL,
	created_at TEXT NOT NULL
)`)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	signalPrice := 100.0
	if _, err := store.SaveSignalRecord(ctx, SignalRecord{
		Signal:      strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalLong, Reason: "migrated"},
		SignalPrice: &signalPrice,
		Virtual: &VirtualTrade{
			SignalCandleAt:    time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC),
			HypotheticalEntry: 100.04,
			StopLoss:          99,
			TakeProfit:        102,
			EstimatedFees:     1,
			EstimatedSlippage: 0.4,
			Qty:               10,
		},
	}); err != nil {
		t.Fatalf("SaveSignalRecord() after migration error = %v", err)
	}
}

// TestStoreStats проверяет статистику закрытых сделок.
func TestStoreStats(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	closedAt := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	for index, pnl := range []float64{10, -4, 6, -8} {
		trade := Trade{
			Symbol:     "BTCUSDT",
			Side:       "LONG",
			EntryPrice: 100,
			ExitPrice:  101,
			Qty:        1,
			PnL:        pnl,
			OpenedAt:   closedAt.Add(time.Duration(index) * time.Minute),
			ClosedAt:   timePointer(closedAt.Add(time.Duration(index+1) * time.Minute)),
		}
		if err := store.SaveTrade(ctx, trade); err != nil {
			t.Fatalf("SaveTrade(%d) error = %v", index, err)
		}
	}

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.TotalTrades != 4 || stats.WinRate != 50 || stats.TotalPnL != 4 || stats.AverageTrade != 1 || stats.MaxDrawdown != 8 {
		t.Fatalf("Stats() = %#v", stats)
	}
	if math.Abs(stats.ProfitFactor-16.0/12.0) > 1e-12 {
		t.Fatalf("ProfitFactor = %v, want %v", stats.ProfitFactor, 16.0/12.0)
	}
}

// TestStoreVirtualStats разделяет статусы и считает метрики только по завершённым строкам.
func TestStoreVirtualStats(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := store.db.ExecContext(ctx, `
INSERT INTO signals (symbol, signal_type, reason, created_at, hypothetical_entry, virtual_closed_at, virtual_pnl, exit_reason) VALUES
('BTCUSDT','LONG','a','2026-01-01',100,'2026-01-02',10,'TAKE_PROFIT'),
('BTCUSDT','LONG','b','2026-01-01',100,'2026-01-02',-4,'STOP_LOSS'),
('BTCUSDT','LONG','c','2026-01-01',100,'2026-01-03',6,'TAKE_PROFIT'),
('BTCUSDT','NONE','open','2026-01-01',100,NULL,500,NULL),
('BTCUSDT','NONE','skip','2026-01-01',NULL,NULL,NULL,'SKIPPED_LIMIT'),
('BTCUSDT','NONE','skip','2026-01-01',NULL,NULL,NULL,'SKIPPED_COOLDOWN'),
('BTCUSDT','NONE','near miss','2026-01-01',NULL,NULL,NULL,'SKIPPEDXFOO'),
('BTCUSDT','LONG','no entry','2026-01-01',NULL,'2026-01-04',999,'TAKE_PROFIT'),
('BTCUSDT','LONG','no pnl','2026-01-01',100,'2026-01-04',NULL,'TAKE_PROFIT')`)
	if err != nil {
		t.Fatal(err)
	}

	stats, err := store.VirtualStats(ctx)
	if err != nil {
		t.Fatalf("VirtualStats() error = %v", err)
	}
	if stats.ClosedTrades != 3 || stats.OpenTrades != 1 || stats.SkippedSignals != 2 {
		t.Fatalf("VirtualStats() counts = closed:%d open:%d skipped:%d", stats.ClosedTrades, stats.OpenTrades, stats.SkippedSignals)
	}
	assertFloat(t, stats.TotalPnL, 12)
	assertFloat(t, stats.AverageTrade, 4)
	assertFloat(t, stats.WinRate, 200.0/3)
	assertFloat(t, stats.ProfitFactor, 4)
	assertFloat(t, stats.MaxDrawdown, 4)
}

// openTestStore создаёт изолированную временную SQLite-базу для теста.
func openTestStore(t *testing.T) *Store {
	t.Helper()

	store, err := Open(DriverName, filepath.Join(t.TempDir(), "storage.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

// timePointer возвращает указатель на переданное время.
func timePointer(value time.Time) *time.Time {
	return &value
}

// assertFloat сравнивает числа с небольшим допуском округления.
func assertFloat(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %f, want %f", got, want)
	}
}
