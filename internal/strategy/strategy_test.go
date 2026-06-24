package strategy

import (
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

func TestEvaluateLongSignal(t *testing.T) {
	now := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	candles := baseCandles(20, 100)
	candles = append(candles, exchange.Candle{Open: 100, Close: 105, Volume: 250})

	s := New(Config{
		OIGrowthPercent:      3,
		VolumeMultiplier:     2,
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
	candles = append(candles, exchange.Candle{Open: 105, Close: 100, Volume: 250})

	s := New(Config{
		OIGrowthPercent:      3,
		VolumeMultiplier:     2,
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
	candles = append(candles, exchange.Candle{Open: 100, Close: 105, Volume: 150})

	s := New(Config{
		OIGrowthPercent:      3,
		VolumeMultiplier:     2,
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

func baseCandles(count int, volume float64) []exchange.Candle {
	candles := make([]exchange.Candle, 0, count)
	for range count {
		candles = append(candles, exchange.Candle{Open: 100, Close: 101, Volume: volume})
	}
	return candles
}
