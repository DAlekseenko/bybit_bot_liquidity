package execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
	"bybit_bot_liquidity/internal/risk"
	"bybit_bot_liquidity/internal/strategy"
)

func TestExecutePlacesLongOrder(t *testing.T) {
	rules := testInstrumentRules()
	rules.QtyStep = 0.1
	rules.MinOrderQty = 0.1
	fakeExchange := &exchangeMock{rules: rules}
	fakeStore := &signalStoreMock{}
	executor := New(
		fakeExchange,
		risk.New(risk.Config{RiskPerTradePercent: 1, StopLossPercent: 0.8, TakeProfitPercent: 1.6}),
		fakeStore,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Minute,
	)

	order, err := executor.Execute(context.Background(), strategy.Signal{
		Symbol: "BTCUSDT",
		Type:   strategy.SignalLong,
		Reason: "test",
	}, 10000, 50000)
	if err != nil {
		t.Fatal(err)
	}

	if order.ID != "order-1" {
		t.Fatalf("unexpected order id %q", order.ID)
	}
	if fakeExchange.lastOrder.Side != exchange.SideLong {
		t.Fatalf("expected LONG, got %s", fakeExchange.lastOrder.Side)
	}
	if fakeExchange.lastOrder.Qty != 0.2 {
		t.Fatalf("normalized Qty = %v, want 0.2", fakeExchange.lastOrder.Qty)
	}
	if fakeStore.saved.Type != strategy.SignalLong {
		t.Fatalf("signal was not saved")
	}
}

func TestExecuteRejectsOpenPosition(t *testing.T) {
	fakeExchange := &exchangeMock{
		position: &exchange.Position{Symbol: "BTCUSDT", Side: exchange.SideLong},
	}
	executor := New(
		fakeExchange,
		risk.New(risk.Config{RiskPerTradePercent: 1, StopLossPercent: 0.8, TakeProfitPercent: 1.6}),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Minute,
	)

	_, err := executor.Execute(context.Background(), strategy.Signal{
		Symbol: "BTCUSDT",
		Type:   strategy.SignalLong,
	}, 10000, 50000)
	if err != ErrPositionAlreadyOpen {
		t.Fatalf("got %v, want %v", err, ErrPositionAlreadyOpen)
	}
}

func TestExecuteRejectsCooldown(t *testing.T) {
	now := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	executor := New(
		&exchangeMock{},
		risk.New(risk.Config{RiskPerTradePercent: 1, StopLossPercent: 0.8, TakeProfitPercent: 1.6}),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Minute,
	)
	executor.SetNow(func() time.Time { return now })
	executor.MarkPositionClosed(now.Add(-5 * time.Minute))

	_, err := executor.Execute(context.Background(), strategy.Signal{
		Symbol: "BTCUSDT",
		Type:   strategy.SignalLong,
	}, 10000, 50000)
	if err != ErrCooldownActive {
		t.Fatalf("got %v, want %v", err, ErrCooldownActive)
	}
}

func TestExecuteAllowsOnlyOneConcurrentOrder(t *testing.T) {
	fakeExchange := &countingExchange{}
	executor := New(
		fakeExchange,
		risk.New(risk.Config{RiskPerTradePercent: 1, StopLossPercent: 0.8, TakeProfitPercent: 1.6}),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Minute,
	)

	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := executor.Execute(context.Background(), strategy.Signal{
				Symbol: "BTCUSDT",
				Type:   strategy.SignalLong,
			}, 10000, 50000)
			errorsCh <- err
		}()
	}
	close(start)

	var succeeded int
	var rejected int
	for range 2 {
		err := <-errorsCh
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrPositionAlreadyOpen):
			rejected++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded = %d, rejected = %d", succeeded, rejected)
	}
	if got := fakeExchange.orderCount(); got != 1 {
		t.Fatalf("orders = %d, want 1", got)
	}
}

