package bybit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

const fundingTestEvent = `{"symbol":"BTCUSDT","fundingRate":"0","fundingRateTimestamp":"101"}`

// fundingTestBody создаёт envelope с указанными событиями.
func fundingTestBody(list string) string {
	return `{"retCode":0,"time":999,"result":{"category":"linear","list":[` + list + `]}}`
}

// TestMomentumFundingPageWire проверяет точный запрос, происхождение и signed ставки.
func TestMomentumFundingPageWire(t *testing.T) {
	for host, environment := range map[string]string{"api-testnet.bybit.com": "testnet", "api.bybit.com": "mainnet"} {
		calls := 0
		httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != host || r.URL.Path != fundingHistoryPath {
				t.Fatalf("request: %s %s", r.Method, r.URL)
			}
			assertQuery(t, r, map[string]string{"category": "linear", "symbol": "BTCUSDT", "limit": "200", "startTime": "101", "endTime": "701"})
			for _, name := range []string{"Authorization", "Cookie", "X-BAPI-API-KEY", "X-BAPI-SIGN", "X-BAPI-TIMESTAMP", "X-BAPI-RECV-WINDOW"} {
				if r.Header.Get(name) != "" {
					t.Fatalf("unexpected header %q", name)
				}
			}
			return httpResponse(200, fundingTestBody(fundingTestEvent+`,{"symbol":"BTCUSDT","fundingRate":"9007199254740993.123456789012345678","fundingRateTimestamp":"701"},
				{"symbol":"BTCUSDT","fundingRate":"-0.0000000000000000001","fundingRateTimestamp":"203"}`)), nil
		})}
		c := newAuthenticatedClient(t, "https://"+host, "fake-key", "fake-secret", httpClient)
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		jar.SetCookies(&c.baseURL, []*http.Cookie{{Name: "session", Value: "synthetic"}})
		httpClient.Jar = jar
		page, err := c.GetMomentumFundingPage(context.Background(), "BTCUSDT", 101, 701)
		if err != nil || calls != 1 || httpClient.Jar != jar || page.Environment != environment || page.Source != "bybit_v5" ||
			page.Category != "linear" || page.Symbol != "BTCUSDT" || page.Endpoint != "https://"+host+fundingHistoryPath ||
			page.StartTime != time.UnixMilli(101).UTC() || page.EndTime != time.UnixMilli(701).UTC() || page.ServerTime != time.UnixMilli(999).UTC() || len(page.Events) != 3 {
			t.Fatalf("page: %+v, err=%v, calls=%d", page, err, calls)
		}
		for i, stamp := range []int64{701, 203, 101} {
			rate, err := page.Events[i].Rate.WireDecimal()
			want := []string{"9007199254740993.123456789012345678", "-0.0000000000000000001", "0"}[i]
			if err != nil || page.Events[i].FundingTime != time.UnixMilli(stamp).UTC() || rate != want {
				t.Fatalf("event %d: rate=%s, err=%v", i, rate, err)
			}
		}
	}
}

// TestMomentumFundingPageArguments проверяет отказ до запроса и отменённый контекст.
func TestMomentumFundingPageArguments(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP called for invalid request")
		return nil, nil
	})}
	for _, origin := range []string{"https://api.bybit.com:443", "http://api-testnet.bybit.com", "https://api.bybit.com.evil", "https://api.bybit.com/", "https://api.bybit.com?", "https://api.bybit.com#fragment"} {
		c := newClient(t, origin, httpClient)
		page, err := c.GetMomentumFundingPage(context.Background(), "BTCUSDT", 0, 1)
		if !errors.Is(err, ErrInvalidArgument) || !reflect.DeepEqual(page, model.FundingPage{}) {
			t.Fatalf("origin %s: %+v %v", origin, page, err)
		}
	}
	c := newClient(t, "https://api-testnet.bybit.com", httpClient)
	for _, tc := range []struct {
		symbol     string
		start, end int64
	}{{"btcusdt", 0, 1}, {"ETH-USDUSDT", 0, 1}, {"USDT", 0, 1}, {"BTCPERP", 0, 1}, {"BTCUSDT", -1, 1}, {"BTCUSDT", 0, 0}, {"BTCUSDT", 2, 1}} {
		page, err := c.GetMomentumFundingPage(context.Background(), tc.symbol, tc.start, tc.end)
		if !errors.Is(err, ErrInvalidArgument) || !reflect.DeepEqual(page, model.FundingPage{}) {
			t.Fatalf("args %+v: %+v %v", tc, page, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.GetMomentumFundingPage(ctx, "BTCUSDT", 0, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel: %v", err)
	}
}

// TestMomentumFundingPageShape проверяет пустой список, точечный диапазон и ошибки ответа.
func TestMomentumFundingPageShape(t *testing.T) {
	valid := fundingTestBody(fundingTestEvent)
	overLimitRows := make([]string, 201)
	for i := range overLimitRows {
		overLimitRows[i] = fmt.Sprintf(`{"symbol":"BTCUSDT","fundingRate":"0","fundingRateTimestamp":"%d"}`, 101+i)
	}
	for name, wire := range map[string]string{
		"wrong-category": strings.Replace(valid, "linear", "inverse", 1), "wrong-symbol": strings.Replace(valid, "BTCUSDT", "ETHUSDT", 1),
		"missing-list": `{"retCode":0,"time":999,"result":{"category":"linear"}}`, "null-list": strings.Replace(valid, "["+fundingTestEvent+"]", "null", 1),
		"numeric-timestamp": strings.Replace(valid, `"101"`, "101", 1), "overflow": strings.Replace(valid, "101", "9223372036854775808", 1),
		"signed-timestamp": strings.Replace(valid, `"101"`, `"+101"`, 1), "leading-zero-timestamp": strings.Replace(valid, `"101"`, `"0101"`, 1),
		"future": strings.Replace(valid, "999", "100", 1), "bad-time": strings.Replace(valid, "999", "0", 1),
		"empty-bad-time": strings.Replace(fundingTestBody(""), `"time":999`, `"time":0`, 1),
		"before-bound":   strings.Replace(valid, "101", "100", 1), "after-bound": strings.Replace(valid, "101", "102", 1),
		"duplicate": fundingTestBody(fundingTestEvent + "," + fundingTestEvent), "exp-rate": strings.Replace(valid, `"0"`, `"1e-5"`, 1),
		"nonnumeric-rate": strings.Replace(valid, `"0"`, `"bad"`, 1),
		"partial":         fundingTestBody(fundingTestEvent + `,{"symbol":"BTCUSDT","fundingRate":null,"fundingRateTimestamp":"102"}`),
		"null-rate":       strings.Replace(valid, `"0"`, "null", 1), "nonfinite-rate": strings.Replace(valid, `"0"`, `"NaN"`, 1),
		"over-limit": fundingTestBody(strings.Join(overLimitRows, ",")),
		"empty":      fundingTestBody(""), "point": valid,
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, "https://api-testnet.bybit.com", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return httpResponse(200, wire), nil
			})})
			end := int64(101)
			if name == "partial" {
				end = 102
			}
			if name == "over-limit" {
				end = 301
			}
			page, err := c.GetMomentumFundingPage(context.Background(), "BTCUSDT", 101, end)
			switch name {
			case "empty", "point":
				want := map[string]int{"empty": 0, "point": 1}[name]
				if err != nil || page.Events == nil || len(page.Events) != want || page.StartTime.UnixMilli() != 101 || page.EndTime.UnixMilli() != 101 {
					t.Fatalf("bounded page: %+v, %v", page, err)
				}
			default:
				if err == nil || !reflect.DeepEqual(page, model.FundingPage{}) {
					t.Fatalf("malformed page: %+v, %v", page, err)
				}
			}
		})
	}
}

