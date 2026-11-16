// Package collect загружает канонические пакеты Momentum из публичного Testnet.
package collect

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"bybit_bot_liquidity/internal/exchange/bybit"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/signals"
)

const (
	testnetOrigin = "https://api-testnet.bybit.com"
	dayMillis     = 86_400_000
	windowSize    = 31
)

// ReadBatch строит канонический пакет из публичного времени и подтверждённых свечей.
func ReadBatch(ctx context.Context, cfg config.Config, transport http.RoundTripper) (signals.Batch, time.Time, error) {
	values := cfg.Values()
	if values.Mode != "paper" || values.ModelID != "roc30" || values.DataSource.Kind != "bybit_testnet_public" || values.DataSource.Environment != "testnet" {
		return signals.Batch{}, time.Time{}, errors.New("unsupported Momentum collection configuration")
	}
	if err := ctx.Err(); err != nil {
		return signals.Batch{}, time.Time{}, err
	}
	client, err := bybit.NewClient(testnetOrigin, &http.Client{Timeout: 10 * time.Second, Transport: transport})
	if err != nil {
		return signals.Batch{}, time.Time{}, errors.New("cannot initialize public Testnet client")
	}
	symbols := values.Symbols
	serverTime, err := client.GetMomentumTestnetTime(ctx)
	if err != nil {
		return signals.Batch{}, time.Time{}, safeSourceError(ctx, "cannot read public Testnet clock", err)
	}
	cutoff := time.Date(serverTime.UTC().Year(), serverTime.UTC().Month(), serverTime.UTC().Day(), 0, 0, 0, 0, time.UTC)
	if cutoff.Year() > 9999 {
		return signals.Batch{}, time.Time{}, errors.New("cutoff cannot be represented as RFC3339")
	}
	cutoffMS := cutoff.UnixMilli()
	if cutoffMS <= windowSize*dayMillis {
		return signals.Batch{}, time.Time{}, errors.New("cutoff cannot supply a full daily window")
	}
	start, end := cutoffMS-windowSize*dayMillis, cutoffMS-1
	windows := make([]signals.Window, 0, len(symbols))
	for _, symbol := range symbols {
		page, err := client.GetMomentumCandlePage(ctx, symbol, "D", start, end)
		if err != nil {
			return signals.Batch{}, time.Time{}, safeSourceError(ctx, "cannot read public candles for "+symbol, err)
		}
		closes := make([]data.DailyClose, 0, len(page.Candles))
		for _, candle := range page.Candles {
			closes = append(closes, data.DailyClose{Symbol: candle.Symbol, OpenTime: candle.OpenTime, Close: candle.Close, Confirmed: candle.Confirmed})
		}
		sort.Slice(closes, func(i, j int) bool { return closes[i].OpenTime.Before(closes[j].OpenTime) })
		windows = append(windows, signals.Window{Symbol: symbol, Environment: page.Environment, Source: page.Source, Category: page.Category,
			Endpoint: page.Endpoint, StartMS: start, EndMS: end, ServerTime: page.ServerTime, Candles: closes})
	}
	batch, err := signals.NewBatch(cfg, cutoff, windows)
	if contextErr := ctx.Err(); contextErr != nil {
		return signals.Batch{}, time.Time{}, contextErr
	}
	if err != nil {
		return signals.Batch{}, time.Time{}, errors.New("public candles do not form a valid signal batch")
	}
	return batch, cutoff, nil
}

// safeSourceError сохраняет безопасные context sentinels и скрывает vendor body и секреты.
func safeSourceError(ctx context.Context, message string, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New(message)
}
