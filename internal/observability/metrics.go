// Package observability предоставляет ограниченные безопасные метрики процесса.
package observability

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Registry хранит фиксированный набор операционных метрик без внешних меток.
type Registry struct {
	mu                  sync.RWMutex
	lastMarketData      time.Time
	marketDataErrors    uint64
	storageWriteErrors  uint64
	storageWriteSeconds float64
	storageWriteCount   uint64
	websocketReconnects uint64
	readinessState      string
}

// NewRegistry создаёт реестр с состоянием readiness not_configured.
func NewRegistry() *Registry { return &Registry{readinessState: "not_configured"} }

// MarkMarketDataSuccess сохраняет время успешного обновления в UTC.
func (r *Registry) MarkMarketDataSuccess(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastMarketData = at.UTC()
}

// MarkMarketDataFailure увеличивает фиксированный счётчик ошибок данных.
func (r *Registry) MarkMarketDataFailure() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.marketDataErrors++
}

// ObserveStorageWrite учитывает длительность записи и её результат без деталей ошибки.
func (r *Registry) ObserveStorageWrite(duration time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.storageWriteSeconds += duration.Seconds()
	r.storageWriteCount++
	if err != nil {
		r.storageWriteErrors++
	}
}

// MarkWebsocketReconnect увеличивает фиксированный счётчик переподключений.
func (r *Registry) MarkWebsocketReconnect() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.websocketReconnects++
}

// SetReadiness записывает одно из двух фиксированных состояний readiness.
func (r *Registry) SetReadiness(ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ready {
		r.readinessState = "ready"
		return
	}
	r.readinessState = "failed"
}

// Handler возвращает обработчик только для маршрута /metrics.
func (r *Registry) Handler() http.Handler { return metricsHandler{registry: r} }

type metricsHandler struct{ registry *Registry }

// ServeHTTP отдаёт фиксированные метрики или ответ 404 для других маршрутов.
func (h metricsHandler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/metrics" {
		http.NotFound(w, request)
		return
	}
	r := h.registry
	r.mu.RLock()
	defer r.mu.RUnlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	lastSuccess := float64(0)
	if !r.lastMarketData.IsZero() {
		lastSuccess = float64(r.lastMarketData.Unix())
	}
	fmt.Fprintf(w, "bybit_bot_market_data_last_success_unixtime %.0f\n", lastSuccess)
	fmt.Fprintf(w, "bybit_bot_market_data_errors_total %d\n", r.marketDataErrors)
	fmt.Fprintf(w, "bybit_bot_storage_write_errors_total %d\n", r.storageWriteErrors)
	fmt.Fprintf(w, "bybit_bot_storage_write_duration_seconds_sum %.9f\n", r.storageWriteSeconds)
	fmt.Fprintf(w, "bybit_bot_storage_write_duration_seconds_count %d\n", r.storageWriteCount)
	fmt.Fprintf(w, "bybit_bot_websocket_reconnects_total %d\n", r.websocketReconnects)
	fmt.Fprintf(w, "bybit_bot_execution_readiness{state=%q} 1\n", r.readinessState)
}
