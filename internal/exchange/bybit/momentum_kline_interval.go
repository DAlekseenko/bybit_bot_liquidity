package bybit

import (
	"fmt"
	"strconv"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// parseMomentumKlinePage проверяет одну страницу D/60 без I/O и локальных часов.
func parseMomentumKlinePage(symbol, interval string, serverMs int64, result klineResult) ([]model.Candle, error) {
	var durationMs int64
	switch interval {
	case "D":
		durationMs = 86_400_000
	case "60":
		durationMs = 3_600_000
	default:
		return nil, fmt.Errorf("%w: unsupported momentum kline interval %q", ErrInvalidArgument, interval)
	}
	if err := validateSymbol(symbol); err != nil {
		return nil, err
	}
	if err := validateResultIdentity(result.Category, result.Symbol, symbol); err != nil {
		return nil, err
	}
	if serverMs <= 0 || len(result.List) > 200 {
		return nil, fmt.Errorf("daily kline: invalid server time or page size")
	}
	candles := make([]model.Candle, 0, len(result.List))
	var previousMs int64
	for i, row := range result.List {
		if len(row) != 7 {
			return nil, fmt.Errorf("daily kline row %d: expected seven fields", i)
		}
		openMs, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil || openMs <= 0 || openMs%durationMs != 0 || (i > 0 && openMs >= previousMs) {
			return nil, fmt.Errorf("daily kline row %d: invalid timestamp or order", i)
		}
		var values [6]model.Decimal
		for j := range values {
			parse := model.ParsePrice
			if j >= 4 {
				parse = model.ParseQty
			}
			values[j], err = parse(row[j+1])
			if err != nil {
				return nil, fmt.Errorf("daily kline row %d field %d: %w", i, j+1, err)
			}
		}
		candles = append(candles, model.Candle{
			Symbol: symbol, IntervalCode: interval, OpenTime: time.UnixMilli(openMs).UTC(),
			Open: values[0], High: values[1], Low: values[2], Close: values[3],
			Volume: values[4], Turnover: values[5],
			Confirmed: serverMs >= openMs && serverMs-openMs >= durationMs,
		})
		previousMs = openMs
	}
	return candles, nil
}