// TestMomentumFundingPageLimit проверяет максимальную страницу с эпохой на нижней границе.
func TestMomentumFundingPageLimit(t *testing.T) {
	rows := make([]string, 200)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"symbol":"BTCUSDT","fundingRate":"0","fundingRateTimestamp":"%d"}`, i)
	}
	c := newClient(t, "https://api-testnet.bybit.com", &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		assertQuery(t, r, map[string]string{"category": "linear", "symbol": "BTCUSDT", "limit": "200", "startTime": "0", "endTime": "199"})
		return httpResponse(200, fundingTestBody(strings.Join(rows, ","))), nil
	})})
	page, err := c.GetMomentumFundingPage(context.Background(), "BTCUSDT", 0, 199)
	if err != nil || len(page.Events) != 200 || page.Events[0].FundingTime.UnixMilli() != 199 ||
		page.Events[199].FundingTime.UnixMilli() != 0 {
		t.Fatalf("limit: events=%d, err=%v", len(page.Events), err)
	}
}

// TestMomentumFundingPageFailures проверяет классификацию envelope/HTTP и отмену запроса.
func TestMomentumFundingPageFailures(t *testing.T) {
	for _, kind := range []string{"http", "api", "limit", "deadline", "cancel", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				switch kind {
				case "http":
					return httpResponse(503, "unavailable"), nil
				case "api":
					return httpResponse(200, `{"retCode":10006,"result":{}}`), nil
				case "limit":
					return httpResponse(200, strings.Repeat("x", maxResponseBytes+1)), nil
				case "deadline":
					return nil, context.DeadlineExceeded
				case "timeout":
					select {
					case <-r.Context().Done():
						return nil, r.Context().Err()
					case <-time.After(time.Second):
						return httpResponse(200, fundingTestBody(fundingTestEvent)), nil
					}
				default:
					cancel()
					return httpResponse(200, fundingTestBody(fundingTestEvent)), nil
				}
			})}
			if kind == "timeout" {
				httpClient.Timeout = 25 * time.Millisecond
			}
			c := newClient(t, "https://api-testnet.bybit.com", httpClient)
			page, err := c.GetMomentumFundingPage(ctx, "BTCUSDT", 0, 101)
			var httpErr *HTTPError
			var apiErr *APIError
			switch kind {
			case "http":
				if !errors.As(err, &httpErr) || httpErr.StatusCode != 503 {
					t.Fatal(err)
				}
			case "api":
				if !errors.As(err, &apiErr) || apiErr.Code != 10006 {
					t.Fatal(err)
				}
			default:
				want := map[string]error{"limit": ErrResponseTooLarge, "deadline": context.DeadlineExceeded, "cancel": context.Canceled, "timeout": context.DeadlineExceeded}[kind]
				if !errors.Is(err, want) {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(page, model.FundingPage{}) {
				t.Fatalf("unexpected partial page: %+v", page)
			}
		})
	}
}

// TestMomentumFundingPageRedirect подтверждает, что источник не сменится по Location.
func TestMomentumFundingPageRedirect(t *testing.T) {
	calls := 0
	c := newClient(t, "https://api-testnet.bybit.com", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		res := httpResponse(302, "redirect")
		res.Header.Set("Location", "https://api.bybit.com"+fundingHistoryPath)
		return res, nil
	})})
	page, err := c.GetMomentumFundingPage(context.Background(), "BTCUSDT", 0, 1)
	var httpErr *HTTPError
	if calls != 1 || !errors.As(err, &httpErr) || httpErr.StatusCode != 302 || !reflect.DeepEqual(page, model.FundingPage{}) {
		t.Fatalf("redirect: %+v, %v, calls=%d", page, err, calls)
	}
}
