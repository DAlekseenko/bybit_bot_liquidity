package bybit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	maxKlineLimit        = 1000
	maxResponseBytes     = 2 << 20
	maxErrorBodyBytes    = 4 << 10
	defaultReceiveWindow = "5000"
	unifiedAccountType   = "UNIFIED"
)

var (
	ErrNotImplemented          = errors.New("private bybit client method is not implemented yet")
	ErrInvalidArgument         = errors.New("invalid bybit client argument")
	ErrEmptyResult             = errors.New("bybit returned an empty result")
	ErrResponseTooLarge        = errors.New("bybit response exceeds the size limit")
	ErrUnsupportedPositionMode = errors.New("bybit hedge position mode is not supported")
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
	apiKey     string
	apiSecret  string
	recvWindow string
	now        func() time.Time
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	return &Client{
		baseURL:    strings.TrimSpace(baseURL),
		httpClient: httpClient,
		recvWindow: defaultReceiveWindow,
		now:        time.Now,
	}
}

func NewAuthenticatedClient(baseURL, apiKey, apiSecret string, httpClient *http.Client) *Client {
	client := NewClient(baseURL, httpClient)
	client.apiKey = strings.TrimSpace(apiKey)
	client.apiSecret = strings.TrimSpace(apiSecret)
	return client
}

func (c *Client) String() string {
	return fmt.Sprintf("{baseURL:%q authenticated:%t}", c.baseURL, c.apiKey != "" && c.apiSecret != "")
}

