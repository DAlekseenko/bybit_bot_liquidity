package bybit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// momentumWireBody создаёт фиксированную точную свечу для HTTP fake.
func momentumWireBody(open, server int64) string {
	return fmt.Sprintf(`{"retCode":0,"time":%d,"result":{"category":"linear","symbol":"BTCUSDT","list":[["%d","9007199254740993.00000001","12.34","0.000000000000000001","7.89","0","0"]]}}`, server, open)
}

// TestMomentumPageWire проверяет origin, unsigned query, точность и server clock.
func TestMomentumPageWire(t *testing.T) {
	for host, environment := range map[string]string{"api-testnet.bybit.com": "testnet", "api.bybit.com": "mainnet"} {
		for interval, step := range map[string]int64{"D": 86400000, "60": 3600000} {
			end := map[string]int64{"D": step, "60": math.MaxInt64}[interval]
			calls := 0
			client := newAuthenticatedClient(t, "https://"+host, "fake-key", "fake-secret", &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != host || r.URL.Path != "/v5/market/kline" {
					t.Fatalf("request = %s %s", r.Method, r.URL)
				}
				assertQuery(t, r, map[string]string{"category": "linear", "symbol": "BTCUSDT", "interval": interval,
					"limit": "200", "start": strconv.FormatInt(step-1, 10), "end": strconv.FormatInt(end, 10)})
				for _, name := range []string{"Authorization", "Cookie", "X-BAPI-API-KEY", "X-BAPI-SIGN", "X-BAPI-TIMESTAMP", "X-BAPI-RECV-WINDOW"} {
					if r.Header.Get(name) != "" {
						t.Fatalf("unexpected header %s", name)
					}
				}
				return httpResponse(200, momentumWireBody(step, 2*step-1)), nil
			})})
			jar, jarErr := cookiejar.New(nil)
			if jarErr != nil {
				t.Fatal(jarErr)
			}
			jar.SetCookies(&client.baseURL, []*http.Cookie{{Name: "session", Value: "synthetic"}})
			client.httpClient.Jar = jar
			got, err := client.GetMomentumCandlePage(context.Background(), "BTCUSDT", interval, step, end)
			if err != nil || calls != 1 || client.httpClient.Jar != jar || got.Environment != environment || got.Source != "bybit_v5" || got.Category != "linear" ||
				got.Endpoint != "https://"+host+"/v5/market/kline" || got.ServerTime != time.UnixMilli(2*step-1).UTC() || len(got.Candles) != 1 {
				t.Fatalf("page = %+v, %v, calls %d", got, err, calls)
			}
			candle := got.Candles[0]
			if candle.Confirmed || candle.Symbol != "BTCUSDT" || candle.IntervalCode != interval || candle.OpenTime != time.UnixMilli(step).UTC() {
				t.Fatalf("candle = %+v", candle)
			}
			for i, value := range []model.Decimal{candle.Open, candle.High, candle.Low, candle.Close, candle.Volume, candle.Turnover} {
				want, parseErr := model.ParseAmount(dailyPageRow(step)[i+1])
				if cmp, err := value.Cmp(want); parseErr != nil || err != nil || cmp != 0 {
					t.Fatalf("field %d: %v", i, err)
				}
			}
		}
	}
}

