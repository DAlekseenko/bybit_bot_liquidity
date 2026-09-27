package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"bybit_bot_liquidity/internal/exchange"
	"bybit_bot_liquidity/internal/marketdata"
)

type liquidationStreamer interface {
	Stream(ctx context.Context, symbol string, output chan<- exchange.Liquidation) error
}

type generationLiquidation struct {
	generation  uint64
	liquidation exchange.Liquidation
	observedAt  time.Time
}

type reconnectPolicy struct {
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	JitterPercent float64
}

// defaultReconnectPolicy задаёт базовые пределы повторного подключения.
func defaultReconnectPolicy() reconnectPolicy {
	return reconnectPolicy{
		InitialDelay:  5 * time.Second,
		MaxDelay:      time.Minute,
		JitterPercent: 0.2,
	}
}

// delay рассчитывает экспоненциальную задержку с ограниченным jitter.
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

// runLiquidationStream запускает поток с политикой повторного подключения по умолчанию.
func runLiquidationStream(
	ctx context.Context,
	log *slog.Logger,
	streamer liquidationStreamer,
	symbol string,
	output chan<- exchange.Liquidation,
	retryDelay time.Duration,
) {
	policy := defaultReconnectPolicy()
	policy.InitialDelay = retryDelay
	var generation uint64
	events := make(chan generationLiquidation, cap(output))
	forwardCtx, cancelForwarder := context.WithCancel(ctx)
	forwarderDone := make(chan struct{})
	go func() {
		defer close(forwarderDone)
		for {
			select {
			case <-forwardCtx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				select {
				case output <- event.liquidation:
				case <-forwardCtx.Done():
					return
				}
			}
		}
	}()
	runLiquidationStreamWithPolicy(ctx, log, streamer, symbol, events, policy, func() uint64 {
		generation++
		return generation
	}, func() {}, rand.Float64)
	cancelForwarder()
	<-forwarderDone
}

// runLiquidationStreamWithPolicy привязывает отдельную очередь к каждому поколению.
func runLiquidationStreamWithPolicy(
	ctx context.Context,
	log *slog.Logger,
	streamer liquidationStreamer,
	symbol string,
	output chan<- generationLiquidation,
	policy reconnectPolicy,
	beginGeneration func() uint64,
	invalidate func(),
	random func() float64,
) {
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
	if beginGeneration == nil || invalidate == nil {
		log.Error("liquidation stream generation callbacks are required")
		return
	}

	attempt := 0
	for {
		generation := beginGeneration()
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		streamEvents := make(chan exchange.Liquidation, cap(output))
		collectorDone := make(chan struct{})
		go func() {
			defer close(collectorDone)
			collectLiquidationGeneration(attemptCtx, streamEvents, generation, output)
		}()
		streamResult := make(chan error, 1)
		go func() { streamResult <- streamer.Stream(attemptCtx, symbol, streamEvents) }()
		var err error
		select {
		case err = <-streamResult:
		case <-ctx.Done():
			cancelAttempt()
			<-streamResult
			<-collectorDone
			return
		}
		if ctx.Err() != nil {
			cancelAttempt()
			<-collectorDone
			return
		}
		attempt++
		delay := policy.delay(attempt, random)
		// Поколение инвалидируется до отмены сборщика и обработки его очереди.
		invalidate()
		close(streamEvents)
		<-collectorDone
		cancelAttempt()
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

// collectLiquidationGeneration помечает события неизменяемым токеном попытки.
func collectLiquidationGeneration(ctx context.Context, output <-chan exchange.Liquidation, generation uint64, destination chan<- generationLiquidation) {
	for {
		select {
		case <-ctx.Done():
			return
		case liquidation, ok := <-output:
			if !ok {
				return
			}
			select {
			case destination <- generationLiquidation{generation: generation, liquidation: liquidation, observedAt: time.Now()}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// collectLiquidations переносит события канала в переданный обработчик.
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

// collectGenerationLiquidations сохраняет только события с их токеном поколения.
func collectGenerationLiquidations(ctx context.Context, output <-chan generationLiquidation, window *marketdata.LiquidationWindow) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-output:
			if !ok {
				return
			}
			window.AddForGeneration(event.generation, event.liquidation, event.observedAt)
		}
	}
}
