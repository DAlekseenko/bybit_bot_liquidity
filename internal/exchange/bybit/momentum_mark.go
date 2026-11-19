package bybit

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

const markPriceKlinePath = "/v5/market/mark-price-kline"

var markPriceSymbolPattern = regexp.MustCompile(`^[A-Z0-9]+USDT$`)

// GetMomentumMarkPricePage загружает одну неподписанную страницу mark-price D/60.
func (c *Client) GetMomentumMarkPricePage(ctx context.Context, symbol, interval string, startMs, endMs int64) (model.MarkPricePage, error) {
	var empty model.MarkPricePage
	var step int64
	switch interval {
	case "D":
		step = 86_400_000
	case "60":
		step = 3_600_000
	default:
		return empty, fmt.Errorf("get momentum mark price page: %w: unsupported interval", ErrInvalidArgument)
	}
	if !markPriceSymbolPattern.MatchString(symbol) || startMs <= 0 || startMs%step != 0 || endMs < startMs {
		return empty, fmt.Errorf("get momentum mark price page: %w: invalid symbol or boundaries", ErrInvalidArgument)
	}
	reader, environment, err := c.momentumMarketPageReader()
	if err != nil {
		return empty, fmt.Errorf("get momentum mark price page: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return empty, fmt.Errorf("get momentum mark price page: %w", err)
	}
	query := url.Values{
		"category": {linearCategory}, "symbol": {symbol}, "interval": {interval}, "limit": {"200"},
		"start": {strconv.FormatInt(startMs, 10)}, "end": {strconv.FormatInt(endMs, 10)},
	}
	var result klineResult
	serverMs, err := reader.get(ctx, markPriceKlinePath, query, &result)
	if err != nil {
		return empty, fmt.Errorf("get momentum mark price page: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return empty, fmt.Errorf("get momentum mark price page: %w", err)
	}
	if serverMs <= 0 || result.Category != linearCategory || result.Symbol != symbol || result.List == nil || len(result.List) > 200 {
		return empty, fmt.Errorf("get momentum mark price page: invalid result metadata")
	}
	candles := make([]model.MarkPriceCandle, 0, len(result.List))
	var previous int64
	for i, row := range result.List {
		if len(row) != 5 {
			return empty, fmt.Errorf("get momentum mark price page: expected five fields")
		}
		openMs, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil || strconv.FormatInt(openMs, 10) != row[0] || openMs < startMs || openMs > endMs ||
			openMs > serverMs || openMs%step != 0 || (i > 0 && openMs >= previous) {
			return empty, fmt.Errorf("get momentum mark price page: invalid timestamp or order")
		}
		candle, err := parseMarkOHLC(row[1:])
		if err != nil {
			return empty, err
		}
		candle.OpenTime = time.UnixMilli(openMs).UTC()
		candle.Confirmed = serverMs-openMs >= step
		candles = append(candles, candle)
		previous = openMs
	}
	return model.MarkPricePage{
		Environment: environment, Source: "bybit_v5", Category: linearCategory, Endpoint: "https://" + c.baseURL.Host + markPriceKlinePath,
		Symbol: symbol, IntervalCode: interval, StartTime: time.UnixMilli(startMs).UTC(), EndTime: time.UnixMilli(endMs).UTC(),
		ServerTime: time.UnixMilli(serverMs).UTC(), Candles: candles,
	}, nil
}

// parseMarkOHLC декодирует ровно четыре точные цены и проверяет границы свечи.
func parseMarkOHLC(raw []string) (model.MarkPriceCandle, error) {
	var prices [4]model.Decimal
	for i, value := range raw {
		price, err := model.ParsePrice(value)
		if err != nil {
			return model.MarkPriceCandle{}, fmt.Errorf("get momentum mark price page: invalid mark price")
		}
		prices[i] = price
	}
	for _, value := range [...]model.Decimal{prices[0], prices[3]} {
		high, _ := prices[1].Cmp(value)
		low, _ := prices[2].Cmp(value)
		if high < 0 || low > 0 {
			return model.MarkPriceCandle{}, fmt.Errorf("get momentum mark price page: invalid OHLC bounds")
		}
	}
	return model.MarkPriceCandle{Open: prices[0], High: prices[1], Low: prices[2], Close: prices[3]}, nil
}
