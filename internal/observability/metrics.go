// Package observability предоставляет ограниченные метрики read-only процесса.
package observability

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Registry хранит метрики процесса без данных счёта, секретов и payload.
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

// NewRegistry создаёт реестр с безопасным начальным состоянием readiness.
func NewRegistry() *Registry { return &Registry{readinessState: "not_configured"} }

// MarkMarketDataSuccess сохраняет время успешного снимка рынка.
func (r *Registry) MarkMarketDataSuccess(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastMarketData = at.UTC()
}

// MarkMarketDataFailure увеличивает счётчик ошибок снимка рынка.
func (r *Registry) MarkMarketDataFailure() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.marketDataErrors++
}

// ObserveStorageWrite сохраняет длительность и успех без текста ошибки.
func (r *Registry) ObserveStorageWrite(duration time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if duration > 0 {
		r.storageWriteSeconds += duration.Seconds()
	}
	r.storageWriteCount++
	if err != nil {
		r.storageWriteErrors++
	}
}

// MarkWebsocketReconnect увеличивает счётчик переподключений публичного WebSocket.
func (r *Registry) MarkWebsocketReconnect() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.websocketReconnects++
}

// SetReadiness сохраняет только ограниченное состояние ready или failed.
func (r *Registry) SetReadiness(ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ready {
		r.readinessState = "ready"
		return
	}
	r.readinessState = "failed"
}

// Handler возвращает HTTP-обработчик только для пути /metrics.
func (r *Registry) Handler() http.Handler { return registryHandler{registry: r} }

type registryHandler struct{ registry *Registry }

// ServeHTTP отдаёт фиксированные метрики и возвращает 404 для остальных путей.
func (h registryHandler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/metrics" {
		http.NotFound(w, request)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if _, err := io.WriteString(w, h.registry.metricsText()); err != nil {
		return
	}
}

// metricsText копирует состояние под read-lock и выдаёт только фиксированные метрики.
func (r *Registry) metricsText() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	lastSuccess := int64(0)
	if !r.lastMarketData.IsZero() {
		lastSuccess = r.lastMarketData.Unix()
	}
	readiness := r.readinessState
	if readiness == "" {
		readiness = "not_configured"
	}
	return fmt.Sprintf("bybit_bot_market_data_last_success_unixtime %d\n"+
		"bybit_bot_market_data_errors_total %d\n"+
		"bybit_bot_storage_write_errors_total %d\n"+
		"bybit_bot_storage_write_duration_seconds_sum %.9f\n"+
		"bybit_bot_storage_write_duration_seconds_count %d\n"+
		"bybit_bot_websocket_reconnects_total %d\n"+
		"bybit_bot_execution_readiness{state=%q} 1\n",
		lastSuccess, r.marketDataErrors, r.storageWriteErrors, r.storageWriteSeconds,
		r.storageWriteCount, r.websocketReconnects, readiness)
}
