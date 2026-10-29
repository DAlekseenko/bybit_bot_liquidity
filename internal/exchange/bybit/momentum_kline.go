package bybit

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// GetDailyCandles загружает уникальные дневные свечи полуоткрытого UTC-диапазона.
func (c *Client) GetDailyCandles(ctx context.Context, symbol string, start, end time.Time) ([]DailyCandle, error) {
	if err := validateSymbol(symbol); err != nil {
		return nil, fmt.Errorf("get daily candles: %w", err)
	}
	startMs, endMs, err := dailyRangeBounds(start, end)
	if err != nil {
		return nil, fmt.Errorf("get daily candles: %w", err)
	}
	var candles []DailyCandle
	cursor := endMs - 1
	var previousOldest int64
	for page := 0; page < 20; page++ {
		batch, err := c.getDailyKlinePage(ctx, symbol, startMs, cursor)
		if err != nil {
			return nil, fmt.Errorf("get daily candles page %d: %w", page+1, err)
		}
		if len(batch) == 0 {
			break
		}
		if page > 0 && batch[0].OpenTime.UnixMilli() >= previousOldest {
			return nil, fmt.Errorf("get daily candles: overlapping or nonprogressing page")
		}
		for _, candle := range batch {
			openMs := candle.OpenTime.UnixMilli()
			if openMs >= startMs && openMs < endMs {
				candles = append(candles, candle)
			}
		}
		oldest := batch[len(batch)-1].OpenTime.UnixMilli()
		if oldest <= startMs {
			break
		}
		if page == 19 {
			return nil, fmt.Errorf("get daily candles: exceeded 20 pages")
		}
		previousOldest = oldest
		cursor = oldest - 1
	}
	if len(candles) == 0 {
		return nil, fmt.Errorf("get daily candles: %w", ErrEmptyResult)
	}
	slices.Reverse(candles)
	return candles, nil
}
