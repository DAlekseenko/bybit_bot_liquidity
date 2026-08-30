package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

type liquidationStreamer interface {
	Stream(ctx context.Context, symbol string, output chan<- exchange.Liquidation) error
}

type websocketMetrics interface {
	MarkWebsocketReconnect()
}

type reconnectPolicy struct {
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	JitterPercent float64
}

func defaultReconnectPolicy() reconnectPolicy {
	return reconnectPolicy{InitialDelay: 5 * time.Second, MaxDelay: time.Minute, JitterPercent: 0.2}
}

func (p reconnectPolicy) delay(attempt int, random func() float64) time.Duration {
	delay := p.InitialDelay
	for i := 1; i < attempt && delay < p.MaxDelay; i++ {
		delay *= 2
		if delay > p.MaxDelay {
			delay = p.MaxDelay
		}
	}
	if p.JitterPercent == 0 {
		return delay
	}
	return time.Duration(float64(delay) * (1 - p.JitterPercent + 2*p.JitterPercent*random()))
}

func runLiquidationStream(ctx context.Context, log *slog.Logger, streamer liquidationStreamer, symbol string, output chan<- exchange.Liquidation, retryDelay time.Duration) {
	policy := defaultReconnectPolicy()
	policy.InitialDelay = retryDelay
	runLiquidationStreamWithPolicy(ctx, log, streamer, symbol, output, policy, nil, nil, rand.Float64)
}

// runLiquidationStreamWithPolicy reconnects a public data stream with bounded
// exponential backoff. recovery invalidates state that cannot be replayed.
func runLiquidationStreamWithPolicy(ctx context.Context, log *slog.Logger, streamer liquidationStreamer, symbol string, output chan<- exchange.Liquidation, policy reconnectPolicy, recovery func(), metrics websocketMetrics, random func() float64) {
	if log == nil {
		log = slog.Default()
	}
	if policy.InitialDelay <= 0 || policy.MaxDelay < policy.InitialDelay || policy.JitterPercent < 0 || policy.JitterPercent > 1 {
		log.Error("invalid liquidation reconnect policy")
		return
	}
	if random == nil {
		random = rand.Float64
	}

	attempt := 0
	for {
		err := streamer.Stream(ctx, symbol, output)
		if ctx.Err() != nil {
			return
		}
		attempt++
		delay := policy.delay(attempt, random)
		if recovery != nil {
			recovery()
		}
		if metrics != nil {
			metrics.MarkWebsocketReconnect()
		}
		log.Warn("liquidation websocket disconnected; liquidation window reset", "symbol", symbol, "error", err, "attempt", attempt, "retry_in", delay)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func collectLiquidations(ctx context.Context, output <-chan exchange.Liquidation, add func(exchange.Liquidation)) {
	for {
		select {
		case <-ctx.Done():
			return
		case liquidation, ok := <-output:
			if !ok {
				return
			}
			add(liquidation)
		}
	}
}
