package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
	"bybit_bot_liquidity/internal/marketdata"
	"bybit_bot_liquidity/internal/papertrade"
	"bybit_bot_liquidity/internal/risk"
	"bybit_bot_liquidity/internal/storage"
	"bybit_bot_liquidity/internal/strategy"
)

const testStrategyCandleLimit = 22

func TestReadOnlyRunnerInitializesPreviousOpenInterest(t *testing.T) {
	openInterestTime := time.Date(2026, 7, 22, 11, 55, 0, 0, time.UTC)
	collector := &snapshotCollectorMock{
		snapshots: []marketdata.Snapshot{
			{OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 100, Time: openInterestTime}},
		},
	}
	evaluator := &signalEvaluatorMock{}
	runner := newReadOnlyRunner(discardLogger(), "BTCUSDT", collector, evaluator, &signalStoreMock{}, nil, &liquidationSourceMock{}, testStrategyCandleLimit)

	runner.Tick(context.Background())

	if runner.previousOpenInterest == nil || runner.previousOpenInterest.Value != 100 {
		t.Fatalf("previousOpenInterest = %+v, want value 100", runner.previousOpenInterest)
	}
	if evaluator.calls != 0 {
		t.Fatalf("evaluator calls = %d, want 0", evaluator.calls)
	}
}

func TestReadOnlyRunnerEvaluatesSignalAfterSecondSnapshot(t *testing.T) {
	collectedAt := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	previousTime := collectedAt.Add(-5 * time.Minute)
	collector := &snapshotCollectorMock{
		snapshots: []marketdata.Snapshot{
			{OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 100, Time: previousTime}},
			{
				Candles: []exchange.Candle{{
					Symbol:    "BTCUSDT",
					OpenTime:  collectedAt.Add(-time.Minute),
					Open:      99,
					High:      101,
					Low:       99,
					Close:     100,
					Volume:    10,
					Confirmed: true,
				}},
				OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 104, Time: collectedAt},
				Liquidations: []exchange.Liquidation{{Symbol: "BTCUSDT", Side: exchange.SideShort, Price: 100, Qty: 2}},
				CollectedAt:  collectedAt,
			},
		},
	}
	evaluator := &signalEvaluatorMock{
		evaluation: strategy.Evaluation{
			Signal: strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalNone, Reason: "test"},
			Indicators: strategy.Indicators{
				Complete:                 true,
				PreviousOpenInterest:     100,
				CurrentOpenInterest:      104,
				OIGrowthPercent:          4,
				OIGrowthThresholdPercent: 0.5,
			},
		},
	}
	store := &signalStoreMock{}
	liquidations := &liquidationSourceMock{
		events: []exchange.Liquidation{{Symbol: "BTCUSDT", Side: exchange.SideShort, Price: 100, Qty: 2}},
	}
	runner := newReadOnlyRunner(discardLogger(), "BTCUSDT", collector, evaluator, store, nil, liquidations, testStrategyCandleLimit)

	runner.Tick(context.Background())
	runner.Tick(context.Background())

	if evaluator.calls != 1 {
		t.Fatalf("evaluator calls = %d, want 1", evaluator.calls)
	}
	if len(store.records) != 1 || store.records[0].Signal != evaluator.evaluation.Signal {
		t.Fatalf("saved records = %#v, want signal %#v", store.records, evaluator.evaluation.Signal)
	}
	if store.records[0].SignalPrice == nil || *store.records[0].SignalPrice != 100 {
		t.Fatalf("signal price = %v, want 100", store.records[0].SignalPrice)
	}
	if store.records[0].Indicators == nil || *store.records[0].Indicators != evaluator.evaluation.Indicators {
		t.Fatalf("indicators = %#v, want %#v", store.records[0].Indicators, evaluator.evaluation.Indicators)
	}
	input := evaluator.lastInput
	if input.PreviousOpenInterest.Value != 100 || input.CurrentOpenInterest.Value != 104 {
		t.Fatalf("unexpected OI input: previous=%f current=%f", input.PreviousOpenInterest.Value, input.CurrentOpenInterest.Value)
	}
	if !input.Now.Equal(collectedAt) {
		t.Fatalf("Now = %s, want %s", input.Now, collectedAt)
	}
	if len(input.Liquidations) != 1 || input.Liquidations[0] != liquidations.events[0] {
		t.Fatalf("Liquidations = %#v, want %#v", input.Liquidations, liquidations.events)
	}
	if runner.previousOpenInterest == nil || runner.previousOpenInterest.Value != 104 {
		t.Fatalf("previousOpenInterest = %+v, want value 104", runner.previousOpenInterest)
	}
}

