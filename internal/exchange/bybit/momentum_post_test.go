package bybit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// testPostBody — фиксированное тело запроса для fixture подписи.
type testPostBody struct {
	Category string `json:"category"`
	Symbol   string `json:"symbol"`
}

// fixtureSignature посчитана независимо: openssl dgst -sha256 -hmac fake-secret.
const fixtureSignature = "8f2aae595628683468707383c6a278cd60d2251b669421c3c44bc053c9170fd9"

// testPostClient создаёт Testnet-клиент с фиксированным временем и fake transport.
func testPostClient(t *testing.T, baseURL string, rt http.RoundTripper) *Client {
	t.Helper()
	client := newAuthenticatedClient(t, baseURL, "fake-key", "fake-secret", &http.Client{Transport: rt, Timeout: 5 * time.Second})
	client.now = func() time.Time { return time.UnixMilli(1700000000000) }
	return client
}

// TestPostMomentumTestnetSignedFixture проверяет подпись, заголовки, байты тела и разбор ответа.
func TestPostMomentumTestnetSignedFixture(t *testing.T) {
	calls := 0
	client := testPostClient(t, "https://api-testnet.bybit.com", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.String() != "https://api-testnet.bybit.com/v5/test/echo" ||
			string(body) != `{"category":"linear","symbol":"BTCUSDT"}` || r.Header.Get("Content-Type") != "application/json" ||
			r.Header.Get("X-BAPI-API-KEY") != "fake-key" || r.Header.Get("X-BAPI-TIMESTAMP") != "1700000000000" ||
			r.Header.Get("X-BAPI-RECV-WINDOW") != "5000" || r.Header.Get("X-BAPI-SIGN") != fixtureSignature {
			t.Errorf("unexpected request %s %s body=%s headers=%v", r.Method, r.URL, body, r.Header)
		}
		return timeResponse(r, 200, `{"retCode":0,"retMsg":"OK","result":{"orderId":"x"},"time":1700000000123}`), nil
	}))
	got, err := client.PostMomentumTestnetSigned(context.Background(), "/v5/test/echo", testPostBody{"linear", "BTCUSDT"})
	if err != nil || calls != 1 || string(got.Result) != `{"orderId":"x"}` || got.ServerTime.UnixMilli() != 1700000000123 || got.ServerTime.Location() != time.UTC {
		t.Fatalf("result=%+v err=%v calls=%d", got, err, calls)
	}
}

// TestPostMomentumTestnetSignedRefuses проверяет отказ вне Testnet и на неверных аргументах без запроса.
func TestPostMomentumTestnetSignedRefuses(t *testing.T) {
	calls := 0
	rt := roundTripperFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected") })
	for _, base := range []string{"https://api.bybit.com", "http://api-testnet.bybit.com", "https://api-testnet.bybit.com:8443", "https://api-testnet.bybit.com/proxy", "http://127.0.0.1:1"} {
		if _, err := testPostClient(t, base, rt).PostMomentumTestnetSigned(context.Background(), "/v5/x", testPostBody{}); !errors.Is(err, ErrPostNotTestnet) {
			t.Fatalf("base %s: err=%v", base, err)
		}
	}
	client := testPostClient(t, "https://api-testnet.bybit.com", rt)
	for _, path := range []string{"", "/v5/", "/v4/x", "/v5/x?a=1", "/v5/../x", "/v5//x", "https://evil/v5/x"} {
		if _, err := client.PostMomentumTestnetSigned(context.Background(), path, testPostBody{}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("path %q: err=%v", path, err)
		}
	}
	for _, body := range []any{nil, []int{1}, "x", func() {}} {
		if _, err := client.PostMomentumTestnetSigned(context.Background(), "/v5/x", body); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("body %T: err=%v", body, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.PostMomentumTestnetSigned(ctx, "/v5/x", testPostBody{}); !errors.Is(err, context.Canceled) || errors.Is(err, ErrPostOutcomeUnknown) {
		t.Fatalf("cancelled before send: err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("refused requests reached transport: %d", calls)
	}
}

// TestPostMomentumTestnetSignedClassifiesOutcome проверяет unknown/definitive исходы, один запрос и отсутствие секретов.
func TestPostMomentumTestnetSignedClassifiesOutcome(t *testing.T) {
	reset := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	for _, tc := range []struct {
		status  int
		body    string
		rtErr   error
		unknown bool
	}{
		{rtErr: reset, unknown: true},
		{status: 500, body: "boom", unknown: true},
		{status: 408, unknown: true},
		{status: 200, body: `{"retCode":10000,"retMsg":"timeout","result":{},"time":1}`, unknown: true},
		{status: 200, body: `{"retCode":10016,"retMsg":"internal","result":{},"time":1}`, unknown: true},
		{status: 200, body: `malformed`, unknown: true},
		{status: 200, body: `{"retCode":0,"result":{}}`, unknown: true},
		{status: 200, body: `{"retCode":0,"result":null,"time":1}`, unknown: true},
		{status: 403, body: "forbidden"},
		{status: 200, body: `{"retCode":10001,"retMsg":"params error","result":{},"time":1}`},
		{status: 200, body: `{"retCode":10006,"retMsg":"rate limit","result":{},"time":1}`},
	} {
		calls := 0
		client := testPostClient(t, "https://api-testnet.bybit.com", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if tc.rtErr != nil {
				return nil, tc.rtErr
			}
			return timeResponse(r, tc.status, tc.body), nil
		}))
		_, err := client.PostMomentumTestnetSigned(context.Background(), "/v5/x", testPostBody{"linear", "BTCUSDT"})
		if err == nil || errors.Is(err, ErrPostOutcomeUnknown) != tc.unknown || calls != 1 {
			t.Fatalf("case %d %q: err=%v calls=%d", tc.status, tc.body, err, calls)
		}
		assertNoSecrets(t, err.Error()+fmt.Sprintf("%v %+v %#v", client, client, client))
	}
}

// TestPostMomentumTestnetSignedTimeoutSendsOnce проверяет через httptest, что timeout даёт unknown без повтора.
func TestPostMomentumTestnetSignedTimeoutSendsOnce(t *testing.T) {
	var hits atomic.Int32
	stop := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer server.Close()
	defer close(stop)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	client := testPostClient(t, "https://api-testnet.bybit.com", transport)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := client.PostMomentumTestnetSigned(ctx, "/v5/x", testPostBody{"linear", "BTCUSDT"})
	if !errors.Is(err, ErrPostOutcomeUnknown) || !errors.Is(err, context.DeadlineExceeded) || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
	assertNoSecrets(t, err.Error())
}

// assertNoSecrets проверяет, что текст не раскрывает secret и подпись.
func assertNoSecrets(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "fake-secret") || strings.Contains(text, fixtureSignature) || strings.Contains(text, "fake-key") {
		t.Fatalf("secret material leaked: %s", text)
	}
}
