package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

func TestRunLiquidationStreamReconnectsAndStops(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	streamer := &liquidationStreamerMock{cancel: cancel}
	runLiquidationStream(ctx, discardLogger(), streamer, "BTCUSDT", make(chan exchange.Liquidation), time.Millisecond)

	if streamer.calls != 2 {
		t.Fatalf("stream calls = %d, want 2", streamer.calls)
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
	calls  int
}

func (m *liquidationStreamerMock) Stream(context.Context, string, chan<- exchange.Liquidation) error {
	m.calls++
	if m.calls == 2 {
		m.cancel()
	}
	return errors.New("disconnected")
}
