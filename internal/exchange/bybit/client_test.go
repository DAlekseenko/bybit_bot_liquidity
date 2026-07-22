package bybit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

func TestGetCandles(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/proxy/v5/market/kline" {
			t.Errorf("path = %s", r.URL.Path)
		}
		assertQuery(t, r, map[string]string{
			"category": linearCategory,
			"symbol":   "BTCUSDT",
			"interval": oneMinuteInterval,
			"limit":    "2",
		})
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}

		return httpResponse(http.StatusOK, `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {
    "category": "linear",
    "symbol": "BTCUSDT",
    "list": [
      ["120000", "102", "106", "101", "105", "2.5", "260"],
      ["60000", "100", "103", "99", "102", "1.5", "151.5"]
    ]
  },
  "time": 179999
}`), nil
	})}

	client := NewClient("https://api.example.test/proxy/", httpClient)
	candles, err := client.GetCandles(context.Background(), "BTCUSDT", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 2 {
		t.Fatalf("len(candles) = %d, want 2", len(candles))
	}

	first := candles[0]
	if first.Symbol != "BTCUSDT" {
		t.Errorf("first.Symbol = %q", first.Symbol)
	}
	if !first.OpenTime.Equal(time.UnixMilli(60000).UTC()) {
		t.Errorf("first.OpenTime = %s", first.OpenTime)
	}
	if first.Open != 100 || first.High != 103 || first.Low != 99 || first.Close != 102 {
		t.Errorf("unexpected first OHLC: %+v", first)
	}
	if first.Volume != 1.5 || first.Turnover != 151.5 {
		t.Errorf("unexpected first volume/turnover: %+v", first)
	}
	if !first.Confirmed {
		t.Error("older candle must be confirmed")
	}

	second := candles[1]
	if !second.OpenTime.Equal(time.UnixMilli(120000).UTC()) {
		t.Errorf("second.OpenTime = %s", second.OpenTime)
	}
	if second.Confirmed {
		t.Error("current candle must not be confirmed")
	}
}

func TestGetOpenInterest(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v5/market/open-interest" {
			t.Errorf("path = %s", r.URL.Path)
		}
		assertQuery(t, r, map[string]string{
			"category":     linearCategory,
			"symbol":       "BTCUSDT",
			"intervalTime": openInterestInterval,
			"limit":        "1",
		})

		return httpResponse(http.StatusOK, `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {
    "category": "linear",
    "symbol": "BTCUSDT",
    "list": [{"openInterest": "123.45", "timestamp": "180000"}]
  },
  "time": 180100
}`), nil
	})}

	client := NewClient("https://api.example.test", httpClient)
	openInterest, err := client.GetOpenInterest(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if openInterest.Symbol != "BTCUSDT" {
		t.Errorf("Symbol = %q", openInterest.Symbol)
	}
	if openInterest.Value != 123.45 {
		t.Errorf("Value = %f", openInterest.Value)
	}
	if !openInterest.Time.Equal(time.UnixMilli(180000).UTC()) {
		t.Errorf("Time = %s", openInterest.Time)
	}
}

