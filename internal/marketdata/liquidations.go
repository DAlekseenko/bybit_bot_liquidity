package marketdata

import (
	"sync"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

// LiquidationWindow хранит события ликвидаций за ограниченный промежуток времени.
type LiquidationWindow struct {
	mu        sync.Mutex
	retention time.Duration
	events    []exchange.Liquidation
}

func NewLiquidationWindow(retention time.Duration) *LiquidationWindow {
	return &LiquidationWindow{retention: retention}
}

func (w *LiquidationWindow) Add(liquidation exchange.Liquidation) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.events = append(w.events, liquidation)
}

// Recent удаляет события старше now-retention и возвращает оставшиеся события.
// Событие ровно на границе окна сохраняется. Возвращаемый срез является копией,
// поэтому вызывающий код может изменять его без блокировки и влияния на окно.
func (w *LiquidationWindow) Recent(now time.Time) []exchange.Liquidation {
	w.mu.Lock()
	defer w.mu.Unlock()

	cutoff := now.Add(-w.retention)
	recentEvents := w.events[:0]
	for _, event := range w.events {
		if !event.Time.Before(cutoff) {
			recentEvents = append(recentEvents, event)
		}
	}
	w.events = recentEvents

	recent := make([]exchange.Liquidation, len(w.events))
	copy(recent, w.events)
	return recent
}
