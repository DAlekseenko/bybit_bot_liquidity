package marketdata

import (
	"context"
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
}

func NewCollector(exchange Exchange, symbol string, oiWindowMinutes int) *Collector {
	return &Collector{exchange: exchange, symbol: symbol, oiWindowMinutes: oiWindowMinutes}
}

func (c *Collector) Snapshot(ctx context.Context, candleLimit int, liquidations []exchange.Liquidation) (Snapshot, error) {
	candles, err := c.exchange.GetCandles(ctx, c.symbol, candleLimit)
	if err != nil {
		return Snapshot{}, err
	}

	oi, err := c.exchange.GetOpenInterest(ctx, c.symbol, c.oiWindowMinutes)
	if err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		Candles:      candles,
		OpenInterest: oi,
		Liquidations: liquidations,
		CollectedAt:  time.Now().UTC(),
	}, nil
}
