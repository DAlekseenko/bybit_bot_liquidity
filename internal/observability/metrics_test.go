package observability

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRegistryExportsSafeOperationalMetrics проверяет фиксированные значения без деталей ошибок.
func TestRegistryExportsSafeOperationalMetrics(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	registry.MarkMarketDataSuccess(time.Unix(100, 0))
	registry.MarkMarketDataFailure()
	registry.MarkWebsocketReconnect()
	registry.ObserveStorageWrite(2*time.Second, errors.New("database contains secret"))
	registry.SetReadiness(false)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, want := range []string{
		"bybit_bot_market_data_last_success_unixtime 100",
		"bybit_bot_market_data_errors_total 1",
		"bybit_bot_storage_write_errors_total 1",
		"bybit_bot_storage_write_duration_seconds_sum 2.000000000",
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

// TestRegistryHandlerRejectsOtherPaths проверяет 404 для каждого пути кроме метрик.
func TestRegistryHandlerRejectsOtherPaths(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/", "/metrics/", "/health", "/metrics-extra"} {
		response := httptest.NewRecorder()
		NewRegistry().Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("path %q status = %d, want 404", path, response.Code)
		}
	}
}

// TestRegistryConcurrentAccess проверяет конкурентное чтение и изменение метрик.
func TestRegistryConcurrentAccess(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go recordMetricsConcurrently(registry, &workers)
	}
	for i := 0; i < 8; i++ {
		response := httptest.NewRecorder()
		registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	}
	workers.Wait()
	if got := strings.Count(registry.metricsText(), "bybit_bot_market_data_errors_total 800"); got != 1 {
		t.Fatalf("concurrent failure count missing from metrics: %s", registry.metricsText())
	}
}

// recordMetricsConcurrently меняет ограниченные счётчики в тесте конкуренции.
func recordMetricsConcurrently(registry *Registry, workers *sync.WaitGroup) {
	defer workers.Done()
	for i := 0; i < 100; i++ {
		registry.MarkMarketDataFailure()
		registry.MarkWebsocketReconnect()
	}
}
