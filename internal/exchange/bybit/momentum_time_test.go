package bybit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestGetMomentumTestnetTime проверяет canonical unsigned GET и UTC timestamp.
func TestGetMomentumTestnetTime(t *testing.T) {
	client := testClockClient(t)
	calls := 0
	client.httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet || req.URL.String() != "https://api-testnet.bybit.com/v5/market/time" || len(req.Header.Values("X-BAPI-SIGN")) != 0 || len(req.Header.Values("X-BAPI-API-KEY")) != 0 {
			t.Errorf("unexpected request: %s %s", req.Method, req.URL)
		}
		return timeResponse(req, 200, `{"retCode":0,"retMsg":"OK","result":{"timeSecond":"1"},"time":1763164800123}`), nil
	})
	got, err := client.GetMomentumTestnetTime(context.Background())
	if err != nil || got.Location() != time.UTC || got.UnixMilli() != 1763164800123 || calls != 1 {
		t.Fatalf("time=%v err=%v calls=%d", got, err, calls)
	}
}

// testClockClient создаёт Testnet-клиент для unit-теста.
func testClockClient(t *testing.T) *Client {
	t.Helper()
	client, err := NewClient("https://api-testnet.bybit.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// timeResponse создаёт HTTP-ответ fixture без сетевого listener.
func timeResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

// TestGetMomentumTestnetTimeRejects проверяет source, ошибки ответа, redirect и cancel.
func TestGetMomentumTestnetTimeRejects(t *testing.T) {
	client := testClockClient(t)
	calls := 0
	client.baseURL.Host = "api.bybit.com"
	client.httpClient.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected request") })
	_, mainnetErr := client.GetMomentumTestnetTime(context.Background())
	client.baseURL.Host, client.baseURL.Path = "api-testnet.bybit.com", "/path"
	_, pathErr := client.GetMomentumTestnetTime(context.Background())
	if mainnetErr == nil || pathErr == nil || calls != 0 {
		t.Fatalf("mainnet=%v invalid origin=%v calls=%d", mainnetErr, pathErr, calls)
	}
	client = testClockClient(t)
	for _, tc := range []struct {
		status int
		body   string
	}{
		{500, `secret-marker`},
		{200, `{"retCode":7,"retMsg":"bad","result":{},"time":1763164800000}`},
		{200, `{"retCode":0,"result":{}}`},
		{200, `{"retCode":0,"result":{},"time":0}`},
		{200, `{"retCode":0,"result":{},"time":9223372036854775807}`},
		{200, `malformed`},
	} {
		client.httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) { return timeResponse(req, tc.status, tc.body), nil })
		if _, err := client.GetMomentumTestnetTime(context.Background()); err == nil {
			t.Fatalf("accepted source/body %q", tc.body)
		}
	}
	client = testClockClient(t)
	calls = 0
	client.httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		resp := timeResponse(req, 302, "")
		resp.Header = make(http.Header)
		resp.Header.Set("Location", "https://evil.example/time")
		return resp, nil
	})
	if _, err := client.GetMomentumTestnetTime(context.Background()); err == nil || calls != 1 {
		t.Fatalf("redirect err=%v calls=%d", err, calls)
	}
	for _, wantCalls := range []int{0, 1} {
		ctx, cancel := context.WithCancel(context.Background())
		calls = 0
		client.httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			cancel()
			return timeResponse(req, 200, `{"retCode":0,"result":{},"time":1763164800000}`), nil
		})
		if wantCalls == 0 {
			cancel()
		}
		got, err := client.GetMomentumTestnetTime(ctx)
		cancel()
		if !errors.Is(err, context.Canceled) || !got.IsZero() || calls != wantCalls {
			t.Fatalf("calls=%d time=%v err=%v", calls, got, err)
		}
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Time{})
	defer stop()
	calls = 0
	got, err := client.GetMomentumTestnetTime(deadline)
	if !errors.Is(err, context.DeadlineExceeded) || !got.IsZero() || calls != 0 {
		t.Fatalf("deadline time=%v err=%v calls=%d", got, err, calls)
	}
}
