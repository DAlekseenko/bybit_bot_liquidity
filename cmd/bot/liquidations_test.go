package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

// TestRunLiquidationStreamReconnectsAndStops проверяет reconnect и отмену цикла.
func TestRunLiquidationStreamReconnectsAndStops(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	event := exchange.Liquidation{Symbol: "BTCUSDT", Price: 100}
	streamer := &liquidationStreamerMock{cancel: cancel, event: event}
	output := make(chan exchange.Liquidation, 1)
	runLiquidationStream(ctx, discardLogger(), streamer, "BTCUSDT", output, time.Millisecond)

	if streamer.calls != 2 {
		t.Fatalf("stream calls = %d, want 2", streamer.calls)
	}
	if got := <-output; got != event {
		t.Fatalf("forwarded event = %#v, want %#v", got, event)
	}
	invalidDone := make(chan struct{})
	go func() {
		runLiquidationStream(context.Background(), discardLogger(), &liquidationStreamerMock{}, "BTCUSDT", output, 0)
		close(invalidDone)
	}()
	select {
	case <-invalidDone:
	case <-time.After(time.Second):
		t.Fatal("invalid retry policy left the forwarder blocked")
	}
}

// TestReconnectPolicyUsesExponentialBackoffWithBoundedJitter проверяет границы задержки.
func TestReconnectPolicyUsesExponentialBackoffWithBoundedJitter(t *testing.T) {
	t.Parallel()

	policy := reconnectPolicy{InitialDelay: time.Second, MaxDelay: 4 * time.Second, JitterPercent: 0.2}
	if got := policy.delay(3, func() float64 { return 1 }); got != 4800*time.Millisecond {
		t.Fatalf("delay = %s, want 4.8s", got)
	}
	if got := policy.delay(10, func() float64 { return 0 }); got != 3200*time.Millisecond {
		t.Fatalf("capped delay = %s, want 3.2s", got)
	}
}

// TestCollectLiquidationsStopsWithContext проверяет отмену сборщика.
func TestCollectLiquidationsStopsWithContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	output := make(chan exchange.Liquidation, 1)
	output <- exchange.Liquidation{Symbol: "BTCUSDT", Price: 100}
	collected := make(chan exchange.Liquidation, 1)
	done := make(chan struct{})
	go func() {
		collectLiquidations(ctx, output, func(liquidation exchange.Liquidation) {
			collected <- liquidation
			cancel()
		})
		close(done)
	}()

	select {
	case liquidation := <-collected:
		if liquidation.Price != 100 {
			t.Fatalf("liquidation = %#v", liquidation)
		}
	case <-time.After(time.Second):
		t.Fatal("liquidation was not collected")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("collector did not stop")
	}
}

// TestCollectLiquidationsStopsWhenChannelCloses проверяет закрытие входа.
func TestCollectLiquidationsStopsWhenChannelCloses(t *testing.T) {
	t.Parallel()

	output := make(chan exchange.Liquidation)
	close(output)
	done := make(chan struct{})
	go func() {
		collectLiquidations(context.Background(), output, func(exchange.Liquidation) {
			t.Error("closed channel must not produce a liquidation")
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("collector did not stop after channel close")
	}
}

// TestRunLiquidationStreamMetricsCountOnlyUnexpectedDisconnect проверяет учёт reconnect и отмены.
func TestRunLiquidationStreamMetricsCountOnlyUnexpectedDisconnect(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 1)
	var calls int
	streamer := liquidationStreamerFunc(func(ctx context.Context, _ string, _ chan<- exchange.Liquidation) error {
		calls++
		if calls == 1 {
			return errors.New("private transport detail")
		}
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	metrics := &websocketMetricsStub{reconnects: make(chan struct{}, 2)}
	policy := reconnectPolicy{InitialDelay: time.Millisecond, MaxDelay: time.Millisecond}
	var logOutput bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logOutput, nil))
	done := make(chan struct{})
	go func() {
		runLiquidationStreamWithPolicy(ctx, log, streamer, "BTCUSDT", make(chan generationLiquidation, 1), policy, func() uint64 { return 1 }, func() {}, nil, metrics)
		close(done)
	}()
	select {
	case <-metrics.reconnects:
	case <-time.After(time.Second):
		t.Fatal("unexpected disconnect did not increment reconnect metric")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stream did not reconnect")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
	select {
	case <-metrics.reconnects:
		t.Fatal("normal cancellation incremented reconnect metric")
	default:
	}
	if logged := logOutput.String(); !strings.Contains(logged, "reason=stream_error") || strings.Contains(logged, "private transport detail") {
		t.Fatalf("websocket log is not safely classified: %q", logged)
	}
}

type liquidationStreamerMock struct {
	cancel context.CancelFunc
	event  exchange.Liquidation
	calls  int
}

type liquidationStreamerFunc func(context.Context, string, chan<- exchange.Liquidation) error

// Stream делегирует вызов тестовой функции потока.
func (f liquidationStreamerFunc) Stream(ctx context.Context, symbol string, output chan<- exchange.Liquidation) error {
	return f(ctx, symbol, output)
}

type websocketMetricsStub struct {
	reconnects chan struct{}
}

// MarkWebsocketReconnect записывает одно событие в тестовый канал.
func (s *websocketMetricsStub) MarkWebsocketReconnect() {
	s.reconnects <- struct{}{}
}

// Stream завершает тестовую попытку и отменяет контекст на повторном вызове.
func (m *liquidationStreamerMock) Stream(_ context.Context, _ string, output chan<- exchange.Liquidation) error {
	m.calls++
	if m.calls == 1 {
		output <- m.event
	}
	if m.calls == 2 {
		m.cancel()
	}
	return errors.New("disconnected")
}
