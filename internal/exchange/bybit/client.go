package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

const (
	linearCategory       = "linear"
	oneMinuteInterval    = "1"
	openInterestInterval = "5min"
	maxKlineLimit        = 1000
	maxResponseBytes     = 2 << 20
	maxErrorBodyBytes    = 4 << 10
)

var (
	ErrNotImplemented   = errors.New("private bybit client method is not implemented yet")
	ErrInvalidArgument  = errors.New("invalid bybit client argument")
	ErrEmptyResult      = errors.New("bybit returned an empty result")
	ErrResponseTooLarge = errors.New("bybit response exceeds the size limit")
)

// APIError описывает успешный HTTP-ответ с ненулевым retCode Bybit.
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("bybit API error: retCode=%d", e.Code)
	}

	return fmt.Sprintf("bybit API error: retCode=%d retMsg=%q", e.Code, e.Message)
}

// HTTPError описывает ответ Bybit за пределами диапазона 2xx.
type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("bybit HTTP error: %s", e.Status)
	}

	return fmt.Sprintf("bybit HTTP error: %s: %s", e.Status, e.Body)
}

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
		baseURL:    strings.TrimSpace(baseURL),
		httpClient: httpClient,
	}
}

// GetCandles получает linear-свечи 1m и возвращает их по возрастанию OpenTime.
func (c *Client) GetCandles(ctx context.Context, symbol string, limit int) ([]exchange.Candle, error) {
	if err := validateSymbol(symbol); err != nil {
		return nil, err
	}
	if limit < 1 || limit > maxKlineLimit {
		return nil, fmt.Errorf("%w: candle limit must be between 1 and %d", ErrInvalidArgument, maxKlineLimit)
	}

	query := url.Values{
		"category": {linearCategory},
		"symbol":   {symbol},
		"interval": {oneMinuteInterval},
		"limit":    {strconv.Itoa(limit)},
	}

	var result klineResult
	serverTimeMillis, err := c.get(ctx, "/v5/market/kline", query, &result)
	if err != nil {
		return nil, fmt.Errorf("get candles: %w", err)
	}
	if serverTimeMillis <= 0 {
		return nil, errors.New("get candles: bybit response has invalid server time")
	}
	if err := validateResultIdentity(result.Category, result.Symbol, symbol); err != nil {
		return nil, fmt.Errorf("get candles: %w", err)
	}
	if len(result.List) == 0 {
		return nil, fmt.Errorf("get candles: %w", ErrEmptyResult)
	}

	candles := make([]exchange.Candle, 0, len(result.List))
	for index, values := range result.List {
		candle, err := parseCandle(symbol, values, serverTimeMillis)
		if err != nil {
			return nil, fmt.Errorf("get candles: parse item %d: %w", index, err)
		}
		candles = append(candles, candle)
	}

	sort.Slice(candles, func(i, j int) bool {
		return candles[i].OpenTime.Before(candles[j].OpenTime)
	})

	return candles, nil
}

// GetOpenInterest получает последнее linear-значение OI с интервалом 5min.
func (c *Client) GetOpenInterest(ctx context.Context, symbol string) (exchange.OpenInterest, error) {
	if err := validateSymbol(symbol); err != nil {
		return exchange.OpenInterest{}, err
	}

	query := url.Values{
		"category":     {linearCategory},
		"symbol":       {symbol},
		"intervalTime": {openInterestInterval},
		"limit":        {"1"},
	}

	var result openInterestResult
	if _, err := c.get(ctx, "/v5/market/open-interest", query, &result); err != nil {
		return exchange.OpenInterest{}, fmt.Errorf("get open interest: %w", err)
	}
	if err := validateResultIdentity(result.Category, result.Symbol, symbol); err != nil {
		return exchange.OpenInterest{}, fmt.Errorf("get open interest: %w", err)
	}
	if len(result.List) == 0 {
		return exchange.OpenInterest{}, fmt.Errorf("get open interest: %w", ErrEmptyResult)
	}

	latest, err := parseOpenInterest(symbol, result.List[0])
	if err != nil {
		return exchange.OpenInterest{}, fmt.Errorf("get open interest: parse item 0: %w", err)
	}

	return latest, nil
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

type responseEnvelope struct {
	RetCode *int            `json:"retCode"`
	RetMsg  string          `json:"retMsg"`
	Result  json.RawMessage `json:"result"`
	Time    int64           `json:"time"`
}

type klineResult struct {
	Category string     `json:"category"`
	Symbol   string     `json:"symbol"`
	List     [][]string `json:"list"`
}

type openInterestResult struct {
	Category string             `json:"category"`
	Symbol   string             `json:"symbol"`
	List     []openInterestItem `json:"list"`
}

type openInterestItem struct {
	OpenInterest string `json:"openInterest"`
	Timestamp    string `json:"timestamp"`
}

func (c *Client) get(ctx context.Context, path string, query url.Values, result any) (int64, error) {
	endpoint, err := c.endpoint(path, query)
	if err != nil {
		return 0, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, fmt.Errorf("request %s: %w", path, err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		return 0, &HTTPError{
			StatusCode: response.StatusCode,
			Status:     response.Status,
			Body:       strings.TrimSpace(string(body)),
		}
	}

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return 0, ErrResponseTooLarge
	}

	var envelope responseEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return 0, fmt.Errorf("decode response envelope: %w", err)
	}
	if envelope.RetCode == nil {
		return 0, errors.New("decode response envelope: retCode is missing")
	}
	if *envelope.RetCode != 0 {
		return 0, &APIError{Code: *envelope.RetCode, Message: envelope.RetMsg}
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return 0, errors.New("decode response envelope: result is missing")
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return 0, fmt.Errorf("decode response result: %w", err)
	}

	return envelope.Time, nil
}