func TestGetCandlesRejectsInvalidArgumentsWithoutRequest(t *testing.T) {
	requests := 0
	client := NewClient("https://example.com", &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return nil, errors.New("unexpected request")
		}),
	})

	tests := []struct {
		name   string
		symbol string
		limit  int
	}{
		{name: "empty symbol", symbol: "", limit: 1},
		{name: "lowercase symbol", symbol: "btcusdt", limit: 1},
		{name: "surrounding whitespace", symbol: " BTCUSDT", limit: 1},
		{name: "zero limit", symbol: "BTCUSDT", limit: 0},
		{name: "limit above maximum", symbol: "BTCUSDT", limit: maxKlineLimit + 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.GetCandles(context.Background(), test.symbol, test.limit)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}

	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestGetOpenInterestRejectsInvalidBaseURL(t *testing.T) {
	client := NewClient("://invalid", nil)
	_, err := client.GetOpenInterest(context.Background(), "BTCUSDT")
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
}

func TestClientReturnsAPIError(t *testing.T) {
	client := clientForResponse(t, http.StatusOK, `{
  "retCode": 10001,
  "retMsg": "invalid request",
  "result": {},
  "time": 180000
}`)

	_, err := client.GetOpenInterest(context.Background(), "BTCUSDT")
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiError.Code != 10001 || apiError.Message != "invalid request" {
		t.Errorf("unexpected API error: %+v", apiError)
	}
}

func TestClientReturnsHTTPError(t *testing.T) {
	client := clientForResponse(t, http.StatusTooManyRequests, "rate limited")

	_, err := client.GetOpenInterest(context.Background(), "BTCUSDT")
	var httpError *HTTPError
	if !errors.As(err, &httpError) {
		t.Fatalf("error = %v, want *HTTPError", err)
	}
	if httpError.StatusCode != http.StatusTooManyRequests || httpError.Body != "rate limited" {
		t.Errorf("unexpected HTTP error: %+v", httpError)
	}
}

func TestClientRejectsMalformedResponses(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "invalid JSON",
			body:    `{`,
			wantErr: "decode response envelope",
		},
		{
			name:    "missing retCode",
			body:    `{"retMsg":"OK","result":{},"time":180000}`,
			wantErr: "retCode is missing",
		},
		{
			name:    "missing result",
			body:    `{"retCode":0,"retMsg":"OK","time":180000}`,
			wantErr: "result is missing",
		},
		{
			name: "invalid numeric value",
			body: `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {
    "category": "linear",
    "symbol": "BTCUSDT",
    "list": [{"openInterest": "NaN", "timestamp": "180000"}]
  },
  "time": 180100
}`,
			wantErr: "invalid openInterest",
		},
		{
			name: "unexpected symbol",
			body: `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {"category": "linear", "symbol": "ETHUSDT", "list": []},
  "time": 180100
}`,
			wantErr: "unexpected symbol",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := clientForResponse(t, http.StatusOK, test.body)
			_, err := client.GetOpenInterest(context.Background(), "BTCUSDT")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestClientRejectsEmptyResults(t *testing.T) {
	client := clientForResponse(t, http.StatusOK, `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {"category": "linear", "symbol": "BTCUSDT", "list": []},
  "time": 180100
}`)

	_, err := client.GetOpenInterest(context.Background(), "BTCUSDT")
	if !errors.Is(err, ErrEmptyResult) {
		t.Fatalf("error = %v, want ErrEmptyResult", err)
	}
}

func TestGetCandlesRejectsMalformedItem(t *testing.T) {
	client := clientForResponse(t, http.StatusOK, `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {
    "category": "linear",
    "symbol": "BTCUSDT",
    "list": [["180000", "100"]]
  },
  "time": 180100
}`)

	_, err := client.GetCandles(context.Background(), "BTCUSDT", 1)
	if err == nil || !strings.Contains(err.Error(), "expected at least 7 fields") {
		t.Fatalf("error = %v", err)
	}
}

func TestClientRejectsOversizedResponse(t *testing.T) {
	client := clientForResponse(t, http.StatusOK, strings.Repeat("x", maxResponseBytes+1))

	_, err := client.GetOpenInterest(context.Background(), "BTCUSDT")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
}

func TestClientPreservesContextCancellation(t *testing.T) {
	client := NewClient("https://example.com", &http.Client{
		Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetOpenInterest(ctx, "BTCUSDT")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestPrivateMethodsRemainNotImplemented(t *testing.T) {
	client := NewClient("https://example.com", nil)

	if _, err := client.PlaceOrder(context.Background(), exchange.OrderRequest{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("PlaceOrder error = %v", err)
	}
	if _, err := client.GetPosition(context.Background(), "BTCUSDT"); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("GetPosition error = %v", err)
	}
	if err := client.ClosePosition(context.Background(), "BTCUSDT"); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("ClosePosition error = %v", err)
	}
}

func assertQuery(t *testing.T, request *http.Request, expected map[string]string) {
	t.Helper()

	query := request.URL.Query()
	if len(query) != len(expected) {
		t.Errorf("query = %v, want %v", query, expected)
	}
	for key, want := range expected {
		if got := query.Get(key); got != want {
			t.Errorf("query[%s] = %q, want %q", key, got, want)
		}
	}
}

func clientForResponse(t *testing.T, status int, body string) *Client {
	t.Helper()

	return NewClient("https://api.example.test", &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return httpResponse(status, body), nil
		}),
	})
}

func httpResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
