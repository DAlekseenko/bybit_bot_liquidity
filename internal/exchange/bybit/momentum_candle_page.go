package bybit

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// GetMomentumCandlePage загружает одну неподписанную D/60 страницу с происхождением.
func (c *Client) GetMomentumCandlePage(ctx context.Context, symbol, interval string, startMs, endMs int64) (model.CandlePage, error) {
	var empty model.CandlePage
	if err := validateSymbol(symbol); err != nil {
		return empty, fmt.Errorf("get momentum candle page: %w", err)
	}
	var step int64
	switch interval {
	case "D":
		step = 86_400_000
	case "60":
		step = 3_600_000
	default:
		return empty, fmt.Errorf("get momentum candle page: %w: unsupported interval", ErrInvalidArgument)
	}
	if startMs <= 0 || startMs%step != 0 || endMs < startMs {
		return empty, fmt.Errorf("get momentum candle page: %w: invalid boundaries", ErrInvalidArgument)
	}
	reader, environment, err := c.momentumMarketPageReader()
	if err != nil {
		return empty, fmt.Errorf("get momentum candle page: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return empty, fmt.Errorf("get momentum candle page: %w", err)
	}
	query := url.Values{
		"category": {linearCategory}, "symbol": {symbol}, "interval": {interval}, "limit": {"200"},
		"start": {strconv.FormatInt(startMs-1, 10)}, "end": {strconv.FormatInt(endMs, 10)},
	}
	var result klineResult
	serverMs, err := reader.get(ctx, "/v5/market/kline", query, &result)
	if err != nil {
		return empty, fmt.Errorf("get momentum candle page: %w", err)
	}
	if result.List == nil {
		return empty, fmt.Errorf("get momentum candle page: candle list is missing or null")
	}
	candles, err := parseMomentumKlinePage(symbol, interval, serverMs, result)
	if err != nil {
		return empty, fmt.Errorf("get momentum candle page: %w", err)
	}
	for _, candle := range candles {
		if openMs := candle.OpenTime.UnixMilli(); openMs < startMs || openMs > endMs {
			return empty, fmt.Errorf("get momentum candle page: candle outside requested boundaries")
		}
	}
	return model.CandlePage{
		Environment: environment, Source: "bybit_v5", Category: linearCategory,
		Endpoint:   "https://" + c.baseURL.Host + "/v5/market/kline",
		ServerTime: time.UnixMilli(serverMs).UTC(), Candles: candles,
	}, nil
}

// momentumMarketPageReader допускает канонический origin и копирует только HTTP transport и timeout.
func (c *Client) momentumMarketPageReader() (*Client, string, error) {
	if c.baseURL.Scheme != "https" || c.baseURL.User != nil || c.baseURL.Port() != "" || c.baseURL.Path != "" ||
		c.baseURL.RawPath != "" || c.baseURL.RawQuery != "" || c.baseURL.ForceQuery || c.baseURL.Fragment != "" || c.baseURL.Opaque != "" {
		return nil, "", fmt.Errorf("%w: unsupported source", ErrInvalidArgument)
	}
	var environment string
	switch c.baseURL.Host {
	case "api-testnet.bybit.com":
		environment = "testnet"
	case "api.bybit.com":
		environment = "mainnet"
	default:
		return nil, "", fmt.Errorf("%w: unsupported source", ErrInvalidArgument)
	}
	return &Client{baseURL: c.baseURL, httpClient: &http.Client{
		Transport: c.httpClient.Transport, Timeout: c.httpClient.Timeout,
		CheckRedirect: rejectMomentumRedirect,
	}}, environment, nil
}

// rejectMomentumRedirect запрещает переход к другому источнику публичных данных.
func rejectMomentumRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