func (c *Client) endpoint(path string, query url.Values) (string, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("%w: parse base URL: %v", ErrInvalidArgument, err)
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return "", fmt.Errorf("%w: base URL must be an absolute HTTP(S) URL", ErrInvalidArgument)
	}
	if base.User != nil {
		return "", fmt.Errorf("%w: base URL must not contain credentials", ErrInvalidArgument)
	}

	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawPath = ""
	base.RawQuery = query.Encode()
	base.Fragment = ""

	return base.String(), nil
}

func validateSymbol(symbol string) error {
	if symbol == "" {
		return fmt.Errorf("%w: symbol is required", ErrInvalidArgument)
	}
	if strings.TrimSpace(symbol) != symbol || strings.ToUpper(symbol) != symbol {
		return fmt.Errorf("%w: symbol must be uppercase without surrounding whitespace", ErrInvalidArgument)
	}

	return nil
}

func validateResultIdentity(category, responseSymbol, requestedSymbol string) error {
	if category != linearCategory {
		return fmt.Errorf("unexpected category %q", category)
	}
	if responseSymbol != requestedSymbol {
		return fmt.Errorf("unexpected symbol %q", responseSymbol)
	}

	return nil
}

func parseCandle(symbol string, values []string, serverTimeMillis int64) (exchange.Candle, error) {
	if len(values) < 7 {
		return exchange.Candle{}, fmt.Errorf("expected at least 7 fields, got %d", len(values))
	}

	openTime, err := parseTimestamp(values[0], "startTime")
	if err != nil {
		return exchange.Candle{}, err
	}
	open, err := parseNumber(values[1], "open", true)
	if err != nil {
		return exchange.Candle{}, err
	}
	high, err := parseNumber(values[2], "high", true)
	if err != nil {
		return exchange.Candle{}, err
	}
	low, err := parseNumber(values[3], "low", true)
	if err != nil {
		return exchange.Candle{}, err
	}
	closePrice, err := parseNumber(values[4], "close", true)
	if err != nil {
		return exchange.Candle{}, err
	}
	volume, err := parseNumber(values[5], "volume", false)
	if err != nil {
		return exchange.Candle{}, err
	}
	turnover, err := parseNumber(values[6], "turnover", false)
	if err != nil {
		return exchange.Candle{}, err
	}

	return exchange.Candle{
		Symbol:    symbol,
		OpenTime:  openTime,
		Open:      open,
		High:      high,
		Low:       low,
		Close:     closePrice,
		Volume:    volume,
		Turnover:  turnover,
		Confirmed: openTime.Add(time.Minute).UnixMilli() <= serverTimeMillis,
	}, nil
}

func parseOpenInterest(symbol string, item openInterestItem) (exchange.OpenInterest, error) {
	value, err := parseNumber(item.OpenInterest, "openInterest", false)
	if err != nil {
		return exchange.OpenInterest{}, err
	}
	timestamp, err := parseTimestamp(item.Timestamp, "timestamp")
	if err != nil {
		return exchange.OpenInterest{}, err
	}

	return exchange.OpenInterest{
		Symbol: symbol,
		Time:   timestamp,
		Value:  value,
	}, nil
}

func parseTimestamp(raw, field string) (time.Time, error) {
	milliseconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || milliseconds <= 0 {
		return time.Time{}, fmt.Errorf("invalid %s %q", field, raw)
	}

	return time.UnixMilli(milliseconds).UTC(), nil
}

func parseNumber(raw, field string, positive bool) (float64, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("invalid %s %q", field, raw)
	}
	if (positive && value <= 0) || (!positive && value < 0) {
		return 0, fmt.Errorf("invalid %s %q", field, raw)
	}

	return value, nil
}

var _ exchange.Client = (*Client)(nil)
