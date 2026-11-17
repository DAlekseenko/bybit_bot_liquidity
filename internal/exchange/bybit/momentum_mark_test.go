package bybit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// markPriceTestRow создаёт строку mark API с четырьмя точными ценами.
func markPriceTestRow(stamp int64) string {
	return fmt.Sprintf(`["%d","9007199254740993.0000001","9007199254740994.0000002","9007199254740991.0000003","9007199254740993.111111111111111111"]`, stamp)
}

// markPriceTestBody задаёт фактическое время ответа и произвольный список.
func markPriceTestBody(server int64, list string) string {
	return fmt.Sprintf(`{"retCode":0,"time":%d,"result":{"symbol":"BTCUSDT","category":"linear","list":[%s]}}`, server, list)
}

// markPriceTestRows создаёт строки с уникальными убывающими UTC bucket timestamp.
func markPriceTestRows(count int, step int64) string {
	rows := make([]string, count)
	for i := range rows {
		rows[i] = markPriceTestRow(int64(count-i) * step)
	}
	return strings.Join(rows, ",")
}

// TestMomentumMarkPriceWire проверяет D/60, exact boundaries, provenance и форму OHLC.
func TestMomentumMarkPriceWire(t *testing.T) {
	for host, environment := range map[string]string{"api-testnet.bybit.com": "testnet", "api.bybit.com": "mainnet"} {
		for interval, step := range map[string]int64{"D": 86_400_000, "60": 3_600_000} {
			calls := 0
			latest, oldest := 3*step, 2*step
			hc := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != host || r.URL.Path != markPriceKlinePath {
					t.Fatalf("request: %s %s", r.Method, r.URL)
				}
				assertQuery(t, r, map[string]string{"category": "linear", "symbol": "BTCUSDT", "interval": interval, "limit": "200",
					"start": strconv.FormatInt(oldest, 10), "end": strconv.FormatInt(latest, 10)})
				for _, key := range []string{"Authorization", "Cookie", "X-BAPI-API-KEY", "X-BAPI-SIGN", "X-BAPI-TIMESTAMP", "X-BAPI-RECV-WINDOW"} {
					if r.Header.Get(key) != "" {
						t.Fatalf("unexpected header %q", key)
					}
				}
				return httpResponse(200, markPriceTestBody(latest, markPriceTestRow(latest)+","+markPriceTestRow(oldest))), nil
			})}
			client := newAuthenticatedClient(t, "https://"+host, "fake-key", "fake-secret", hc)
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			jar.SetCookies(&client.baseURL, []*http.Cookie{{Name: "synthetic", Value: "cookie"}})
			hc.Jar = jar
			page, err := client.GetMomentumMarkPricePage(context.Background(), "BTCUSDT", interval, oldest, latest)
			if err != nil || calls != 1 || hc.Jar != jar || len(page.Candles) != 2 || page.Environment != environment ||
				page.Source != "bybit_v5" || page.Category != "linear" || page.Endpoint != "https://"+host+markPriceKlinePath ||
				page.Symbol != "BTCUSDT" || page.IntervalCode != interval || page.StartTime != time.UnixMilli(oldest).UTC() ||
				page.EndTime != time.UnixMilli(latest).UTC() || page.ServerTime != time.UnixMilli(latest).UTC() {
				t.Fatalf("unexpected page: %+v, %v", page, err)
			}
			for i, row := range page.Candles {
				wantStamp := latest - int64(i)*step
				if row.OpenTime != time.UnixMilli(wantStamp).UTC() || row.Confirmed != (i == 1) {
					t.Fatalf("unexpected candle %d: %+v", i, row)
				}
				values := []string{"9007199254740993.0000001", "9007199254740994.0000002", "9007199254740991.0000003", "9007199254740993.111111111111111111"}
				for j, price := range []model.Decimal{row.Open, row.High, row.Low, row.Close} {
					exact, err := price.WireDecimal()
					if err != nil || exact != values[j] {
						t.Fatalf("field %d: %q %v", j, exact, err)
					}
				}
			}
		}
	}
}