func (c *Client) GoString() string {
	return c.String()
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

// GetOpenInterest получает последнее linear-значение OI для указанного окна.
func (c *Client) GetOpenInterest(ctx context.Context, symbol string, windowMinutes int) (exchange.OpenInterest, error) {
	if err := validateSymbol(symbol); err != nil {
		return exchange.OpenInterest{}, err
	}
	interval, err := openInterestInterval(windowMinutes)
	if err != nil {
		return exchange.OpenInterest{}, err
	}

	query := url.Values{
		"category":     {linearCategory},
		"symbol":       {symbol},
		"intervalTime": {interval},
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

func openInterestInterval(windowMinutes int) (string, error) {
	switch windowMinutes {
	case 5:
		return "5min", nil
	case 15:
		return "15min", nil
	case 30:
		return "30min", nil
	case 60:
		return "1h", nil
	case 240:
		return "4h", nil
	case 1440:
		return "1d", nil
	default:
		return "", fmt.Errorf("%w: unsupported open interest window %d minutes", ErrInvalidArgument, windowMinutes)
	}
}

// GetInstrumentRules получает актуальные ограничения linear-инструмента.
func (c *Client) GetInstrumentRules(ctx context.Context, symbol string) (exchange.InstrumentRules, error) {
	if err := validateSymbol(symbol); err != nil {
		return exchange.InstrumentRules{}, err
	}

	query := url.Values{
		"category": {linearCategory},
		"symbol":   {symbol},
		"limit":    {"1"},
	}

	var result instrumentInfoResult
	if _, err := c.get(ctx, "/v5/market/instruments-info", query, &result); err != nil {
		return exchange.InstrumentRules{}, fmt.Errorf("get instrument rules: %w", err)
	}
	if result.Category != linearCategory {
		return exchange.InstrumentRules{}, fmt.Errorf("get instrument rules: unexpected category %q", result.Category)
	}
	if len(result.List) == 0 {
		return exchange.InstrumentRules{}, fmt.Errorf("get instrument rules: %w", ErrEmptyResult)
	}
	if len(result.List) != 1 {
		return exchange.InstrumentRules{}, fmt.Errorf("get instrument rules: expected one instrument, got %d", len(result.List))
	}

	rules, err := parseInstrumentRules(result.List[0], symbol)
	if err != nil {
		return exchange.InstrumentRules{}, fmt.Errorf("get instrument rules: %w", err)
	}
	return rules, nil
}

// GetLastPrice получает актуальные last/mark/index цены linear-инструмента.
func (c *Client) GetLastPrice(ctx context.Context, symbol string) (exchange.MarketPrice, error) {
	if err := validateSymbol(symbol); err != nil {
		return exchange.MarketPrice{}, err
	}

	query := url.Values{
		"category": {linearCategory},
		"symbol":   {symbol},
	}
	var result tickerResult
	serverTimeMillis, err := c.get(ctx, "/v5/market/tickers", query, &result)
	if err != nil {
		return exchange.MarketPrice{}, fmt.Errorf("get last price: %w", err)
	}
	if result.Category != linearCategory {
		return exchange.MarketPrice{}, fmt.Errorf("get last price: unexpected category %q", result.Category)
	}
	if len(result.List) == 0 {
		return exchange.MarketPrice{}, fmt.Errorf("get last price: %w", ErrEmptyResult)
	}
	if len(result.List) != 1 {
		return exchange.MarketPrice{}, fmt.Errorf("get last price: expected one ticker, got %d", len(result.List))
	}

	price, err := parseMarketPrice(result.List[0], symbol, serverTimeMillis)
	if err != nil {
		return exchange.MarketPrice{}, fmt.Errorf("get last price: %w", err)
	}
	return price, nil
}

// GetAccountInfo получает read-only режимы Unified account.
func (c *Client) GetAccountInfo(ctx context.Context) (exchange.AccountInfo, error) {
	var result accountInfoResult
	if _, err := c.signedGet(ctx, "/v5/account/info", url.Values{}, &result); err != nil {
		return exchange.AccountInfo{}, fmt.Errorf("get account info: %w", err)
	}

	info, err := parseAccountInfo(result)
	if err != nil {
		return exchange.AccountInfo{}, fmt.Errorf("get account info: %w", err)
	}
	return info, nil
}

// GetAccountBalance получает доступный USD-баланс Unified account.
func (c *Client) GetAccountBalance(ctx context.Context) (exchange.AccountBalance, error) {
	query := url.Values{"accountType": {unifiedAccountType}}
	var result walletBalanceResult
	serverTimeMillis, err := c.signedGet(ctx, "/v5/account/wallet-balance", query, &result)
	if err != nil {
		return exchange.AccountBalance{}, fmt.Errorf("get account balance: %w", err)
	}
	if len(result.List) == 0 {
		return exchange.AccountBalance{}, fmt.Errorf("get account balance: %w", ErrEmptyResult)
	}
	if len(result.List) != 1 {
		return exchange.AccountBalance{}, fmt.Errorf("get account balance: expected one account, got %d", len(result.List))
	}

	balance, err := parseAccountBalance(result.List[0], serverTimeMillis)
	if err != nil {
		return exchange.AccountBalance{}, fmt.Errorf("get account balance: %w", err)
	}
	return balance, nil
}

// GetLeverage получает текущую one-way настройку leverage linear-инструмента.
func (c *Client) GetLeverage(ctx context.Context, symbol string) (exchange.LeverageInfo, error) {
	if err := validateSymbol(symbol); err != nil {
		return exchange.LeverageInfo{}, err
	}

	query := url.Values{
		"category": {linearCategory},
		"symbol":   {symbol},
	}
	var result leverageResult
	serverTimeMillis, err := c.signedGet(ctx, "/v5/position/list", query, &result)
	if err != nil {
		return exchange.LeverageInfo{}, fmt.Errorf("get leverage: %w", err)
	}
	if result.Category != linearCategory {
		return exchange.LeverageInfo{}, fmt.Errorf("get leverage: unexpected category %q", result.Category)
	}
	if len(result.List) == 0 {
		return exchange.LeverageInfo{}, fmt.Errorf("get leverage: %w", ErrEmptyResult)
	}
	for _, item := range result.List {
		if item.PositionIndex != nil && (*item.PositionIndex == 1 || *item.PositionIndex == 2) {
			return exchange.LeverageInfo{}, fmt.Errorf("get leverage: %w", ErrUnsupportedPositionMode)
		}
	}
	if len(result.List) != 1 {
		return exchange.LeverageInfo{}, fmt.Errorf("get leverage: expected one one-way leverage, got %d", len(result.List))
	}

	leverage, err := parseLeverage(result.List[0], symbol, serverTimeMillis)
	if err != nil {
		return exchange.LeverageInfo{}, fmt.Errorf("get leverage: %w", err)
	}
	return leverage, nil
}

func (c *Client) PlaceOrder(ctx context.Context, order exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, ErrNotImplemented
}

// GetPosition получает текущую one-way позицию по linear-инструменту.
func (c *Client) GetPosition(ctx context.Context, symbol string) (*exchange.Position, error) {
	if err := validateSymbol(symbol); err != nil {
		return nil, err
	}

	query := url.Values{
		"category": {linearCategory},
		"symbol":   {symbol},
	}
	var result positionResult
	if _, err := c.signedGet(ctx, "/v5/position/list", query, &result); err != nil {
		return nil, fmt.Errorf("get position: %w", err)
	}
	if result.Category != linearCategory {
		return nil, fmt.Errorf("get position: unexpected category %q", result.Category)
	}
	if len(result.List) == 0 {
		return nil, fmt.Errorf("get position: %w", ErrEmptyResult)
	}
	for _, item := range result.List {
		if item.PositionIndex != nil && (*item.PositionIndex == 1 || *item.PositionIndex == 2) {
			return nil, fmt.Errorf("get position: %w", ErrUnsupportedPositionMode)
		}
	}
	if len(result.List) != 1 {
		return nil, fmt.Errorf("get position: expected one one-way position, got %d", len(result.List))
	}

	position, err := parsePosition(result.List[0], symbol)
	if err != nil {
		return nil, fmt.Errorf("get position: %w", err)
	}
	return position, nil
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

type instrumentInfoResult struct {
	Category string               `json:"category"`
	List     []instrumentInfoItem `json:"list"`
}

type instrumentInfoItem struct {
	Symbol         string         `json:"symbol"`
	Status         string         `json:"status"`
	LeverageFilter leverageFilter `json:"leverageFilter"`
	PriceFilter    priceFilter    `json:"priceFilter"`
	LotSizeFilter  lotSizeFilter  `json:"lotSizeFilter"`
}

type leverageFilter struct {
	MinLeverage  string `json:"minLeverage"`
	MaxLeverage  string `json:"maxLeverage"`
	LeverageStep string `json:"leverageStep"`
}

type priceFilter struct {
	MinPrice string `json:"minPrice"`
	MaxPrice string `json:"maxPrice"`
	TickSize string `json:"tickSize"`
}

type lotSizeFilter struct {
	MinNotionalValue  string `json:"minNotionalValue"`
	MaxOrderQty       string `json:"maxOrderQty"`
	MaxMarketOrderQty string `json:"maxMktOrderQty"`
	MinOrderQty       string `json:"minOrderQty"`
	QtyStep           string `json:"qtyStep"`
}

type tickerResult struct {
	Category string       `json:"category"`
	List     []tickerItem `json:"list"`
}

type tickerItem struct {
	Symbol     string `json:"symbol"`
	LastPrice  string `json:"lastPrice"`
	MarkPrice  string `json:"markPrice"`
	IndexPrice string `json:"indexPrice"`
}

type accountInfoResult struct {
	UnifiedMarginStatus int    `json:"unifiedMarginStatus"`
	MarginMode          string `json:"marginMode"`
	UpdatedTime         string `json:"updatedTime"`
}

type walletBalanceResult struct {
	List []walletBalanceItem `json:"list"`
}

type walletBalanceItem struct {
	AccountType           string `json:"accountType"`
	TotalAvailableBalance string `json:"totalAvailableBalance"`
	TotalEquity           string `json:"totalEquity"`
	TotalWalletBalance    string `json:"totalWalletBalance"`
}

type positionResult struct {
	Category string         `json:"category"`
	List     []positionItem `json:"list"`
}

type positionItem struct {
	PositionIndex *int   `json:"positionIdx"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Size          string `json:"size"`
	AveragePrice  string `json:"avgPrice"`
	CreatedTime   string `json:"createdTime"`
}

type leverageResult struct {
	Category string         `json:"category"`
	List     []leverageItem `json:"list"`
}

type leverageItem struct {
	Symbol        string `json:"symbol"`
	Leverage      string `json:"leverage"`
	PositionIndex *int   `json:"positionIdx"`
}

func (c *Client) get(ctx context.Context, path string, query url.Values, result any) (int64, error) {
	return c.getWithHeaders(ctx, path, query, result, nil)
}

func (c *Client) signedGet(ctx context.Context, path string, query url.Values, result any) (int64, error) {
	if c.apiKey == "" || c.apiSecret == "" {
		return 0, fmt.Errorf("%w: authenticated client credentials are required", ErrInvalidArgument)
	}
	timestamp := strconv.FormatInt(c.now().UTC().UnixMilli(), 10)
	queryString := query.Encode()
	signaturePayload := timestamp + c.apiKey + c.recvWindow + queryString
	mac := hmac.New(sha256.New, []byte(c.apiSecret))
	_, _ = mac.Write([]byte(signaturePayload))
	signature := hex.EncodeToString(mac.Sum(nil))

	headers := http.Header{
		"X-BAPI-API-KEY":     {c.apiKey},
		"X-BAPI-TIMESTAMP":   {timestamp},
		"X-BAPI-RECV-WINDOW": {c.recvWindow},
		"X-BAPI-SIGN":        {signature},
	}
	return c.getWithHeaders(ctx, path, query, result, headers)
}

func (c *Client) getWithHeaders(ctx context.Context, path string, query url.Values, result any, headers http.Header) (int64, error) {
	endpoint, err := c.endpoint(path, query)
	if err != nil {
		return 0, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}

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

func parseInstrumentRules(item instrumentInfoItem, expectedSymbol string) (exchange.InstrumentRules, error) {
	if item.Symbol != expectedSymbol {
		return exchange.InstrumentRules{}, fmt.Errorf("unexpected symbol %q", item.Symbol)
	}
	if item.Status == "" {
		return exchange.InstrumentRules{}, errors.New("instrument status is missing")
	}

	minLeverage, err := parseNumber(item.LeverageFilter.MinLeverage, "minLeverage", true)
	if err != nil {
		return exchange.InstrumentRules{}, err
	}
	maxLeverage, err := parseNumber(item.LeverageFilter.MaxLeverage, "maxLeverage", true)
	if err != nil {
		return exchange.InstrumentRules{}, err
	}
	leverageStep, err := parseNumber(item.LeverageFilter.LeverageStep, "leverageStep", true)
	if err != nil {
		return exchange.InstrumentRules{}, err
	}
	tickSize, err := parseNumber(item.PriceFilter.TickSize, "tickSize", true)
	if err != nil {
		return exchange.InstrumentRules{}, err
	}
	minPrice, err := parseNumber(item.PriceFilter.MinPrice, "minPrice", true)
	if err != nil {
		return exchange.InstrumentRules{}, err
	}
	maxPrice, err := parseNumber(item.PriceFilter.MaxPrice, "maxPrice", true)
	if err != nil {
		return exchange.InstrumentRules{}, err
	}
	qtyStep, err := parseNumber(item.LotSizeFilter.QtyStep, "qtyStep", true)
	if err != nil {
		return exchange.InstrumentRules{}, err
	}
	minOrderQty, err := parseNumber(item.LotSizeFilter.MinOrderQty, "minOrderQty", true)
	if err != nil {
		return exchange.InstrumentRules{}, err
	}
	maxOrderQty, err := parseNumber(item.LotSizeFilter.MaxOrderQty, "maxOrderQty", true)
	if err != nil {
		return exchange.InstrumentRules{}, err
	}
	maxMarketOrderQty, err := parseNumber(item.LotSizeFilter.MaxMarketOrderQty, "maxMktOrderQty", true)
	if err != nil {
		return exchange.InstrumentRules{}, err
	}
	minNotionalValue, err := parseNumber(item.LotSizeFilter.MinNotionalValue, "minNotionalValue", true)
	if err != nil {
		return exchange.InstrumentRules{}, err
	}
	if minLeverage > maxLeverage || minPrice > maxPrice ||
		minOrderQty > maxOrderQty || minOrderQty > maxMarketOrderQty {
		return exchange.InstrumentRules{}, errors.New("inconsistent instrument limits")
	}

	return exchange.InstrumentRules{
		Symbol:            item.Symbol,
		Status:            item.Status,
		MinLeverage:       minLeverage,
		MaxLeverage:       maxLeverage,
		LeverageStep:      leverageStep,
		TickSize:          tickSize,
		MinPrice:          minPrice,
		MaxPrice:          maxPrice,
		QtyStep:           qtyStep,
		MinOrderQty:       minOrderQty,
		MaxOrderQty:       maxOrderQty,
		MaxMarketOrderQty: maxMarketOrderQty,
		MinNotionalValue:  minNotionalValue,
	}, nil
}

func parseLeverage(item leverageItem, expectedSymbol string, serverTimeMillis int64) (exchange.LeverageInfo, error) {
	if item.Symbol != expectedSymbol {
		return exchange.LeverageInfo{}, fmt.Errorf("unexpected symbol %q", item.Symbol)
	}
	if item.PositionIndex == nil {
		return exchange.LeverageInfo{}, errors.New("positionIdx is missing")
	}
	switch *item.PositionIndex {
	case 0:
	case 1, 2:
		return exchange.LeverageInfo{}, ErrUnsupportedPositionMode
	default:
		return exchange.LeverageInfo{}, fmt.Errorf("invalid positionIdx %d", *item.PositionIndex)
	}
	if serverTimeMillis <= 0 {
		return exchange.LeverageInfo{}, errors.New("invalid server time")
	}
	value, err := parseNumber(item.Leverage, "leverage", true)
	if err != nil {
		return exchange.LeverageInfo{}, err
	}
	return exchange.LeverageInfo{
		Symbol:   item.Symbol,
		Leverage: value,
		Time:     time.UnixMilli(serverTimeMillis).UTC(),
	}, nil
}

func parseMarketPrice(item tickerItem, expectedSymbol string, serverTimeMillis int64) (exchange.MarketPrice, error) {
	if item.Symbol != expectedSymbol {
		return exchange.MarketPrice{}, fmt.Errorf("unexpected symbol %q", item.Symbol)
	}
	if serverTimeMillis <= 0 {
		return exchange.MarketPrice{}, errors.New("invalid server time")
	}
	lastPrice, err := parseNumber(item.LastPrice, "lastPrice", true)
	if err != nil {
		return exchange.MarketPrice{}, err
	}
	markPrice, err := parseNumber(item.MarkPrice, "markPrice", true)
	if err != nil {
		return exchange.MarketPrice{}, err
	}
	indexPrice, err := parseNumber(item.IndexPrice, "indexPrice", true)
	if err != nil {
		return exchange.MarketPrice{}, err
	}
	return exchange.MarketPrice{
		Symbol:     item.Symbol,
		LastPrice:  lastPrice,
		MarkPrice:  markPrice,
		IndexPrice: indexPrice,
		Time:       time.UnixMilli(serverTimeMillis).UTC(),
	}, nil
}

func parseAccountInfo(result accountInfoResult) (exchange.AccountInfo, error) {
	if result.UnifiedMarginStatus <= 0 {
		return exchange.AccountInfo{}, fmt.Errorf("invalid unifiedMarginStatus %d", result.UnifiedMarginStatus)
	}

	marginMode := exchange.MarginMode(result.MarginMode)
	switch marginMode {
	case exchange.MarginModeIsolated, exchange.MarginModeRegular, exchange.MarginModePortfolio:
	default:
		return exchange.AccountInfo{}, fmt.Errorf("unsupported marginMode %q", result.MarginMode)
	}
	updatedAt, err := parseTimestamp(result.UpdatedTime, "updatedTime")
	if err != nil {
		return exchange.AccountInfo{}, err
	}

	return exchange.AccountInfo{
		UnifiedMarginStatus: result.UnifiedMarginStatus,
		MarginMode:          marginMode,
		UpdatedAt:           updatedAt,
	}, nil
}

func parseAccountBalance(item walletBalanceItem, serverTimeMillis int64) (exchange.AccountBalance, error) {
	if item.AccountType != unifiedAccountType {
		return exchange.AccountBalance{}, fmt.Errorf("unexpected account type %q", item.AccountType)
	}
	if serverTimeMillis <= 0 {
		return exchange.AccountBalance{}, errors.New("invalid server time")
	}
	available, err := parseNumber(item.TotalAvailableBalance, "totalAvailableBalance", false)
	if err != nil {
		return exchange.AccountBalance{}, err
	}
	equity, err := parseNumber(item.TotalEquity, "totalEquity", false)
	if err != nil {
		return exchange.AccountBalance{}, err
	}
	wallet, err := parseNumber(item.TotalWalletBalance, "totalWalletBalance", false)
	if err != nil {
		return exchange.AccountBalance{}, err
	}
	return exchange.AccountBalance{
		AvailableUSD: available,
		EquityUSD:    equity,
		WalletUSD:    wallet,
		Time:         time.UnixMilli(serverTimeMillis).UTC(),
	}, nil
}

func parsePosition(item positionItem, expectedSymbol string) (*exchange.Position, error) {
	if item.Symbol != expectedSymbol {
		return nil, fmt.Errorf("unexpected symbol %q", item.Symbol)
	}
	if item.PositionIndex == nil {
		return nil, errors.New("positionIdx is missing")
	}
	switch *item.PositionIndex {
	case 0:
	case 1, 2:
		return nil, ErrUnsupportedPositionMode
	default:
		return nil, fmt.Errorf("invalid positionIdx %d", *item.PositionIndex)
	}

	size, err := parseNumber(item.Size, "size", false)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		if item.Side != "" {
			return nil, fmt.Errorf("empty position has unexpected side %q", item.Side)
		}
		return nil, nil
	}

	var side exchange.Side
	switch item.Side {
	case "Buy":
		side = exchange.SideLong
	case "Sell":
		side = exchange.SideShort
	default:
		return nil, fmt.Errorf("unsupported position side %q", item.Side)
	}
	averagePrice, err := parseNumber(item.AveragePrice, "avgPrice", true)
	if err != nil {
		return nil, err
	}
	openedAt, err := parseTimestamp(item.CreatedTime, "createdTime")
	if err != nil {
		return nil, err
	}

	return &exchange.Position{
		Symbol:     item.Symbol,
		Side:       side,
		Qty:        size,
		EntryPrice: averagePrice,
		OpenedAt:   openedAt,
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
