package main

import (
	"context"
	"log/slog"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

type liquidationStreamer interface {
	Stream(ctx context.Context, symbol string, output chan<- exchange.Liquidation) error
}

func runLiquidationStream(
	ctx context.Context,
	log *slog.Logger,
	streamer liquidationStreamer,
	symbol string,
	output chan<- exchange.Liquidation,
	retryDelay time.Duration,
) {
	if log == nil {
		log = slog.Default()
	}
	if retryDelay <= 0 {
		log.Error("invalid liquidation reconnect delay", "delay", retryDelay)
		return
	}

	for {
		err := streamer.Stream(ctx, symbol, output)
		if ctx.Err() != nil {
			return
		}
		log.Error("liquidation websocket disconnected", "symbol", symbol, "error", err, "retry_in", retryDelay)

		timer := time.NewTimer(retryDelay)
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