// TestMomentumMarkPriceArguments проверяет отказ до HTTP при неизвестном origin и диапазоне.
func TestMomentumMarkPriceArguments(t *testing.T) {
	hc := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP called for invalid request")
		return nil, nil
	})}
	for _, origin := range []string{"https://api.bybit.com:443", "http://api-testnet.bybit.com", "https://api.bybit.com.evil", "https://api.bybit.com/", "https://api.bybit.com?x", "https://api.bybit.com#fragment"} {
		c := newClient(t, origin, hc)
		got, err := c.GetMomentumMarkPricePage(context.Background(), "BTCUSDT", "60", 3_600_000, 3_600_000)
		if !errors.Is(err, ErrInvalidArgument) || !reflect.DeepEqual(got, model.MarkPricePage{}) {
			t.Fatalf("origin %s: %+v, %v", origin, got, err)
		}
	}
	c := newClient(t, "https://api-testnet.bybit.com", hc)
	for _, tc := range []struct {
		symbol, interval string
		start, end       int64
	}{
		{"btcusdt", "60", 3_600_000, 3_600_000}, {"ETHPERP", "60", 3_600_000, 3_600_000}, {"ETH-USDUSDT", "60", 3_600_000, 3_600_000},
		{"BTCUSDT", "1", 3_600_000, 3_600_000}, {"BTCUSDT", "D", 3_600_000, 86_400_000}, {"BTCUSDT", "60", 3_600_001, 7_200_000},
		{"BTCUSDT", "60", 0, 7_200_000}, {"BTCUSDT", "60", 3_600_000, 3_599_999},
	} {
		got, err := c.GetMomentumMarkPricePage(context.Background(), tc.symbol, tc.interval, tc.start, tc.end)
		if !errors.Is(err, ErrInvalidArgument) || !reflect.DeepEqual(got, model.MarkPricePage{}) {
			t.Fatalf("%+v: %+v, %v", tc, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.GetMomentumMarkPricePage(ctx, "BTCUSDT", "60", 3_600_000, 3_600_000)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel: %v", err)
	}
}

// TestMomentumMarkPriceShape проверяет ошибки формы и пропуски без заполнения.
func TestMomentumMarkPriceShape(t *testing.T) {
	const step = 3_600_000
	row := markPriceTestRow(3 * step)
	control := markPriceTestBody(4*step, row)
	for name, wire := range map[string]string{
		"wrong-category": strings.Replace(control, "linear", "inverse", 1), "wrong-symbol": strings.Replace(control, "BTCUSDT", "ETHUSDT", 1),
		"missing-list": `{"retCode":0,"time":14400000,"result":{"category":"linear","symbol":"BTCUSDT"}}`,
		"null-list":    strings.Replace(control, "["+row+"]", "null", 1), "malformed-envelope": "{",
		"nonpositive-clock": markPriceTestBody(0, row), "future-open": markPriceTestBody(3*step-1, row),
		"misaligned":         markPriceTestBody(4*step, markPriceTestRow(3*step-1)),
		"before-window":      markPriceTestBody(4*step, markPriceTestRow(step)),
		"after-window":       markPriceTestBody(6*step, markPriceTestRow(5*step)),
		"overflow":           strings.Replace(control, "10800000", "9223372036854775808", 1),
		"duplicate":          markPriceTestBody(4*step, row+","+row),
		"ascending":          markPriceTestBody(4*step, markPriceTestRow(2*step)+","+row),
		"seven-fields":       markPriceTestBody(4*step, row[:len(row)-1]+`,"0","0"]`),
		"zero-price":         strings.Replace(control, "9007199254740993.0000001", "0", 1),
		"nonfinite-price":    strings.Replace(control, "9007199254740993.0000001", "Infinity", 1),
		"bad-high":           strings.Replace(control, "9007199254740994.0000002", "9007199254740993.0000001", 1),
		"bad-low":            strings.Replace(control, "9007199254740991.0000003", "9007199254740993.111111111111111111", 1),
		"invalid-second-row": markPriceTestBody(4*step, row+`,["7200000","9","10","1"]`),
		"empty":              markPriceTestBody(4*step, ""), "gap": markPriceTestBody(4*step, markPriceTestRow(4*step)+","+markPriceTestRow(2*step)),
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, "https://api-testnet.bybit.com", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return httpResponse(200, wire), nil
			})})
			got, err := c.GetMomentumMarkPricePage(context.Background(), "BTCUSDT", "60", 2*step, 4*step+1)
			if n, ok := map[string]int{"empty": 0, "gap": 2}[name]; ok {
				if err != nil || got.Candles == nil || len(got.Candles) != n {
					t.Fatalf("valid page: %+v, %v", got, err)
				}
				if got.Environment != "testnet" || got.Source != "bybit_v5" || got.Category != "linear" ||
					got.Endpoint != "https://api-testnet.bybit.com"+markPriceKlinePath || got.Symbol != "BTCUSDT" ||
					got.IntervalCode != "60" || !got.StartTime.Equal(time.UnixMilli(2*step).UTC()) ||
					!got.EndTime.Equal(time.UnixMilli(4*step+1).UTC()) || !got.ServerTime.Equal(time.UnixMilli(4*step).UTC()) {
					t.Fatalf("incomplete page provenance: %+v", got)
				}
				if name == "gap" && (!got.Candles[0].OpenTime.Equal(time.UnixMilli(4*step).UTC()) ||
					!got.Candles[1].OpenTime.Equal(time.UnixMilli(2*step).UTC())) {
					t.Fatalf("gap timestamps were changed: %+v", got.Candles)
				}
			} else if err == nil || !reflect.DeepEqual(got, model.MarkPricePage{}) {
				t.Fatalf("bad page: %+v, %v", got, err)
			}
		})
	}
}

