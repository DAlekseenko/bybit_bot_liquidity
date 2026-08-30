package risk

import (
	"errors"
	"math"
	"testing"

	"bybit_bot_liquidity/internal/exchange"
)

func TestPlanLong(t *testing.T) {
	manager := New(Config{
		RiskPerTradePercent:          1,
		StopLossPercent:              0.8,
		TakeProfitPercent:            1.6,
		EstimatedRoundTripFeePercent: 0.11,
		SlippageAllowancePercent:     0.04,
	})

	plan, err := manager.Plan(10000, 50000, exchange.SideLong)
	if err != nil {
		t.Fatal(err)
	}

	assertClose(t, plan.RiskUSD, 100)
	assertClose(t, plan.Qty, 100.0/475.0)
	assertClose(t, plan.StopLoss, 49600)
	assertClose(t, plan.TakeProfit, 50800)
}

func TestPlanShort(t *testing.T) {
	manager := New(Config{
		RiskPerTradePercent:          1,
		StopLossPercent:              0.8,
		TakeProfitPercent:            1.6,
		EstimatedRoundTripFeePercent: 0.11,
		SlippageAllowancePercent:     0.04,
	})

	plan, err := manager.Plan(10000, 50000, exchange.SideShort)
	if err != nil {
		t.Fatal(err)
	}

	assertClose(t, plan.Qty, 100.0/475.0)
	assertClose(t, plan.StopLoss, 50400)
	assertClose(t, plan.TakeProfit, 49200)
}

func TestPlanRejectsInvalidInput(t *testing.T) {
	validConfig := Config{RiskPerTradePercent: 1, StopLossPercent: 0.8, TakeProfitPercent: 1.6}
	tests := []struct {
		name       string
		cfg        Config
		balance    float64
		entryPrice float64
		side       exchange.Side
	}{
		{name: "zero balance", cfg: validConfig, balance: 0, entryPrice: 50000, side: exchange.SideLong},
		{name: "NaN balance", cfg: validConfig, balance: math.NaN(), entryPrice: 50000, side: exchange.SideLong},
		{name: "NaN entry price", cfg: validConfig, balance: 10000, entryPrice: math.NaN(), side: exchange.SideLong},
		{name: "infinite entry price", cfg: validConfig, balance: 10000, entryPrice: math.Inf(1), side: exchange.SideLong},
		{name: "NaN config", cfg: Config{RiskPerTradePercent: math.NaN(), StopLossPercent: 0.8, TakeProfitPercent: 1.6}, balance: 10000, entryPrice: 50000, side: exchange.SideLong},
		{name: "overflowing result", cfg: Config{RiskPerTradePercent: math.MaxFloat64, StopLossPercent: 0.8, TakeProfitPercent: 1.6}, balance: math.MaxFloat64, entryPrice: 50000, side: exchange.SideLong},
		{name: "non-positive long stop loss", cfg: Config{RiskPerTradePercent: 1, StopLossPercent: 100, TakeProfitPercent: 1.6}, balance: 10000, entryPrice: 50000, side: exchange.SideLong},
		{name: "non-positive short take profit", cfg: Config{RiskPerTradePercent: 1, StopLossPercent: 0.8, TakeProfitPercent: 100}, balance: 10000, entryPrice: 50000, side: exchange.SideShort},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := New(test.cfg)
			if _, err := manager.Plan(test.balance, test.entryPrice, test.side); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestPlanUsesFeeAndSlippageReserveOnlyForPositionSize(t *testing.T) {
	t.Parallel()

	withoutReserve := New(Config{
		RiskPerTradePercent: 1,
		StopLossPercent:     0.8,
		TakeProfitPercent:   1.6,
	})
	withReserve := New(Config{
		RiskPerTradePercent:          1,
		StopLossPercent:              0.8,
		TakeProfitPercent:            1.6,
		EstimatedRoundTripFeePercent: 0.11,
		SlippageAllowancePercent:     0.04,
	})

	basePlan, err := withoutReserve.Plan(10000, 50000, exchange.SideLong)
	if err != nil {
		t.Fatal(err)
	}
	reservedPlan, err := withReserve.Plan(10000, 50000, exchange.SideLong)
	if err != nil {
		t.Fatal(err)
	}

	if reservedPlan.Qty >= basePlan.Qty {
		t.Fatalf("reserved Qty = %f, want less than %f", reservedPlan.Qty, basePlan.Qty)
	}
	assertClose(t, reservedPlan.StopLoss, basePlan.StopLoss)
	assertClose(t, reservedPlan.TakeProfit, basePlan.TakeProfit)
	assertClose(t, reservedPlan.RiskUSD, basePlan.RiskUSD)
}

func TestPlanRejectsInvalidReserve(t *testing.T) {
	t.Parallel()

	manager := New(Config{
		RiskPerTradePercent:          1,
		StopLossPercent:              0.8,
		TakeProfitPercent:            1.6,
		EstimatedRoundTripFeePercent: -0.01,
	})

	if _, err := manager.Plan(10000, 50000, exchange.SideLong); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
}

func assertClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-want) > 0.000001 {
		t.Fatalf("got %f, want %f", got, want)
	}
}
