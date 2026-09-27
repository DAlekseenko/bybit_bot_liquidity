package main

import (
	"context"
	"errors"
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

type liquidationStreamerMock struct {
	cancel context.CancelFunc
	event  exchange.Liquidation
	calls  int
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
