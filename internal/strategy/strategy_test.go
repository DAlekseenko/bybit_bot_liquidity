package strategy

import (
	"math"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

func TestEvaluateLongSignal(t *testing.T) {
	now := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	candles := baseCandles(20, 100)
	candles = append(candles, exchange.Candle{Open: 100, Close: 105, Volume: 250, Confirmed: true})

	s := New(Config{
		OIGrowthPercent:      3,
		VolumeSMAPeriod:      20,
		VolumeMultiplier:     2,
		LiquidationWindow:    5 * time.Minute,
		LiquidationThreshold: 100000,
	})

	signal := s.Evaluate(Input{
		Symbol:               "BTCUSDT",
		Candles:              candles,
		PreviousOpenInterest: exchange.OpenInterest{Value: 1000},
		CurrentOpenInterest:  exchange.OpenInterest{Value: 1040},
		Liquidations: []exchange.Liquidation{
			{Side: exchange.SideShort, Price: 50000, Qty: 3, Time: now.Add(-time.Minute)},
		},
		Now: now,
	})

	if signal.Type != SignalLong {
		t.Fatalf("expected LONG, got %s: %s", signal.Type, signal.Reason)
	}
}

func TestEvaluateShortSignal(t *testing.T) {
	now := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	candles := baseCandles(20, 100)
	candles = append(candles, exchange.Candle{Open: 105, Close: 100, Volume: 250, Confirmed: true})

	s := New(Config{
		OIGrowthPercent:      3,
		VolumeSMAPeriod:      20,
		VolumeMultiplier:     2,
		LiquidationWindow:    5 * time.Minute,
		LiquidationThreshold: 100000,
	})

	signal := s.Evaluate(Input{
		Symbol:               "BTCUSDT",
		Candles:              candles,
		PreviousOpenInterest: exchange.OpenInterest{Value: 1000},
		CurrentOpenInterest:  exchange.OpenInterest{Value: 1040},
		Liquidations: []exchange.Liquidation{
			{Side: exchange.SideLong, Price: 50000, Qty: 3, Time: now.Add(-time.Minute)},
		},
		Now: now,
	})

	if signal.Type != SignalShort {
		t.Fatalf("expected SHORT, got %s: %s", signal.Type, signal.Reason)
	}
}

func TestEvaluateNoneWhenVolumeIsBelowThreshold(t *testing.T) {
	now := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	candles := baseCandles(20, 100)
	candles = append(candles, exchange.Candle{Open: 100, Close: 105, Volume: 150, Confirmed: true})

	s := New(Config{
		OIGrowthPercent:      3,
		VolumeSMAPeriod:      20,
		VolumeMultiplier:     2,
		LiquidationWindow:    5 * time.Minute,
		LiquidationThreshold: 100000,
	})

	signal := s.Evaluate(Input{
		Symbol:               "BTCUSDT",
		Candles:              candles,
		PreviousOpenInterest: exchange.OpenInterest{Value: 1000},
		CurrentOpenInterest:  exchange.OpenInterest{Value: 1040},
		Liquidations: []exchange.Liquidation{
			{Side: exchange.SideShort, Price: 50000, Qty: 3, Time: now.Add(-time.Minute)},
		},
		Now: now,
	})

	if signal.Type != SignalNone {
		t.Fatalf("expected NONE, got %s", signal.Type)
	}
}

func TestEvaluateDetailedCollectsAllIndicatorsWhenOIFails(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 15, 5, 0, 0, time.UTC)
	previousOIAt := now.Add(-5 * time.Minute)
	candles := baseCandles(20, 100)
	signalCandleAt := now.Add(-time.Minute)
	candles = append(candles, exchange.Candle{
		OpenTime:  signalCandleAt,
		Open:      100,
		Close:     105,
		Volume:    250,
		Confirmed: true,
	})
	s := New(Config{
		OIGrowthPercent:      0.5,
		VolumeSMAPeriod:      20,
		VolumeMultiplier:     2,
		LiquidationWindow:    5 * time.Minute,
		LiquidationThreshold: 100000,
	})

	result := s.EvaluateDetailed(Input{
		Symbol:               "BTCUSDT",
		Candles:              candles,
		PreviousOpenInterest: exchange.OpenInterest{Value: 1000, Time: previousOIAt},
		CurrentOpenInterest:  exchange.OpenInterest{Value: 1004, Time: now},
		Liquidations: []exchange.Liquidation{
			{Side: exchange.SideShort, Price: 50000, Qty: 3, Time: now.Add(-time.Minute)},
			{Side: exchange.SideLong, Price: 50000, Qty: 1, Time: now.Add(-2 * time.Minute)},
		},
		Now: now,
	})

	if result.Signal.Type != SignalNone || result.Signal.Reason != "open interest growth below threshold" {
		t.Fatalf("signal = %#v", result.Signal)
	}
	got := result.Indicators
	if !got.Complete || got.ConfirmedCandles != 21 || got.RequiredCandles != 21 {
		t.Fatalf("candle diagnostics = complete:%t confirmed:%d required:%d", got.Complete, got.ConfirmedCandles, got.RequiredCandles)
	}
	if got.PreviousOpenInterest != 1000 || got.CurrentOpenInterest != 1004 {
		t.Fatalf("OI values = %f/%f", got.PreviousOpenInterest, got.CurrentOpenInterest)
	}
	if math.Abs(got.OIGrowthPercent-0.4) > 1e-12 || got.OIGrowthThresholdPercent != 0.5 || got.OIConditionMet {
		t.Fatalf("OI diagnostics = growth:%f threshold:%f passed:%t", got.OIGrowthPercent, got.OIGrowthThresholdPercent, got.OIConditionMet)
	}
	if got.CurrentVolume != 250 || got.VolumeSMA != 100 || got.VolumeRatio != 2.5 || got.VolumeThreshold != 200 || !got.VolumeConditionMet {
		t.Fatalf("volume diagnostics = %#v", got)
	}
	if got.CandleDirection != CandleBullish || !got.SignalCandleAt.Equal(signalCandleAt) {
		t.Fatalf("candle diagnostics = direction:%s time:%s", got.CandleDirection, got.SignalCandleAt)
	}
	if got.ShortLiquidationsUSD != 150000 || got.LongLiquidationsUSD != 50000 {
		t.Fatalf("liquidation sums = short:%f long:%f", got.ShortLiquidationsUSD, got.LongLiquidationsUSD)
	}
	if !got.ShortLiquidationConditionMet || got.LongLiquidationConditionMet || got.LongSetupMet || got.ShortSetupMet {
		t.Fatalf("condition diagnostics = %#v", got)
	}
	if !got.PreviousOpenInterestAt.Equal(previousOIAt) ||
		!got.CurrentOpenInterestAt.Equal(now) ||
		!got.EvaluatedAt.Equal(now) ||
		!got.LiquidationWindowStart.Equal(now.Add(-5*time.Minute)) {
		t.Fatalf("time diagnostics = %#v", got)
	}
}

