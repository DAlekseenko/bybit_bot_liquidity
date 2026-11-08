package bybit

import (
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// DailyCandle хранит дневную UTC-свечу с точными ценами и объёмами.
type DailyCandle struct {
	Symbol                 string
	OpenTime               time.Time
	Open, High, Low, Close model.Decimal
	Volume, Turnover       model.Decimal
	Confirmed              bool
}

// parseDailyKlinePage проверяет одну страницу без I/O и локальных часов.
func parseDailyKlinePage(symbol string, serverMs int64, result klineResult) ([]DailyCandle, error) {
	parsed, err := parseMomentumKlinePage(symbol, "D", serverMs, result)
	if err != nil {
		return nil, err
	}
	candles := make([]DailyCandle, len(parsed))
	for i, candle := range parsed {
		candles[i] = DailyCandle{
			Symbol: candle.Symbol, OpenTime: candle.OpenTime,
			Open: candle.Open, High: candle.High, Low: candle.Low, Close: candle.Close,
			Volume: candle.Volume, Turnover: candle.Turnover, Confirmed: candle.Confirmed,
		}
	}
	return candles, nil
}