// TestMomentumPageArguments проверяет отказ до HTTP без ложного происхождения.
func TestMomentumPageArguments(t *testing.T) {
	calls := 0
	httpClient := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected request")
	})}
	for _, origin := range []string{"https://api.bybit.com", "http://api.bybit.com", "https://api.bybit.com:443", "https://api.bybit.com/", "https://api.bybit.com/x",
		"https://api.bybit.com?x=1", "https://api.bybit.com?", "https://api.bybit.com#x", "https://api.bybit.com.evil", "https://api.bytick.com"} {
		client := newClient(t, origin, httpClient)
		if origin == "https://api.bybit.com" {
			client.baseURL.User = url.User("synthetic")
		}
		got, err := client.GetMomentumCandlePage(context.Background(), "BTCUSDT", "60", 3600000, 7200000)
		if !errors.Is(err, ErrInvalidArgument) || !reflect.DeepEqual(got, model.CandlePage{}) {
			t.Fatalf("origin %s: %+v, %v", origin, got, err)
		}
	}
	client := newClient(t, "https://api-testnet.bybit.com", httpClient)
	for _, tc := range []struct {
		symbol, interval string
		start, end       int64
	}{{"", "60", 3600000, 7200000}, {"btcusdt", "60", 3600000, 7200000}, {"BTCUSDT", "1", 3600000, 7200000}, {"BTCUSDT", "D", 3600000, 86400000},
		{"BTCUSDT", "60", 0, 7200000}, {"BTCUSDT", "60", -1, 7200000}, {"BTCUSDT", "60", 3600001, 7200000}, {"BTCUSDT", "60", 3600000, 3599999}} {
		got, err := client.GetMomentumCandlePage(context.Background(), tc.symbol, tc.interval, tc.start, tc.end)
		if !errors.Is(err, ErrInvalidArgument) || !reflect.DeepEqual(got, model.CandlePage{}) {
			t.Fatalf("%+v: %+v, %v", tc, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := client.GetMomentumCandlePage(ctx, "BTCUSDT", "60", 3600000, 7200000); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, model.CandlePage{}) {
		t.Fatalf("cancelled: %+v, %v", got, err)
	}
	if calls != 0 {
		t.Fatalf("unexpected HTTP calls %d", calls)
	}
}

// TestMomentumPageFailures проверяет прямую интеграцию ошибок и atomic page shape.
func TestMomentumPageFailures(t *testing.T) {
	valid := momentumWireBody(3600000, 7200000)
	for _, tc := range []struct {
		status int
		body   string
		kind   string
	}{{503, "busy", "http"}, {200, `{"retCode":7,"result":{}}`, "api"}, {200, strings.Repeat("x", maxResponseBytes+1), "large"},
		{302, "redirect", "http"}, {200, `{"retCode":0,"time":7200000,"result":{"category":"linear","symbol":"BTCUSDT","list":[]}}`, "empty"}, {0, "", "deadline"},
		{200, `{"retCode":0,"time":7200000,"result":{"category":"linear","symbol":"BTCUSDT"}}`, "shape"},
		{200, `{"retCode":0,"time":7200000,"result":{"category":"linear","symbol":"BTCUSDT","list":null}}`, "shape"},
		{200, "{", "shape"}, {200, `{"retCode":0,"time":7200000}`, "shape"}, {200, strings.Replace(valid, `"time":7200000`, `"time":0`, 1), "shape"},
		{200, strings.Replace(valid, `"linear"`, `"spot"`, 1), "shape"}, {200, strings.Replace(valid, `"3600000"`, `"10800000"`, 1), "range"},
		{200, strings.Replace(valid, `"3600000"`, `"1"`, 1), "shape"}, {200, valid, "range"}} {
		calls := 0
		originalPolicy := errors.New("original redirect policy")
		httpClient := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return originalPolicy },
			Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if tc.kind == "deadline" {
					return nil, context.DeadlineExceeded
				}
				response := httpResponse(tc.status, tc.body)
				response.Header.Set("Location", "https://api.bybit.com/v5/market/kline")
				return response, nil
			})}
		client := newClient(t, "https://api-testnet.bybit.com", httpClient)
		got, err := client.GetMomentumCandlePage(context.Background(), "BTCUSDT", "60", 7200000, 7200000)
		var httpErr *HTTPError
		var apiErr *APIError
		if calls != 1 || httpClient.Timeout != time.Second || httpClient.CheckRedirect(nil, nil) != originalPolicy ||
			(tc.kind != "empty" && (err == nil || !reflect.DeepEqual(got, model.CandlePage{}))) ||
			(tc.kind == "empty" && (err != nil || got.Candles == nil || len(got.Candles) != 0)) ||
			(tc.kind == "empty" && (got.Environment != "testnet" || got.Source != "bybit_v5" || got.Category != "linear" ||
				got.Endpoint != "https://api-testnet.bybit.com/v5/market/kline" || got.ServerTime != time.UnixMilli(7200000).UTC())) ||
			(tc.kind == "http" && (!errors.As(err, &httpErr) || httpErr.StatusCode != tc.status)) ||
			(tc.kind == "deadline" && !errors.Is(err, context.DeadlineExceeded)) ||
			(tc.kind == "api" && (!errors.As(err, &apiErr) || apiErr.Code != 7)) || (tc.kind == "large" && !errors.Is(err, ErrResponseTooLarge)) {
			t.Fatalf("kind %s: %+v, %v", tc.kind, got, err)
		}
	}
}
