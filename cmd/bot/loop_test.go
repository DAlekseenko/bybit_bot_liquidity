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

// TestReadOnlyRunnerDropsSnapshotWhenLiquidationGenerationResets проверяет reset во время REST-запроса.
func TestReadOnlyRunnerDropsSnapshotWhenLiquidationGenerationResets(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 5, 0, 0, time.UTC)
	window := marketdata.NewLiquidationWindowWithClock(0, func() time.Time { return now })
	window.AddForGeneration(window.BeginGeneration(), exchange.Liquidation{Symbol: "BTCUSDT", Price: 100}, now.Add(-5*time.Minute))
	collector := &barrierSnapshotCollector{entered: make(chan struct{}), resume: make(chan struct{}), snapshot: marketdata.Snapshot{OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 101, Time: now}}}
	evaluator := &signalEvaluatorMock{evaluation: strategy.Evaluation{Signal: strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalLong}}}
	store := &signalStoreMock{}
	runner := newReadOnlyRunner(discardLogger(), "BTCUSDT", collector, evaluator, store, nil, window, testStrategyCandleLimit)
	runner.previousOpenInterest = &exchange.OpenInterest{Symbol: "BTCUSDT", Value: 100, Time: now.Add(-5 * time.Minute)}
	runner.previousOIGeneration = 1
	done := make(chan struct{})
	go func() { runner.Tick(context.Background()); close(done) }()
	<-collector.entered
	func() { window.Reset(); close(collector.resume); <-done }()
	if evaluator.calls != 0 || len(store.records) != 0 || runner.previousOpenInterest != nil {
		t.Fatalf("after concurrent reset: evaluator=%d records=%d previousOI=%+v", evaluator.calls, len(store.records), runner.previousOpenInterest)
	}
	window.AddForGeneration(window.BeginGeneration(), exchange.Liquidation{Symbol: "BTCUSDT", Time: now}, now)
	resetDone, resetStarted := make(chan struct{}), make(chan struct{})
	store = &signalStoreMock{resetDone: resetDone}
	evaluator = &signalEvaluatorMock{evaluation: strategy.Evaluation{Signal: strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalNone}}, entered: make(chan struct{}), resume: make(chan struct{})}
	secondCollector := &snapshotCollectorMock{snapshots: []marketdata.Snapshot{{OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 101, Time: now.Add(5 * time.Minute)}}, {OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 102, Time: now.Add(10 * time.Minute)}}}}
	runner = newReadOnlyRunner(discardLogger(), "BTCUSDT", secondCollector, evaluator, store, nil, window, testStrategyCandleLimit)
	runner.Tick(context.Background())
	done = make(chan struct{})
	go func() { runner.Tick(context.Background()); close(done) }()
	<-evaluator.entered
	go func() { close(resetStarted); window.Reset(); close(resetDone) }()
	<-resetStarted
	close(evaluator.resume)
	<-done
	<-resetDone
	if len(store.records) != 1 || store.savedAfterReset {
		t.Fatalf("signal persistence crossed reset: records=%d savedAfterReset=%t", len(store.records), store.savedAfterReset)
	}
}

