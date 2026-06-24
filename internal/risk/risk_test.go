package risk

import (
	"math"
	"testing"

	"bybit_bot_liquidity/internal/exchange"
)

func TestPlanLong(t *testing.T) {
	manager := New(Config{
		RiskPerTradePercent: 1,
		StopLossPercent:     0.8,
		TakeProfitPercent:   1.6,
	})

	plan, err := manager.Plan(10000, 50000, exchange.SideLong)
	if err != nil {
		t.Fatal(err)
	}

	assertClose(t, plan.RiskUSD, 100)
	assertClose(t, plan.Qty, 0.25)
	assertClose(t, plan.StopLoss, 49600)
	assertClose(t, plan.TakeProfit, 50800)
}

func TestPlanShort(t *testing.T) {
	manager := New(Config{
		RiskPerTradePercent: 1,
		StopLossPercent:     0.8,
		TakeProfitPercent:   1.6,
	})

	plan, err := manager.Plan(10000, 50000, exchange.SideShort)
	if err != nil {
		t.Fatal(err)
	}

	assertClose(t, plan.Qty, 0.25)
	assertClose(t, plan.StopLoss, 50400)
	assertClose(t, plan.TakeProfit, 49200)
}

func TestPlanRejectsInvalidInput(t *testing.T) {
	manager := New(Config{
		RiskPerTradePercent: 1,
		StopLossPercent:     0.8,
		TakeProfitPercent:   1.6,
	})

	if _, err := manager.Plan(0, 50000, exchange.SideLong); err == nil {
		t.Fatal("expected error")
	}
}

func assertClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.000001 {
		t.Fatalf("got %f, want %f", got, want)
	}
}
