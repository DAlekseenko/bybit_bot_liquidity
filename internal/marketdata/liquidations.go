package marketdata

import (
	"sync"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

// LiquidationWindow хранит события ликвидаций за ограниченный промежуток времени.
type LiquidationWindow struct {
	mu            sync.Mutex
	leaseChanged  *sync.Cond
	activeLeases  int
	transitioning bool
	retention     time.Duration
	events        []exchange.Liquidation
	generation    uint64
	coverageStart time.Time
	clock         func() time.Time
}

// LiquidationSnapshot атомарно описывает окно, его поколение и готовность.
type LiquidationSnapshot struct {
	Events     []exchange.Liquidation
	Generation uint64
	Ready      bool
}

// NewLiquidationWindow создаёт окно с системными часами.
func NewLiquidationWindow(retention time.Duration) *LiquidationWindow {
	return NewLiquidationWindowWithClock(retention, time.Now)
}

// NewLiquidationWindowWithClock создаёт окно с внедрённым источником времени.
func NewLiquidationWindowWithClock(retention time.Duration, clock func() time.Time) *LiquidationWindow {
	if clock == nil {
		clock = time.Now
	}
	window := &LiquidationWindow{retention: retention, clock: clock}
	window.leaseChanged = sync.NewCond(&window.mu)
	return window
}

// BeginGeneration очищает окно и выдаёт токен нового потока.
func (w *LiquidationWindow) BeginGeneration() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.transitioning = true
	for w.activeLeases > 0 {
		w.leaseChanged.Wait()
	}
	w.generation++
	w.events = nil
	w.coverageStart = time.Time{}
	w.transitioning = false
	return w.generation
}

// Add сохраняет начальные события до запуска generation-scoped потока.
func (w *LiquidationWindow) Add(liquidation exchange.Liquidation) {
	w.AddForGeneration(0, liquidation, w.clock())
}

// AddForGeneration принимает событие только от актуального поколения.
func (w *LiquidationWindow) AddForGeneration(generation uint64, liquidation exchange.Liquidation, observedAt time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.transitioning || generation != w.generation {
		return false
	}
	if w.coverageStart.IsZero() {
		w.coverageStart = observedAt
	}
	w.events = append(w.events, liquidation)
	return true
}

// Reset очищает события и меняет поколение после разрыва WebSocket.
func (w *LiquidationWindow) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.transitioning = true
	for w.activeLeases > 0 {
		w.leaseChanged.Wait()
	}
	w.generation++
	w.events = nil
	w.coverageStart = time.Time{}
	w.transitioning = false
}

// Recent удаляет старые события и возвращает копию оставшихся.
func (w *LiquidationWindow) Recent(now time.Time) []exchange.Liquidation {
	return w.Snapshot(now).Events
}

// Snapshot возвращает копию окна и атомарный статус его покрытия.
func (w *LiquidationWindow) Snapshot(now time.Time) LiquidationSnapshot {
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
	ready := !w.coverageStart.IsZero() && !now.Before(w.coverageStart.Add(w.retention))
	return LiquidationSnapshot{Events: recent, Generation: w.generation, Ready: ready}
}

// AcquireReadyGeneration фиксирует поколение до завершения обработки snapshot.
func (w *LiquidationWindow) AcquireReadyGeneration(generation uint64, now time.Time) (func(), bool) {
	w.mu.Lock()
	if w.transitioning || w.generation != generation || w.coverageStart.IsZero() || now.Before(w.coverageStart.Add(w.retention)) {
		w.mu.Unlock()
		return nil, false
	}
	w.activeLeases++
	w.mu.Unlock()

	var releaseOnce sync.Once
	return func() {
		releaseOnce.Do(func() {
			w.mu.Lock()
			w.activeLeases--
			w.leaseChanged.Broadcast()
			w.mu.Unlock()
		})
	}, true
}
