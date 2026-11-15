package bybit

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// GetMomentumTestnetTime получает верхнее UTC время публичного Testnet API.
func (c *Client) GetMomentumTestnetTime(ctx context.Context) (time.Time, error) {
	base := c.baseURL
	if base.Scheme != "https" || base.Host != "api-testnet.bybit.com" || base.User != nil || base.Port() != "" || base.Path != "" || base.RawPath != "" || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.Opaque != "" {
		return time.Time{}, fmt.Errorf("get momentum testnet time: %w: unsupported source", ErrInvalidArgument)
	}
	reader := &Client{baseURL: base, httpClient: &http.Client{Transport: c.httpClient.Transport, Timeout: c.httpClient.Timeout, CheckRedirect: rejectMomentumRedirect}}
	if err := ctx.Err(); err != nil {
		return time.Time{}, fmt.Errorf("get momentum testnet time: %w", err)
	}
	stamp, err := reader.get(ctx, "/v5/market/time", url.Values{}, &struct{}{})
	if err != nil {
		return time.Time{}, fmt.Errorf("get momentum testnet time: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, fmt.Errorf("get momentum testnet time: %w", err)
	}
	if stamp <= 0 {
		return time.Time{}, fmt.Errorf("get momentum testnet time: invalid server time")
	}
	result := time.UnixMilli(stamp).UTC()
	if result.Year() > 294276 || result.UnixMilli() != stamp {
		return time.Time{}, fmt.Errorf("get momentum testnet time: invalid server time")
	}
	return result, nil
}