func TestMarkPositionClosedAllowsNextOrderAfterCooldown(t *testing.T) {
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	fakeExchange := &countingExchange{}
	executor := New(
		fakeExchange,
		risk.New(risk.Config{RiskPerTradePercent: 1, StopLossPercent: 0.8, TakeProfitPercent: 1.6}),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Minute,
	)
	executor.SetNow(func() time.Time { return now })
	signal := strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalLong}

	if _, err := executor.Execute(context.Background(), signal, 10000, 50000); err != nil {
		t.Fatal(err)
	}
	executor.MarkPositionClosed(now.Add(-10 * time.Minute))
	if _, err := executor.Execute(context.Background(), signal, 10000, 50000); err != nil {
		t.Fatal(err)
	}

	if got := fakeExchange.orderCount(); got != 2 {
		t.Fatalf("orders = %d, want 2", got)
	}
}

func TestExecuteDoesNotLockPositionAfterRejectedOrder(t *testing.T) {
	placeErr := errors.New("place order")
	fakeExchange := &exchangeMock{placeOrderErr: placeErr}
	executor := New(
		fakeExchange,
		risk.New(risk.Config{RiskPerTradePercent: 1, StopLossPercent: 0.8, TakeProfitPercent: 1.6}),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Minute,
	)
	signal := strategy.Signal{Symbol: "BTCUSDT", Type: strategy.SignalLong}

	if _, err := executor.Execute(context.Background(), signal, 10000, 50000); !errors.Is(err, placeErr) {
		t.Fatalf("first Execute() error = %v, want %v", err, placeErr)
	}
	fakeExchange.placeOrderErr = nil
	if _, err := executor.Execute(context.Background(), signal, 10000, 50000); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
}

type exchangeMock struct {
	position      *exchange.Position
	lastOrder     exchange.OrderRequest
	rules         exchange.InstrumentRules
	placeOrderErr error
}

func (m *exchangeMock) GetInstrumentRules(_ context.Context, symbol string) (exchange.InstrumentRules, error) {
	if m.rules.Symbol == "" {
		return exchange.InstrumentRules{
			Symbol:            symbol,
			Status:            "Trading",
			TickSize:          0.1,
			MinPrice:          0.1,
			MaxPrice:          1000000,
			QtyStep:           0.001,
			MinOrderQty:       0.001,
			MaxOrderQty:       100,
			MaxMarketOrderQty: 100,
			MinNotionalValue:  5,
		}, nil
	}
	return m.rules, nil
}

func (m *exchangeMock) PlaceOrder(ctx context.Context, order exchange.OrderRequest) (exchange.Order, error) {
	m.lastOrder = order
	if m.placeOrderErr != nil {
		return exchange.Order{}, m.placeOrderErr
	}
	return exchange.Order{ID: "order-1", Symbol: order.Symbol, Side: order.Side, Qty: order.Qty}, nil
}

func (m *exchangeMock) GetPosition(ctx context.Context, symbol string) (*exchange.Position, error) {
	return m.position, nil
}

func (m *exchangeMock) ClosePosition(ctx context.Context, symbol string) error {
	return nil
}

type signalStoreMock struct {
	saved strategy.Signal
}

type countingExchange struct {
	mu     sync.Mutex
	orders int
}

func (e *countingExchange) GetInstrumentRules(_ context.Context, symbol string) (exchange.InstrumentRules, error) {
	return exchange.InstrumentRules{
		Symbol:            symbol,
		Status:            "Trading",
		TickSize:          0.1,
		MinPrice:          0.1,
		MaxPrice:          1000000,
		QtyStep:           0.001,
		MinOrderQty:       0.001,
		MaxOrderQty:       100,
		MaxMarketOrderQty: 100,
		MinNotionalValue:  5,
	}, nil
}

func (e *countingExchange) PlaceOrder(_ context.Context, order exchange.OrderRequest) (exchange.Order, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.orders++
	return exchange.Order{ID: "order", Symbol: order.Symbol, Side: order.Side, Qty: order.Qty}, nil
}

func (e *countingExchange) GetPosition(context.Context, string) (*exchange.Position, error) {
	return nil, nil
}

func (e *countingExchange) ClosePosition(context.Context, string) error {
	return nil
}

func (e *countingExchange) orderCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.orders
}

func (m *signalStoreMock) SaveSignal(ctx context.Context, signal strategy.Signal) error {
	m.saved = signal
	return nil
}
