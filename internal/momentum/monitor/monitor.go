// Package monitor поддерживает свежий risk snapshot по событиям private WS и
// резервному REST-опросу баланса и сохраняет переходы risk controller в БД.
package monitor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/risk"
	"bybit_bot_liquidity/internal/momentum/storage"
)

// ErrMonitor — общая категория ошибок монитора; при ней входы запрещены.
var ErrMonitor = errors.New("momentum risk monitor failed")

// Stream сообщает свежесть private WS (bybit.PrivateStreamState).
type Stream interface{ Fresh() bool }

// Balance — резервный REST-источник equity (bybit.Client.GetMomentumIsolatedBalance).
type Balance interface {
	GetMomentumIsolatedBalance(ctx context.Context) (model.IsolatedBalance, error)
}

// Control читает режим и сохраняет переходы (storage).
type Control interface {
	LatestControlState(ctx context.Context) (storage.ControlState, error)
	AppendRiskTransition(ctx context.Context, tr storage.RiskTransition) (storage.ControlState, error)
}

// Flags — признаки сверки, известные вызывающему на момент Tick.
type Flags struct {
	UnknownPosition, UnknownOrder, LockLost, ReconciliationMismatch bool
}

// Monitor объединяет WS-снимки, REST fallback и risk.Evaluate.
type Monitor struct {
	Params     risk.Params
	Stream     Stream
	Balance    Balance
	Control    Control
	Scope      storage.ControlScope
	ConfigHash string
	Poll       time.Duration    // интервал резервного REST-опроса, даже при живом WS
	Clock      func() time.Time // nil — time.Now

	mu       sync.Mutex
	snapshot *risk.EquitySnapshot
	polledAt time.Time
}

// ObserveEquity принимает equity из события баланса private WS.
func (m *Monitor) ObserveEquity(equity model.Decimal, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snapshot == nil || at.After(m.snapshot.ObservedAt) {
		m.snapshot = &risk.EquitySnapshot{ObservedAt: at.UTC(), EquityUSDT: equity, DataFresh: true}
	}
}

// Tick оценивает риск по часам Clock. Режим всегда читается из БД, поэтому
// рестарт не снимает HALT; ошибка БД — запрет входов. Время оценки берётся после
// REST-запроса: ответ сервера не считается «из будущего». Предложенный переход
// сохраняется один раз: повторный Tick в новом режиме даёт ActionNone.
func (m *Monitor) Tick(ctx context.Context, flags Flags) (risk.Decision, error) {
	state, err := m.Control.LatestControlState(ctx)
	if err != nil {
		return blocked(), errors.Join(ErrMonitor, err)
	}
	snapshot := m.refresh(ctx, m.now())
	now := m.now()
	for attempt := 0; attempt < 2; attempt++ {
		decision, err := risk.Evaluate(m.Params, risk.Input{Mode: risk.Mode(state.Mode), Now: now, Equity: snapshot,
			UnknownPosition: flags.UnknownPosition, UnknownOrder: flags.UnknownOrder, LockLost: flags.LockLost,
			ReconciliationMismatch: flags.ReconciliationMismatch})
		if err != nil || decision.Action == risk.ActionNone {
			if err != nil {
				return blocked(), errors.Join(ErrMonitor, err)
			}
			return decision, nil
		}
		next, err := m.Control.AppendRiskTransition(ctx, m.transition(state, decision, now))
		if err == nil {
			decision.Mode = risk.Mode(next.Mode)
			return decision, nil
		}
		if !errors.Is(err, storage.ErrControlConflict) {
			return blocked(), errors.Join(ErrMonitor, err)
		}
		if state, err = m.Control.LatestControlState(ctx); err != nil {
			return blocked(), errors.Join(ErrMonitor, err)
		}
	}
	return blocked(), ErrMonitor
}

// refresh опрашивает REST, если WS не свеж, снимка нет или прошёл интервал Poll;
// берётся более свежий из WS и REST снимков. Сбой REST при несвежем WS даёт
// несвежий снимок — risk controller запретит входы.
func (m *Monitor) refresh(ctx context.Context, now time.Time) *risk.EquitySnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	wsFresh := m.Stream != nil && m.Stream.Fresh()
	if !wsFresh || m.snapshot == nil || now.Sub(m.polledAt) >= m.Poll {
		m.polledAt = now
		if b, err := m.Balance.GetMomentumIsolatedBalance(ctx); err == nil && !b.ServerTime.IsZero() {
			if m.snapshot == nil || b.ServerTime.After(m.snapshot.ObservedAt) {
				m.snapshot = &risk.EquitySnapshot{ObservedAt: b.ServerTime.UTC(), EquityUSDT: b.Equity, DataFresh: true}
			}
			return m.snapshot
		}
		if m.snapshot != nil && !wsFresh {
			stale := *m.snapshot
			stale.DataFresh = false
			return &stale
		}
	}
	return m.snapshot
}

// transition строит сохраняемый переход с детерминированными ID событий.
func (m *Monitor) transition(state storage.ControlState, d risk.Decision, now time.Time) storage.RiskTransition {
	tr := storage.RiskTransition{Scope: m.Scope, ExpectedRevision: state.Revision, Action: string(d.Action),
		Reason: string(d.Reasons[0]), ConfigHash: m.ConfigHash, OccurredAt: now.UTC()}
	for _, reason := range d.Reasons {
		sum := sha256.Sum256([]byte(m.Scope.Environment + "\x00" + m.Scope.AccountID + "\x00" + m.Scope.StrategyID +
			"\x00" + strconv.FormatUint(state.Revision+1, 10) + "\x00" + string(reason)))
		event := storage.RiskEventRecord{EventID: "risk-" + hex.EncodeToString(sum[:16]), Kind: string(reason), Detail: string(reason)}
		if reason == risk.ReasonEquityStop && d.Stop != nil {
			threshold, equity := d.Stop.ThresholdUSDT, d.Stop.EquityUSDT
			event.ThresholdUSDT, event.EquityUSDT = &threshold, &equity
		}
		tr.Events = append(tr.Events, event)
	}
	return tr
}

// now возвращает время по инжектированным часам.
func (m *Monitor) now() time.Time {
	if m.Clock == nil {
		return time.Now().UTC()
	}
	return m.Clock().UTC()
}

// blocked — решение, запрещающее входы при сбое монитора.
func blocked() risk.Decision { return risk.Decision{Mode: risk.ModePausedEntries} }

// DBControl реализует Control через функции storage для одной области.
type DBControl struct {
	DB    *sql.DB
	Scope storage.ControlScope
}

// LatestControlState делегирует storage.LatestControlState.
func (c DBControl) LatestControlState(ctx context.Context) (storage.ControlState, error) {
	return storage.LatestControlState(ctx, c.DB, c.Scope)
}

// AppendRiskTransition делегирует storage.AppendRiskTransition.
func (c DBControl) AppendRiskTransition(ctx context.Context, tr storage.RiskTransition) (storage.ControlState, error) {
	return storage.AppendRiskTransition(ctx, c.DB, tr)
}