func TestReadOnlyRunnerWaitsForNextOpenInterestWindow(t *testing.T) {
	openInterestTime := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	collector := &snapshotCollectorMock{
		snapshots: []marketdata.Snapshot{
			{OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 100, Time: openInterestTime}},
			{OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 101, Time: openInterestTime}},
		},
	}
	evaluator := &signalEvaluatorMock{}
	runner := newReadOnlyRunner(discardLogger(), "BTCUSDT", collector, evaluator, &signalStoreMock{}, nil, &liquidationSourceMock{}, testStrategyCandleLimit)

	runner.Tick(context.Background())
	runner.Tick(context.Background())

	if evaluator.calls != 0 {
		t.Fatalf("evaluator calls = %d, want 0", evaluator.calls)
	}
	if runner.previousOpenInterest == nil || runner.previousOpenInterest.Value != 101 {
		t.Fatalf("previousOpenInterest = %+v, want latest value 101", runner.previousOpenInterest)
	}
}

func TestReadOnlyRunnerKeepsPreviousOpenInterestOnSnapshotError(t *testing.T) {
	openInterestTime := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	collector := &snapshotCollectorMock{
		snapshots: []marketdata.Snapshot{
			{OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 100, Time: openInterestTime}},
		},
		err: errors.New("snapshot failed"),
	}
	runner := newReadOnlyRunner(discardLogger(), "BTCUSDT", collector, &signalEvaluatorMock{}, &signalStoreMock{}, nil, &liquidationSourceMock{}, testStrategyCandleLimit)

	runner.Tick(context.Background())
	runner.Tick(context.Background())

	if runner.previousOpenInterest == nil || runner.previousOpenInterest.Value != 100 {
		t.Fatalf("previousOpenInterest = %+v, want value 100", runner.previousOpenInterest)
	}
}

func TestReadOnlyRunnerRetriesSameOpenInterestAfterStorageError(t *testing.T) {
	previousTime := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	currentTime := previousTime.Add(5 * time.Minute)
	current := marketdata.Snapshot{
		OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 101, Time: currentTime},
		CollectedAt:  currentTime,
	}
	collector := &snapshotCollectorMock{snapshots: []marketdata.Snapshot{
		{OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 100, Time: previousTime}},
		current,
		current,
	}}
	store := &signalStoreMock{err: errors.New("storage unavailable")}
	evaluator := &signalEvaluatorMock{evaluation: strategy.Evaluation{
		Signal: strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalNone, Reason: "test"},
	}}
	runner := newReadOnlyRunner(discardLogger(), "BTCUSDT", collector, evaluator, store, nil, &liquidationSourceMock{}, testStrategyCandleLimit)

	runner.Tick(context.Background())
	runner.Tick(context.Background())
	if runner.previousOpenInterest == nil || !runner.previousOpenInterest.Time.Equal(previousTime) {
		t.Fatalf("previous OI advanced after storage error: %+v", runner.previousOpenInterest)
	}

	store.err = nil
	runner.Tick(context.Background())
	if evaluator.calls != 2 {
		t.Fatalf("evaluator calls = %d, want 2", evaluator.calls)
	}
	if runner.previousOpenInterest == nil || !runner.previousOpenInterest.Time.Equal(currentTime) {
		t.Fatalf("previous OI was not advanced after retry: %+v", runner.previousOpenInterest)
	}
}

