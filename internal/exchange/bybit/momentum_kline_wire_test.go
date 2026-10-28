package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// TestGetDailyKlinePageQuery проверяет точный запрос, prefix и границы int64.
func TestGetDailyKlinePageQuery(t *testing.T) {
	for _, tc := range []struct{ start, end int64 }{{86400000, 86400000}, {1, 1}, {math.MaxInt64, math.MaxInt64}} {
		client := newClient(t, "https://example.test/base", &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.Path != "/base/v5/market/kline" {
				t.Fatalf("request = %s %s", r.Method, r.URL)
			}
			assertQuery(t, r, map[string]string{
				"category": "linear", "symbol": "BTCUSDT", "interval": "D", "limit": "200",
				"start": strconv.FormatInt(tc.start-1, 10), "end": strconv.FormatInt(tc.end, 10),
			})
			return httpResponse(200, `{"retCode":0,"time":172800000,"result":{"category":"linear","symbol":"BTCUSDT","list":[["86400000","9007199254740993.00000001","2","1","2","0","0"]]}}`), nil
		})})
		got, err := client.getDailyKlinePage(context.Background(), "BTCUSDT", tc.start, tc.end)
		if err != nil || len(got) != 1 {
			t.Fatalf("page = %v, %v", got, err)
		}
		wantOpen, parseErr := model.ParsePrice("9007199254740993.00000001")
		cmp, cmpErr := got[0].Open.Cmp(wantOpen)
		if !got[0].Confirmed || got[0].Symbol != "BTCUSDT" ||
			!got[0].OpenTime.Equal(time.UnixMilli(86400000).UTC()) || parseErr != nil || cmpErr != nil || cmp != 0 {
			t.Fatalf("page = %v, %v", got, err)
		}
	}
	client := newClient(t, "https://example.test", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return httpResponse(200, `{"retCode":0,"time":86400000,"result":{"category":"linear","symbol":"BTCUSDT","list":[]}}`), nil
	})})
	got, err := client.getDailyKlinePage(context.Background(), "BTCUSDT", 1, 1)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty = %#v, %v", got, err)
	}
}

// TestGetDailyKlinePageRejectsArguments проверяет отказ до transport.
func TestGetDailyKlinePageRejectsArguments(t *testing.T) {
	calls := 0
	client := newClient(t, "https://example.test", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected") })})
	for _, tc := range []struct {
		symbol     string
		start, end int64
	}{{"", 1, 1}, {"btcusdt", 1, 1}, {" BTCUSDT", 1, 1}, {"BTCUSDT ", 1, 1}, {"BTCUSDT", 0, 1}, {"BTCUSDT", -1, 1}, {"BTCUSDT", 1, 0}, {"BTCUSDT", 1, -1}, {"BTCUSDT", 2, 1}} {
		got, err := client.getDailyKlinePage(context.Background(), tc.symbol, tc.start, tc.end)
		if got != nil || !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%+v: %v, %v", tc, got, err)
		}
	}
	if calls != 0 {
		t.Fatalf("transport calls = %d", calls)
	}
}