func TestEvaluateUsesConfiguredLiquidationWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	candles := baseCandles(20, 100)
	candles = append(candles, exchange.Candle{Open: 100, Close: 105, Volume: 250, Confirmed: true})
	input := Input{
		Symbol:               "BTCUSDT",
		Candles:              candles,
		PreviousOpenInterest: exchange.OpenInterest{Value: 1000},
		CurrentOpenInterest:  exchange.OpenInterest{Value: 1040},
		Liquidations: []exchange.Liquidation{
			{Side: exchange.SideShort, Price: 50000, Qty: 3, Time: now.Add(-3 * time.Minute)},
		},
		Now: now,
	}

	shortWindow := New(Config{
		OIGrowthPercent:      3,
		VolumeSMAPeriod:      20,
		VolumeMultiplier:     2,
		LiquidationWindow:    2 * time.Minute,
		LiquidationThreshold: 100000,
	})
	if signal := shortWindow.Evaluate(input); signal.Type != SignalNone {
		t.Fatalf("short window signal = %s, want NONE", signal.Type)
	}

	longWindow := New(Config{
		OIGrowthPercent:      3,
		VolumeSMAPeriod:      20,
		VolumeMultiplier:     2,
		LiquidationWindow:    4 * time.Minute,
		LiquidationThreshold: 100000,
	})
	if signal := longWindow.Evaluate(input); signal.Type != SignalLong {
		t.Fatalf("long window signal = %s, want LONG", signal.Type)
	}
}

