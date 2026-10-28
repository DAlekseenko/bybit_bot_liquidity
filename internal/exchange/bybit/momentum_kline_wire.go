package bybit

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// getDailyKlinePage загружает и проверяет одну страницу дневных свечей.
func (c *Client) getDailyKlinePage(ctx context.Context, symbol string, startBoundaryMs, endBoundaryMs int64) ([]DailyCandle, error) {
	if err := validateSymbol(symbol); err != nil {
		return nil, fmt.Errorf("get daily kline page: %w", err)
	}
	if startBoundaryMs <= 0 || endBoundaryMs <= 0 || startBoundaryMs > endBoundaryMs {
		return nil, fmt.Errorf("get daily kline page: %w: invalid boundaries", ErrInvalidArgument)
	}
	query := url.Values{
		"category": {linearCategory}, "symbol": {symbol}, "interval": {"D"}, "limit": {"200"},
		"start": {strconv.FormatInt(startBoundaryMs-1, 10)}, "end": {strconv.FormatInt(endBoundaryMs, 10)},
	}
	var result klineResult
	serverMs, err := c.get(ctx, "/v5/market/kline", query, &result)
	if err != nil {
		return nil, fmt.Errorf("get daily kline page: %w", err)
	}
	candles, err := parseDailyKlinePage(symbol, serverMs, result)
	if err != nil {
		return nil, fmt.Errorf("get daily kline page: %w", err)
	}
	return candles, nil
}