// TestGetDailyKlinePageErrors проверяет envelope, parser, transport и typed errors.
func TestGetDailyKlinePageErrors(t *testing.T) {
	bodies := []string{
		"{",
		"{\"time\":86400000,\"result\":{}}",
		"{\"retCode\":0,\"time\":86400000}",
		"{\"retCode\":0,\"time\":86400000,\"result\":null}",
		"{\"retCode\":0,\"time\":86400000,\"result\":[]}",
		"{\"retCode\":0,\"time\":86400000,\"result\":{\"category\":\"linear\",\"symbol\":\"BTCUSDT\",\"list\":[[1]]}}",
		"{\"retCode\":0,\"time\":0,\"result\":{\"category\":\"linear\",\"symbol\":\"BTCUSDT\",\"list\":[]}}",
		"{\"retCode\":0,\"time\":-1,\"result\":{\"category\":\"linear\",\"symbol\":\"BTCUSDT\",\"list\":[]}}",
		"{\"retCode\":0,\"time\":\"bad\",\"result\":{\"category\":\"linear\",\"symbol\":\"BTCUSDT\",\"list\":[]}}",
		"{\"retCode\":0,\"result\":{\"category\":\"linear\",\"symbol\":\"BTCUSDT\",\"list\":[]}}",
		"{\"retCode\":0,\"time\":86400000,\"result\":{\"symbol\":\"BTCUSDT\",\"list\":[]}}",
		"{\"retCode\":0,\"time\":86400000,\"result\":{\"category\":\"linear\",\"list\":[]}}",
		"{\"retCode\":0,\"time\":86400000,\"result\":{\"category\":\"spot\",\"symbol\":\"BTCUSDT\",\"list\":[]}}",
		"{\"retCode\":0,\"time\":86400000,\"result\":{\"category\":\"linear\",\"symbol\":\"ETHUSDT\",\"list\":[]}}",
		"{\"retCode\":0,\"time\":86400000,\"result\":{\"category\":\"linear\",\"symbol\":\"BTCUSDT\",\"list\":[[\"86400000\",1,\"2\",\"1\",\"2\",\"0\",\"0\"]]}}",
		"{\"retCode\":0,\"time\":86400000,\"result\":{\"category\":\"linear\",\"symbol\":\"BTCUSDT\",\"list\":[[\"86400000\",\"1\",\"2\",\"1\",\"2\",\"0\",\"-1\"]]}}",
	}
	for index, body := range bodies {
		t.Run(body, func(t *testing.T) {
			c := newClient(t, "https://example.test", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return httpResponse(200, body), nil })})
			got, err := c.getDailyKlinePage(context.Background(), "BTCUSDT", 1, 1)
			if err == nil || got != nil {
				t.Fatalf("%v, %v", got, err)
			}
			var syntaxErr *json.SyntaxError
			var typeErr *json.UnmarshalTypeError
			if (index == 0 && !errors.As(err, &syntaxErr)) || (index == 8 && !errors.As(err, &typeErr)) {
				t.Fatalf("JSON decode error = %v", err)
			}
		})
	}
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{{200, `{"retCode":7,"retMsg":"bad","time":1,"result":{}}`, &APIError{}}, {503, "busy", &HTTPError{}}, {200, strings.Repeat("x", maxResponseBytes+1), ErrResponseTooLarge}} {
		c := newClient(t, "https://example.test", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return httpResponse(tc.status, tc.body), nil })})
		got, err := c.getDailyKlinePage(context.Background(), "BTCUSDT", 1, 1)
		if err == nil || got != nil {
			t.Fatalf("result = %v, %v", got, err)
		}
		switch want := tc.want.(type) {
		case *APIError:
			var actual *APIError
			if !errors.As(err, &actual) || actual.Code != 7 {
				t.Fatalf("API error = %v", err)
			}
		case *HTTPError:
			var actual *HTTPError
			if !errors.As(err, &actual) || actual.StatusCode != 503 {
				t.Fatalf("HTTP error = %v", err)
			}
		default:
			if !errors.Is(err, want) {
				t.Fatalf("error = %v", err)
			}
		}
	}
	transportErr := errors.New("transport")
	c := newClient(t, "https://example.test", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, transportErr })})
	_, err := c.getDailyKlinePage(context.Background(), "BTCUSDT", 1, 1)
	if !errors.Is(err, transportErr) {
		t.Fatalf("transport = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c = newClient(t, "https://example.test", &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})})
	_, err = c.getDailyKlinePage(ctx, "BTCUSDT", 1, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	c = newClient(t, "https://example.test", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })})
	_, err = c.getDailyKlinePage(context.Background(), "BTCUSDT", 1, 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline = %v", err)
	}
}