func TestEvaluateUsesConfiguredVolumeSMAPeriod(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	candles := []exchange.Candle{
		{Open: 100, Close: 101, Volume: 1000, Confirmed: true},
		{Open: 100, Close: 101, Volume: 100, Confirmed: true},
		{Open: 100, Close: 101, Volume: 100, Confirmed: true},
		{Open: 100, Close: 105, Volume: 250, Confirmed: true},
	}
	input := Input{
		Symbol:               "BTCUSDT",
		Candles:              candles,
		PreviousOpenInterest: exchange.OpenInterest{Value: 1000},
		CurrentOpenInterest:  exchange.OpenInterest{Value: 1040},
		Liquidations: []exchange.Liquidation{
			{Side: exchange.SideShort, Price: 50000, Qty: 3, Time: now.Add(-time.Minute)},
		},
		Now: now,
	}

	shortPeriod := New(Config{
		OIGrowthPercent:      3,
		VolumeSMAPeriod:      2,
		VolumeMultiplier:     2,
		LiquidationWindow:    5 * time.Minute,
		LiquidationThreshold: 100000,
	})
	if signal := shortPeriod.Evaluate(input); signal.Type != SignalLong {
		t.Fatalf("period 2 signal = %s, want LONG", signal.Type)
	}

	longPeriod := New(Config{
		OIGrowthPercent:      3,
		VolumeSMAPeriod:      3,
		VolumeMultiplier:     2,
		LiquidationWindow:    5 * time.Minute,
		LiquidationThreshold: 100000,
	})
	if signal := longPeriod.Evaluate(input); signal.Type != SignalNone {
		t.Fatalf("period 3 signal = %s, want NONE", signal.Type)
	}
}

func TestEvaluateIgnoresUnconfirmedCandle(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	candles := baseCandles(20, 100)
	candles = append(candles,
		exchange.Candle{Open: 100, Close: 105, Volume: 250, Confirmed: true},
		exchange.Candle{Open: 105, Close: 95, Volume: 10000},
	)
	s := New(Config{
		OIGrowthPercent:      3,
		VolumeSMAPeriod:      20,
		VolumeMultiplier:     2,
		LiquidationWindow:    5 * time.Minute,
		LiquidationThreshold: 100000,
	})

	signal := s.Evaluate(Input{
		Symbol:               "BTCUSDT",
		Candles:              candles,
		PreviousOpenInterest: exchange.OpenInterest{Value: 1000},
		CurrentOpenInterest:  exchange.OpenInterest{Value: 1040},
		Liquidations: []exchange.Liquidation{
			{Side: exchange.SideShort, Price: 50000, Qty: 3, Time: now.Add(-time.Minute)},
		},
		Now: now,
	})

	if signal.Type != SignalLong {
		t.Fatalf("signal = %s, want LONG", signal.Type)
	}
}

func baseCandles(count int, volume float64) []exchange.Candle {
	candles := make([]exchange.Candle, 0, count)
	for range count {
		candles = append(candles, exchange.Candle{Open: 100, Close: 101, Volume: volume, Confirmed: true})
	}
	return candles
}
