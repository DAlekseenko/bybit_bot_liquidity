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

// TestRegistryExportsSafeOperationalMetrics проверяет фиксированный набор метрик.
func TestRegistryExportsSafeOperationalMetrics(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	if body := scrapeMetrics(t, registry); !strings.Contains(body, `state="not_configured"`) {
		t.Fatalf("default readiness missing: %s", body)
	}
	registry.MarkMarketDataSuccess(time.Unix(100, 0))
	registry.MarkMarketDataFailure()
	registry.MarkWebsocketReconnect()
	registry.ObserveStorageWrite(2*time.Second, errors.New("private error detail"))
	registry.SetReadiness(false)
	body := scrapeMetrics(t, registry)
	for _, want := range []string{
		"bybit_bot_market_data_last_success_unixtime 100",
		"bybit_bot_market_data_errors_total 1",
		"bybit_bot_storage_write_errors_total 1",
		"bybit_bot_storage_write_duration_seconds_sum 2.000000000",
		"bybit_bot_storage_write_duration_seconds_count 1",
		"bybit_bot_websocket_reconnects_total 1",
		`bybit_bot_execution_readiness{state="failed"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "private error detail") {
		t.Fatalf("metrics leaked error detail: %s", body)
	}
}

// TestRegistryHandlerRejectsOtherRoutes проверяет тип содержимого и закрытый маршрут.
func TestRegistryHandlerRejectsOtherRoutes(t *testing.T) {
	t.Parallel()
	handler := NewRegistry().Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if got := response.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("unexpected content type: %q", got)
	}
}

// TestRegistryConcurrentUpdatesAndScrapes выявляет гонки между записью и чтением.
func TestRegistryConcurrentUpdatesAndScrapes(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	handler := registry.Handler()
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		// Горутина одновременно обновляет реестр и читает его HTTP-снимок.
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				registry.MarkMarketDataSuccess(time.Unix(int64(j), 0))
				registry.MarkMarketDataFailure()
				registry.ObserveStorageWrite(time.Millisecond, nil)
				registry.MarkWebsocketReconnect()
				registry.SetReadiness(j%2 == 0)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
				if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "bybit_bot_") {
					t.Errorf("invalid scrape response: %d", response.Code)
					return
				}
			}
		}()
	}
	workers.Wait()
}

// scrapeMetrics выполняет локальный запрос к тестовому обработчику.
func scrapeMetrics(t *testing.T, registry *Registry) string {
	t.Helper()
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	return response.Body.String()
}
