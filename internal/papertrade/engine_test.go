package papertrade

import (
	"errors"
	"math"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
	"bybit_bot_liquidity/internal/risk"
	"bybit_bot_liquidity/internal/strategy"
)

func TestEngineLongStopLossUsesConservativeSameCandleRule(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t, 10*time.Minute, 10)
	signalCandle := testCandle(time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC), 100, 101, 99)
	plan, err := engine.PlanSignal(strategy.Signal{Type: strategy.SignalLong}, signalCandle)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HypotheticalEntry <= plan.SignalPrice {
		t.Fatalf("entry = %f, want above signal price %f", plan.HypotheticalEntry, plan.SignalPrice)
	}
	if plan.EstimatedFees <= 0 || plan.EstimatedSlippage <= 0 || plan.Qty <= 0 {
		t.Fatalf("invalid plan: %+v", plan)
	}
	if err := engine.Activate(7, plan); err != nil {
		t.Fatal(err)
	}

	if outcome := engine.Advance([]exchange.Candle{signalCandle}); outcome != nil {
		t.Fatalf("signal candle must not close trade: %+v", outcome)
	}
	exitCandle := testCandle(signalCandle.OpenTime.Add(time.Minute), 100, plan.TakeProfit+1, plan.StopLoss-1)
	outcome := engine.Advance([]exchange.Candle{exitCandle})
	if outcome == nil {
		t.Fatal("expected outcome")
	}
	if outcome.SignalID != 7 || outcome.Reason != ExitStopLoss || outcome.ExitPrice != plan.StopLoss {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	wantPnL := (plan.StopLoss-plan.HypotheticalEntry)*plan.Qty - plan.EstimatedFees
	assertClose(t, outcome.PnL, wantPnL)

	repeated := engine.Advance(nil)
	if repeated == nil || *repeated != *outcome {
		t.Fatalf("pending outcome = %+v, want %+v", repeated, outcome)
	}
	engine.ConfirmClosed()
	if engine.HasActiveTrade() {
		t.Fatal("trade must be closed")
	}
}

func TestEngineShortTakeProfit(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t, 10*time.Minute, 10)
	signalCandle := testCandle(time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC), 100, 101, 99)
	plan, err := engine.PlanSignal(strategy.Signal{Type: strategy.SignalShort}, signalCandle)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HypotheticalEntry >= plan.SignalPrice {
		t.Fatalf("entry = %f, want below signal price %f", plan.HypotheticalEntry, plan.SignalPrice)
	}
	if err := engine.Activate(8, plan); err != nil {
		t.Fatal(err)
	}

	exitCandle := testCandle(signalCandle.OpenTime.Add(time.Minute), 100, plan.StopLoss-0.01, plan.TakeProfit-0.01)
	outcome := engine.Advance([]exchange.Candle{exitCandle})
	if outcome == nil || outcome.Reason != ExitTakeProfit || outcome.ExitPrice != plan.TakeProfit {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	wantPnL := (plan.HypotheticalEntry-plan.TakeProfit)*plan.Qty - plan.EstimatedFees
	assertClose(t, outcome.PnL, wantPnL)
}

func TestEngineAppliesPositionCooldownAndDailyLimits(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t, time.Minute, 1)
	firstCandle := testCandle(time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC), 100, 101, 99)
	firstPlan, err := engine.PlanSignal(strategy.Signal{Type: strategy.SignalLong}, firstCandle)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Activate(1, firstPlan); err != nil {
		t.Fatal(err)
	}

	_, err = engine.PlanSignal(strategy.Signal{Type: strategy.SignalLong}, testCandle(firstCandle.OpenTime.Add(time.Minute), 100, 101, 99))
	if !errors.Is(err, ErrPositionLimit) || SkipReason(err) != SkipPositionLimit {
		t.Fatalf("position error = %v", err)
	}

	exitCandle := testCandle(firstCandle.OpenTime.Add(time.Minute), 100, 101, firstPlan.StopLoss-1)
	if outcome := engine.Advance([]exchange.Candle{exitCandle}); outcome == nil {
		t.Fatal("expected close")
	}
	engine.ConfirmClosed()

	cooldownCandle := testCandle(firstCandle.OpenTime.Add(time.Minute), 100, 101, 99)
	_, err = engine.PlanSignal(strategy.Signal{Type: strategy.SignalLong}, cooldownCandle)
	if !errors.Is(err, ErrCooldownActive) || SkipReason(err) != SkipCooldown {
		t.Fatalf("cooldown error = %v", err)
	}

	dailyLimitCandle := testCandle(firstCandle.OpenTime.Add(2*time.Minute), 100, 101, 99)
	_, err = engine.PlanSignal(strategy.Signal{Type: strategy.SignalLong}, dailyLimitCandle)
	if !errors.Is(err, ErrDailyLimitReached) || SkipReason(err) != SkipDailyLimit {
		t.Fatalf("daily limit error = %v", err)
	}
}

func newTestEngine(t *testing.T, cooldown time.Duration, maxTradesPerDay int) *Engine {
	t.Helper()

	engine, err := New(Config{
		VirtualBalanceUSD: 1000,
		Risk: risk.Config{
			RiskPerTradePercent:          1,
			StopLossPercent:              0.8,
			TakeProfitPercent:            1.6,
			EstimatedRoundTripFeePercent: 0.11,
			SlippageAllowancePercent:     0.04,
		},
		Cooldown:        cooldown,
		MaxTradesPerDay: maxTradesPerDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func testCandle(openTime time.Time, close, high, low float64) exchange.Candle {
	return exchange.Candle{
		OpenTime:  openTime,
		Open:      close,
		High:      high,
		Low:       low,
		Close:     close,
		Confirmed: true,
	}
}

func assertClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %f, want %f", got, want)
	}
}
