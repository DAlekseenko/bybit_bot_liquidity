package marketdata

import (
	"context"
	"fmt"
	"sort"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

// Exchange описывает минимальный источник рыночных данных для коллектора.
type Exchange interface {
	GetCandles(ctx context.Context, symbol string, limit int) ([]exchange.Candle, error)
	GetOpenInterest(ctx context.Context, symbol string, windowMinutes int) (exchange.OpenInterest, error)
}

// Snapshot собирает рыночное состояние, которое затем передается в strategy.
type Snapshot struct {
	// Candles - последние свечи по символу.
	Candles []exchange.Candle
	// OpenInterest - текущее значение открытого интереса.
	OpenInterest exchange.OpenInterest
	// Liquidations - накопленные события ликвидаций.
	Liquidations []exchange.Liquidation
	// CollectedAt - время сборки снимка.
	CollectedAt time.Time
}

// Collector получает и агрегирует данные без принятия торговых решений.
type Collector struct {
	exchange        Exchange
	symbol          string
	oiWindowMinutes int
	retryPolicy     RetryPolicy
	sleep           func(context.Context, time.Duration) error
	random          func() float64
}

func NewCollector(exchange Exchange, symbol string, oiWindowMinutes int) *Collector {
	return NewCollectorWithRetry(exchange, symbol, oiWindowMinutes, DefaultRetryPolicy())
}

// NewCollectorWithRetry constructs a collector with a bounded policy for safe
// idempotent reads. Invalid policies fail closed by disabling retries.
func NewCollectorWithRetry(exchange Exchange, symbol string, oiWindowMinutes int, retryPolicy RetryPolicy) *Collector {
	if !retryPolicy.valid() {
		retryPolicy = RetryPolicy{MaxAttempts: 1, InitialDelay: time.Millisecond, MaxDelay: time.Millisecond}
	}
	return &Collector{
		exchange:        exchange,
		symbol:          symbol,
		oiWindowMinutes: oiWindowMinutes,
		retryPolicy:     retryPolicy,
		sleep:           sleepWithContext,
		random:          randomFloat,
	}
}

func (c *Collector) Snapshot(ctx context.Context, candleLimit int, liquidations []exchange.Liquidation) (Snapshot, error) {
	var candles []exchange.Candle
	err := c.retryPolicy.do(ctx, func() error {
		var callErr error
		candles, callErr = c.exchange.GetCandles(ctx, c.symbol, candleLimit)
		return callErr
	}, c.sleep, c.random)
	if err != nil {
		return Snapshot{}, fmt.Errorf("get candles: %w", err)
	}
	candles = normalizeCandles(candles)

	var oi exchange.OpenInterest
	err = c.retryPolicy.do(ctx, func() error {
		var callErr error
		oi, callErr = c.exchange.GetOpenInterest(ctx, c.symbol, c.oiWindowMinutes)
		return callErr
	}, c.sleep, c.random)
	if err != nil {
		return Snapshot{}, fmt.Errorf("get open interest: %w", err)
	}

	return Snapshot{
		Candles:      candles,
		OpenInterest: oi,
		Liquidations: liquidations,
		CollectedAt:  time.Now().UTC(),
	}, nil
}

// normalizeCandles provides deterministic chronological input to the strategy.
// A duplicate interval is represented once; a confirmed copy wins over an
// unconfirmed copy so delayed REST responses cannot downgrade candle finality.
func normalizeCandles(candles []exchange.Candle) []exchange.Candle {
	if len(candles) < 2 {
		return append([]exchange.Candle(nil), candles...)
	}
	ordered := append([]exchange.Candle(nil), candles...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].OpenTime.Before(ordered[j].OpenTime) })
	result := ordered[:0]
	for _, candle := range ordered {
		last := len(result) - 1
		if last >= 0 && candle.OpenTime.Equal(result[last].OpenTime) {
			if candle.Confirmed || !result[last].Confirmed {
				result[last] = candle
			}
			continue
		}
		result = append(result, candle)
	}
	return result
}
