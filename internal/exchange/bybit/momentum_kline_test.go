package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// dailyRangeDays строит убывающие номера суток для независимых fake-страниц.
func dailyRangeDays(high, low int64) []int64 {
	var days []int64
	for day := high; day >= low; day-- {
		days = append(days, day)
	}
	return days
}

// TestGetDailyCandles проверяет пагинацию, границы, отказы и отсутствие частичных данных.
func TestGetDailyCandles(t *testing.T) {
	const dayMs int64 = 86400000
	var exhausted, twentieth [][]int64
	for day := int64(30); day > 10; day-- {
		exhausted = append(exhausted, []int64{day})
		twentieth = append(twentieth, []int64{day - 10})
	}
	transportErr := errors.New("second page transport")
	wantOpen, parseErr := model.ParsePrice("9007199254740993.00000001")
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	for _, tc := range []struct {
		name       string
		pages      [][]int64
		start, end int64
		want       []int64
		calls      int
		failure    string
	}{
		{"203 candles", [][]int64{dailyRangeDays(203, 4), {3, 2, 1}}, 1, 204, dailyRangeDays(203, 1), 2, ""},
		{"short then empty", [][]int64{{6, 5}, {4}, nil}, 1, 7, []int64{6, 5, 4}, 3, ""},
		{"short to lower", [][]int64{{6}, {5, 4}}, 4, 7, []int64{6, 5, 4}, 2, ""},
		{"filter both edges", [][]int64{{7, 6}, {3, 2}}, 3, 7, []int64{6, 3}, 2, ""},
		{"empty", [][]int64{nil}, 1, 7, nil, 1, "empty"},
		{"filtered empty", [][]int64{{7}, {2}}, 3, 7, nil, 2, "empty"},
		{"duplicate", [][]int64{{6, 5}, {5, 4}}, 1, 7, nil, 2, "page"},
		{"overlap", [][]int64{{6, 5}, {6, 4}}, 1, 7, nil, 2, "page"},
		{"filtered overlap", [][]int64{{7, 6}, {7, 3}}, 3, 7, nil, 2, "page"},
		{"nonprogress", [][]int64{{6, 5}, {7}}, 1, 7, nil, 2, "page"},
		{"exhausted", exhausted, 1, 31, nil, 20, "page"},
		{"twentieth reaches start", twentieth, 1, 21, dailyRangeDays(20, 1), 20, ""},
		{"twentieth empty", append(exhausted[:19:19], nil), 1, 31, dailyRangeDays(30, 12), 20, ""},
		{"transport second", [][]int64{{6}, nil}, 1, 7, nil, 2, "transport"},
		{"context second", [][]int64{{6}, nil}, 1, 7, nil, 2, "context"},
		{"HTTP second", [][]int64{{6}, nil}, 1, 7, nil, 2, "HTTP"},
		{"API second", [][]int64{{6}, nil}, 1, 7, nil, 2, "API"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, cursor := 0, tc.end*dayMs-1
			client := newClient(t, "https://example.test", &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if calls >= len(tc.pages) {
					t.Fatalf("unexpected request %d", calls+1)
				}
				if r.Method != http.MethodGet || r.URL.Path != "/v5/market/kline" {
					t.Fatalf("request = %s %s", r.Method, r.URL)
				}
				assertQuery(t, r, map[string]string{
					"category": "linear", "symbol": "BTCUSDT", "interval": "D", "limit": "200",
					"start": strconv.FormatInt(tc.start*dayMs-1, 10), "end": strconv.FormatInt(cursor, 10),
				})
				days := tc.pages[calls]
				calls++
				if calls == 2 {
					switch tc.failure {
					case "transport":
						return nil, transportErr
					case "context":
						return nil, context.Canceled
					case "HTTP":
						return httpResponse(503, "busy"), nil
					case "API":
						return httpResponse(200, `{"retCode":7,"time":1,"result":{}}`), nil
					}
				}
				rows := make([][]string, 0, len(days))
				for _, day := range days {
					rows = append(rows, dailyPageRow(day*dayMs))
					cursor = day*dayMs - 1
				}
				body, err := json.Marshal(map[string]any{"retCode": 0, "time": 204 * dayMs, "result": klineResult{"linear", "BTCUSDT", rows}})
				if err != nil {
					t.Fatal(err)
				}
				return httpResponse(200, string(body)), nil
			})})
			got, err := client.GetDailyCandles(context.Background(), "BTCUSDT", time.UnixMilli(tc.start*dayMs), time.UnixMilli(tc.end*dayMs))
			if calls != tc.calls || (tc.failure == "" && err != nil) || (tc.failure != "" && (err == nil || got != nil)) {
				t.Fatalf("calls=%d result=%v error=%v", calls, got, err)
			}
			for kind, want := range map[string]error{"empty": ErrEmptyResult, "transport": transportErr, "context": context.Canceled} {
				if tc.failure == kind && !errors.Is(err, want) {
					t.Fatalf("error = %v, want %v", err, want)
				}
			}
			var httpErr *HTTPError
			var apiErr *APIError
			if tc.failure == "HTTP" && (!errors.As(err, &httpErr) || httpErr.StatusCode != 503) ||
				tc.failure == "API" && (!errors.As(err, &apiErr) || apiErr.Code != 7) {
				t.Fatalf("typed error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("length = %d, want %d", len(got), len(tc.want))
			}
			for i, candle := range got {
				if cmp, cmpErr := candle.Open.Cmp(wantOpen); cmpErr != nil || cmp != 0 {
					t.Fatalf("open precision lost: %v", cmpErr)
				}
				if candle.OpenTime != time.UnixMilli(tc.want[len(tc.want)-1-i]*dayMs).UTC() || candle.Symbol != "BTCUSDT" || !candle.Confirmed {
					t.Fatalf("candle %d = %+v", i, candle)
				}
			}
		})
	}
}

// TestGetDailyCandlesInvalid проверяет делегирование preflight до первого HTTP.
func TestGetDailyCandlesInvalid(t *testing.T) {
	client := newClient(t, "https://example.test", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected HTTP")
		return nil, errors.New("unexpected HTTP")
	})})
	start, end := time.UnixMilli(86400000).UTC(), time.UnixMilli(172800000).UTC()
	for _, tc := range []struct {
		symbol     string
		start, end time.Time
	}{{"btcusdt", start, end}, {"BTCUSDT", start.Add(time.Nanosecond), end}, {"BTCUSDT", start, end.Add(time.Nanosecond)}} {
		got, err := client.GetDailyCandles(context.Background(), tc.symbol, tc.start, tc.end)
		if got != nil || !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%+v: %v, %v", tc, got, err)
		}
	}
}
