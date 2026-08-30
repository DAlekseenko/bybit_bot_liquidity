package observability

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegistryExportsSafeOperationalMetrics(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.MarkMarketDataSuccess(time.Unix(100, 0))
	registry.MarkMarketDataFailure()
	registry.MarkWebsocketReconnect()
	registry.ObserveStorageWrite(2*time.Second, errors.New("database contains secret"))
	registry.SetReadiness(false)

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, want := range []string{
		"bybit_bot_market_data_last_success_unixtime 100",
		"bybit_bot_market_data_errors_total 1",
		"bybit_bot_storage_write_errors_total 1",
		"bybit_bot_websocket_reconnects_total 1",
		"bybit_bot_execution_readiness{state=\"failed\"} 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "secret") {
		t.Fatalf("metrics leak an error detail: %s", body)
	}
}