func TestReadOnlyRunnerStoresAndClosesVirtualSignal(t *testing.T) {
	openInterestTime := time.Date(2026, 7, 28, 10, 5, 0, 0, time.UTC)
	signalCandleTime := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	signalCandle := exchange.Candle{
		Symbol:    "BTCUSDT",
		OpenTime:  signalCandleTime,
		Open:      99,
		High:      100,
		Low:       99,
		Close:     100,
		Confirmed: true,
	}
	exitCandle := exchange.Candle{
		Symbol:    "BTCUSDT",
		OpenTime:  signalCandleTime.Add(time.Minute),
		Open:      100,
		High:      103,
		Low:       100,
		Close:     102,
		Confirmed: true,
	}
	collector := &snapshotCollectorMock{
		snapshots: []marketdata.Snapshot{
			{OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 100, Time: openInterestTime.Add(-5 * time.Minute)}},
			{
				Candles:      []exchange.Candle{signalCandle},
				OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 104, Time: openInterestTime},
				CollectedAt:  openInterestTime,
			},
			{
				Candles:      []exchange.Candle{signalCandle, exitCandle},
				OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 104, Time: openInterestTime},
				CollectedAt:  openInterestTime.Add(time.Minute),
			},
		},
	}
	evaluator := &signalEvaluatorMock{
		evaluation: strategy.Evaluation{
			Signal: strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalLong, Reason: "virtual"},
		},
	}
	store := &signalStoreMock{nextID: 42}
	paper, err := papertrade.New(papertrade.Config{
		VirtualBalanceUSD: 1000,
		Risk: risk.Config{
			RiskPerTradePercent:          1,
			StopLossPercent:              0.8,
			TakeProfitPercent:            1.6,
			EstimatedRoundTripFeePercent: 0.11,
			SlippageAllowancePercent:     0.04,
		},
		Cooldown:        10 * time.Minute,
		MaxTradesPerDay: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newReadOnlyRunner(discardLogger(), "BTCUSDT", collector, evaluator, store, paper, &liquidationSourceMock{}, testStrategyCandleLimit)

	runner.Tick(context.Background())
	runner.Tick(context.Background())
	runner.Tick(context.Background())

	if len(store.records) != 1 {
		t.Fatalf("records = %d, want 1", len(store.records))
	}
	record := store.records[0]
	if record.SignalPrice == nil || *record.SignalPrice != 100 || record.Virtual == nil {
		t.Fatalf("unexpected record: %+v", record)
	}
	if len(store.closed) != 1 {
		t.Fatalf("closed outcomes = %d, want 1", len(store.closed))
	}
	if store.closed[0].signalID != 42 || store.closed[0].outcome.ExitReason != papertrade.ExitTakeProfit {
		t.Fatalf("unexpected close: %+v", store.closed[0])
	}
	if store.closed[0].outcome.PnL <= 0 {
		t.Fatalf("virtual PnL = %f, want positive", store.closed[0].outcome.PnL)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type snapshotCollectorMock struct {
	snapshots []marketdata.Snapshot
	err       error
	calls     int
}

func (m *snapshotCollectorMock) Snapshot(ctx context.Context, candleLimit int, liquidations []exchange.Liquidation) (marketdata.Snapshot, error) {
	if m.calls >= len(m.snapshots) {
		return marketdata.Snapshot{}, m.err
	}

	snapshot := m.snapshots[m.calls]
	m.calls++
	return snapshot, nil
}

type signalEvaluatorMock struct {
	evaluation strategy.Evaluation
	lastInput  strategy.Input
	calls      int
}

type signalStoreMock struct {
	records  []storage.SignalRecord
	closed   []closedVirtualSignal
	nextID   int64
	err      error
	closeErr error
}

type closedVirtualSignal struct {
	signalID int64
	outcome  storage.VirtualOutcome
}

type liquidationSourceMock struct {
	events []exchange.Liquidation
}

func (m *liquidationSourceMock) Recent(_ time.Time) []exchange.Liquidation {
	return m.events
}

func (m *signalStoreMock) SaveSignalRecord(_ context.Context, record storage.SignalRecord) (int64, error) {
	m.records = append(m.records, record)
	if m.nextID == 0 {
		m.nextID = int64(len(m.records))
	}
	return m.nextID, m.err
}

func (m *signalStoreMock) CloseVirtualSignal(_ context.Context, signalID int64, outcome storage.VirtualOutcome) error {
	m.closed = append(m.closed, closedVirtualSignal{signalID: signalID, outcome: outcome})
	return m.closeErr
}

func (m *signalEvaluatorMock) EvaluateDetailed(input strategy.Input) strategy.Evaluation {
	m.lastInput = input
	m.calls++
	return m.evaluation
}
