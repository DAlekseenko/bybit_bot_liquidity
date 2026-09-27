package marketdata

import (
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

func TestLiquidationWindowKeepsOnlyRecentEventsAndReturnsCopy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	window := NewLiquidationWindow(5 * time.Minute)
	window.Add(exchange.Liquidation{Symbol: "BTCUSDT", Time: now.Add(-6 * time.Minute), Price: 1})
	window.Add(exchange.Liquidation{Symbol: "BTCUSDT", Time: now.Add(-time.Minute), Price: 3})
	window.Add(exchange.Liquidation{Symbol: "BTCUSDT", Time: now.Add(-5 * time.Minute), Price: 2})

	recent := window.Recent(now)
	if len(recent) != 2 || recent[0].Price != 3 || recent[1].Price != 2 {
		t.Fatalf("Recent() = %#v", recent)
	}

	recent[0].Price = 999
	again := window.Recent(now)
	if again[0].Price != 3 {
		t.Fatalf("internal event changed through returned slice: %#v", again[0])
	}
}

func TestLiquidationWindowResetDropsPartialWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	window := NewLiquidationWindow(5 * time.Minute)
	window.Add(exchange.Liquidation{Time: now, Price: 1})
	window.Reset()
	if recent := window.Recent(now); len(recent) != 0 {
		t.Fatalf("Recent() = %#v, want empty", recent)
	}
}
