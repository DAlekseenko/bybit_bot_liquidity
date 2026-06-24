package bybit

import (
	"context"
	"errors"
	"net/http"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

var ErrNotImplemented = errors.New("bybit client is not implemented yet")

// Client отвечает только за низкоуровневое взаимодействие с Bybit V5 API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

func (c *Client) GetCandles(ctx context.Context, symbol string, limit int) ([]exchange.Candle, error) {
	return nil, ErrNotImplemented
}

func (c *Client) GetOpenInterest(ctx context.Context, symbol string) (exchange.OpenInterest, error) {
	return exchange.OpenInterest{}, ErrNotImplemented
}

func (c *Client) PlaceOrder(ctx context.Context, order exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, ErrNotImplemented
}

func (c *Client) GetPosition(ctx context.Context, symbol string) (*exchange.Position, error) {
	return nil, ErrNotImplemented
}

func (c *Client) ClosePosition(ctx context.Context, symbol string) error {
	return ErrNotImplemented
}

var _ exchange.Client = (*Client)(nil)