// TestReadOnlyRunnerWaitsForCompletePostReconnectWindow проверяет baseline и оценку после покрытия окна.
func TestReadOnlyRunnerWaitsForCompletePostReconnectWindow(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	window := marketdata.NewLiquidationWindowWithClock(5*time.Minute, func() time.Time { return now })
	oldGeneration := window.BeginGeneration()
	window.AddForGeneration(oldGeneration, exchange.Liquidation{Symbol: "BTCUSDT", Time: now, Price: 199}, now)
	buffered := make(chan generationLiquidation, 1)
	buffered <- generationLiquidation{generation: oldGeneration, liquidation: exchange.Liquidation{Symbol: "BTCUSDT", Time: now, Price: 199}, observedAt: now}
	collector := &snapshotCollectorMock{snapshots: []marketdata.Snapshot{{OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 99, Time: now}}, {OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 100, Time: now.Add(5 * time.Minute)}}, {OpenInterest: exchange.OpenInterest{Symbol: "BTCUSDT", Value: 101, Time: now.Add(10 * time.Minute)}}}, err: errors.New("temporary REST error")}
	evaluator := &signalEvaluatorMock{evaluation: strategy.Evaluation{Signal: strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalNone}}}
	store := &signalStoreMock{}
	runner := newReadOnlyRunner(discardLogger(), "BTCUSDT", collector, evaluator, store, nil, window, testStrategyCandleLimit)
	runner.now = func() time.Time { return now }
	now = now.Add(5 * time.Minute)
	runner.Tick(context.Background())
	window.Reset()
	close(buffered)
	collectGenerationLiquidations(context.Background(), buffered, window)
	generation := window.BeginGeneration()
	window.AddForGeneration(generation, exchange.Liquidation{Symbol: "BTCUSDT", Time: now, Price: 200}, now)
	if window.Snapshot(now).Ready {
		t.Fatal("new generation became ready before full retention")
	}
	now = now.Add(5 * time.Minute)
	collector.failNext = true
	runner.Tick(context.Background())
	if runner.previousOpenInterest != nil {
		t.Fatalf("REST error retained old-generation baseline: %+v", runner.previousOpenInterest)
	}
	runner.Tick(context.Background())
	now = now.Add(5 * time.Minute)
	newEvent := exchange.Liquidation{Symbol: "BTCUSDT", Time: now, Price: 201}
	window.AddForGeneration(generation, newEvent, now)
	runner.Tick(context.Background())
	if evaluator.calls != 1 || len(store.records) != 1 || evaluator.lastInput.PreviousOpenInterest.Value != 100 || len(evaluator.lastInput.Liquidations) != 1 || evaluator.lastInput.Liquidations[0] != newEvent {
		t.Fatalf("post-reconnect evaluation: evaluator=%d records=%d liquidations=%#v", evaluator.calls, len(store.records), evaluator.lastInput.Liquidations)
	}
}

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
	failNext  bool
	calls     int
}

// Snapshot возвращает очередной тестовый снимок с переданными ликвидациями.
func (m *snapshotCollectorMock) Snapshot(ctx context.Context, candleLimit int, liquidations []exchange.Liquidation) (marketdata.Snapshot, error) {
	if m.failNext {
		m.failNext = false
		return marketdata.Snapshot{}, m.err
	}
	if m.calls >= len(m.snapshots) {
		return marketdata.Snapshot{}, m.err
	}

	snapshot := m.snapshots[m.calls]
	m.calls++
	snapshot.Liquidations = liquidations
	return snapshot, nil
}

type signalEvaluatorMock struct {
	evaluation strategy.Evaluation
	lastInput  strategy.Input
	calls      int
	entered    chan struct{}
	resume     chan struct{}
}

type signalStoreMock struct {
	records         []storage.SignalRecord
	closed          []closedVirtualSignal
	nextID          int64
	err             error
	closeErr        error
	resetDone       chan struct{}
	savedAfterReset bool
}

type closedVirtualSignal struct {
	signalID int64
	outcome  storage.VirtualOutcome
}

type liquidationSourceMock struct {
	events []exchange.Liquidation
}

// Snapshot возвращает готовый снимок тестового источника ликвидаций.
func (m *liquidationSourceMock) Snapshot(_ time.Time) marketdata.LiquidationSnapshot {
	return marketdata.LiquidationSnapshot{Events: append([]exchange.Liquidation(nil), m.events...), Generation: 1, Ready: true}
}

// AcquireReadyGeneration выдаёт тестовый lease только для заданного поколения.
func (m *liquidationSourceMock) AcquireReadyGeneration(generation uint64, _ time.Time) (func(), bool) {
	return func() {}, generation == 1
}

type barrierSnapshotCollector struct {
	entered  chan struct{}
	resume   chan struct{}
	snapshot marketdata.Snapshot
}

// Snapshot блокирует сбор до разрешающего канала, моделируя REST interleaving.
func (c *barrierSnapshotCollector) Snapshot(context.Context, int, []exchange.Liquidation) (marketdata.Snapshot, error) {
	close(c.entered)
	<-c.resume
	return c.snapshot, nil
}

// SaveSignalRecord сохраняет сигнал в памяти для проверки цикла.
func (m *signalStoreMock) SaveSignalRecord(_ context.Context, record storage.SignalRecord) (int64, error) {
	select {
	case <-m.resetDone:
		m.savedAfterReset = true
	default:
	}
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

// EvaluateDetailed сохраняет вход и возвращает заранее заданную оценку.
func (m *signalEvaluatorMock) EvaluateDetailed(input strategy.Input) strategy.Evaluation {
	if m.entered != nil {
		close(m.entered)
		<-m.resume
	}
	m.lastInput = input
	m.calls++
	return m.evaluation
}
