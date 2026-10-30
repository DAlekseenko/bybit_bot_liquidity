// Package planner вычисляет чистые флаги стратегии Momentum.
package planner

import (
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
)

// ROC30 вычисляет активность LONG по последним 31 общим дневным свечам.
func ROC30(symbols []string, series map[string][]data.DailyClose, serverTime time.Time) (map[string]bool, error) {
	windows, err := data.CommonDailyWindow(symbols, series, serverTime)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		window := windows[symbol]
		cmp, err := window[len(window)-1].Close.Cmp(window[0].Close)
		if err != nil {
			return nil, err
		}
		active[symbol] = cmp > 0
	}
	return active, nil
}