// TestMomentumMarkPriceLimits проверяет точечный диапазон и полный лимит.
func TestMomentumMarkPriceLimits(t *testing.T) {
	const step = 3_600_000
	for _, n := range []int{1, 200} {
		rows := make([]string, n)
		for i := range rows {
			rows[i] = markPriceTestRow(int64(n-i) * step)
		}
		c := newClient(t, "https://api-testnet.bybit.com", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return httpResponse(200, markPriceTestBody(int64(n+1)*step, strings.Join(rows, ","))), nil
		})})
		got, err := c.GetMomentumMarkPricePage(context.Background(), "BTCUSDT", "60", step, int64(n)*step)
		if err != nil || len(got.Candles) != n || !got.Candles[0].Confirmed || got.Candles[n-1].OpenTime.UnixMilli() != step {
			t.Fatalf("n=%d: size=%d, %v", n, len(got.Candles), err)
		}
	}
	client := newClient(t, "https://api-testnet.bybit.com", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return httpResponse(200, markPriceTestBody(202*step, markPriceTestRows(201, step))), nil
	})})
	got, err := client.GetMomentumMarkPricePage(context.Background(), "BTCUSDT", "60", step, 201*step)
	if err == nil || !reflect.DeepEqual(got, model.MarkPricePage{}) {
		t.Fatalf("expected 201-row page error and zero page: candles=%d error=%v", len(got.Candles), err)
	}
}

// TestMomentumMarkPricePointRange проверяет включительный запрос одной UTC-точки.
func TestMomentumMarkPricePointRange(t *testing.T) {
	const point = 3_600_000
	calls := 0
	c := newClient(t, "https://api-testnet.bybit.com", &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		assertQuery(t, r, map[string]string{"category": "linear", "symbol": "BTCUSDT", "interval": "60", "limit": "200",
			"start": strconv.FormatInt(point, 10), "end": strconv.FormatInt(point, 10)})
		return httpResponse(200, markPriceTestBody(point+1, markPriceTestRow(point))), nil
	})})
	page, err := c.GetMomentumMarkPricePage(context.Background(), "BTCUSDT", "60", point, point)
	if err != nil || calls != 1 || len(page.Candles) != 1 || page.Candles[0].OpenTime.UnixMilli() != point || page.Candles[0].Confirmed {
		t.Fatalf("point page: %+v, calls=%d, error=%v", page, calls, err)
	}
}

// TestMomentumMarkPriceTransport проверяет отмену, bounded ошибки и блокировку смены origin.
func TestMomentumMarkPriceTransport(t *testing.T) {
	for _, kind := range []string{"302", "503", "retcode", "limit", "deadline", "timeout", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			cancel := func() {}
			var clientTimeout time.Duration
			switch kind {
			case "deadline":
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			case "timeout":
				clientTimeout = 20 * time.Millisecond
			case "cancel":
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			calls := 0
			hc := &http.Client{Timeout: clientTimeout, Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				switch kind {
				case "retcode":
					return httpResponse(200, `{"retCode":10006,"result":{}}`), nil
				case "limit":
					return httpResponse(200, strings.Repeat("x", maxResponseBytes+1)), nil
				case "deadline", "timeout":
					select {
					case <-r.Context().Done():
						return nil, r.Context().Err()
					case <-time.After(time.Second):
						return nil, errors.New("request context did not expire")
					}
				case "cancel":
					cancel()
					return httpResponse(200, markPriceTestBody(7_200_000, markPriceTestRow(3_600_000))), nil
				default:
					code, _ := strconv.Atoi(kind)
					res := httpResponse(code, "error")
					res.Header.Set("Location", "https://api.bybit.com"+markPriceKlinePath)
					return res, nil
				}
			})}
			c := newClient(t, "https://api-testnet.bybit.com", hc)
			got, err := c.GetMomentumMarkPricePage(ctx, "BTCUSDT", "60", 3_600_000, 3_600_000)
			var httpErr *HTTPError
			var apiErr *APIError
			switch kind {
			case "302", "503":
				if !errors.As(err, &httpErr) || strconv.Itoa(httpErr.StatusCode) != kind {
					t.Fatal(err)
				}
			case "retcode":
				if !errors.As(err, &apiErr) || apiErr.Code != 10006 {
					t.Fatal(err)
				}
			default:
				want := map[string]error{"limit": ErrResponseTooLarge, "deadline": context.DeadlineExceeded,
					"timeout": context.DeadlineExceeded, "cancel": context.Canceled}[kind]
				if !errors.Is(err, want) {
					t.Fatal(err)
				}
			}
			if calls != 1 || !reflect.DeepEqual(got, model.MarkPricePage{}) {
				t.Fatalf("unexpected response: %+v, calls %d", got, calls)
			}
		})
	}
}
