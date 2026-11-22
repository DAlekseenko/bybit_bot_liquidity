package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/risk"
	"bybit_bot_liquidity/internal/momentum/storage"
)

// fakeStream возвращает заданную свежесть WS.
type fakeStream bool

// Fresh возвращает значение.
func (s fakeStream) Fresh() bool { return bool(s) }

// fakeBalance отдаёт equity на время сервера или ошибку.
type fakeBalance struct {
	equity string
	at     time.Time
	err    error
	calls  int
	clock  *time.Time // если задан, запрос сдвигает часы на 300 мс (задержка REST)
}

// GetMomentumIsolatedBalance возвращает заданный баланс.
func (b *fakeBalance) GetMomentumIsolatedBalance(context.Context) (model.IsolatedBalance, error) {
	b.calls++
	if b.clock != nil {
		*b.clock = b.clock.Add(300 * time.Millisecond)
	}
	if b.err != nil {
		return model.IsolatedBalance{}, b.err
	}
	eq, _ := model.ParseAmount(b.equity)
	return model.IsolatedBalance{Equity: eq, ServerTime: b.at}, nil
}

// fakeControl хранит режим и число сохранённых переходов.
type fakeControl struct {
	state     storage.ControlState
	readErr   error
	conflicts int
	appended  []storage.RiskTransition
}

// LatestControlState возвращает режим или ошибку БД.
func (c *fakeControl) LatestControlState(context.Context) (storage.ControlState, error) {
	return c.state, c.readErr
}

// AppendRiskTransition сохраняет переход или возвращает конфликт ревизии.
func (c *fakeControl) AppendRiskTransition(_ context.Context, tr storage.RiskTransition) (storage.ControlState, error) {
	if c.conflicts > 0 {
		c.conflicts--
		c.state = storage.ControlState{Revision: c.state.Revision + 1, Mode: "HALTED"}
		return storage.ControlState{}, storage.ErrControlConflict
	}
	c.appended = append(c.appended, tr)
	mode := map[string]string{"HALT": "HALTED", "PAUSE_ENTRIES": "PAUSED_ENTRIES"}[tr.Action]
	c.state = storage.ControlState{Revision: tr.ExpectedRevision + 1, Mode: mode}
	return c.state, nil
}

var t0 = time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

// newMonitor собирает монитор с порогом 8000 и max age 1 минута.
func newMonitor(t *testing.T, stream bool, balance *fakeBalance, control *fakeControl, clock *time.Time) *Monitor {
	threshold, _ := model.ParseAmount("8000")
	balance.clock = clock
	return &Monitor{Clock: func() time.Time { return *clock }, Params: risk.Params{EquityStop: risk.EquityStop{Enabled: true, Threshold: threshold}, MaxSnapshotAge: time.Minute},
		Stream: fakeStream(stream), Balance: balance, Control: control, Poll: 30 * time.Second,
		Scope: storage.ControlScope{Environment: "testnet", AccountID: "acc", StrategyID: "s"}, ConfigHash: "ab"}
}

// TestMonitorFallbackDetectsMissedEvent проверяет, что REST-опрос находит пропущенное WS-событие и HALT сохраняется один раз.
func TestMonitorFallbackDetectsMissedEvent(t *testing.T) {
	clock := t0
	balance := &fakeBalance{equity: "9000", at: t0}
	control := &fakeControl{state: storage.ControlState{Revision: 1, Mode: "ACTIVE"}}
	m := newMonitor(t, true, balance, control, &clock)
	eq, _ := model.ParseAmount("9000")
	m.ObserveEquity(eq, t0)
	if d, err := m.tickAt(&clock, t0.Add(time.Second), Flags{}); err != nil || !d.EntriesAllowed() {
		t.Fatalf("healthy tick: %+v %v", d, err)
	}
	balance.equity, balance.at = "7999", t0.Add(40*time.Second) // WS-событие о падении equity пропущено
	d, err := m.tickAt(&clock, t0.Add(41*time.Second), Flags{})
	if err != nil || d.Action != risk.ActionHalt || len(control.appended) != 1 || control.appended[0].Events[0].Kind != "EQUITY_STOP" {
		t.Fatalf("fallback halt: %+v %v %+v", d, err, control.appended)
	}
	if d, _ = m.tickAt(&clock, t0.Add(42*time.Second), Flags{}); d.Action != risk.ActionNone || len(control.appended) != 1 || d.EntriesAllowed() {
		t.Fatalf("threshold must persist once: %+v", d)
	}
	restarted := newMonitor(t, true, &fakeBalance{equity: "20000", at: t0.Add(50 * time.Second)}, control, &clock)
	if d, _ = restarted.tickAt(&clock, t0.Add(51*time.Second), Flags{}); d.EntriesAllowed() || d.Mode != risk.ModeHalted {
		t.Fatalf("restart must keep halt: %+v", d)
	}
}

// TestMonitorBlocks проверяет запрет входов при устаревших данных, сбое БД и гонке ревизий.
func TestMonitorBlocks(t *testing.T) {
	clock := t0
	control := &fakeControl{state: storage.ControlState{Revision: 1, Mode: "ACTIVE"}}
	m := newMonitor(t, false, &fakeBalance{err: errors.New("rest down")}, control, &clock)
	eq, _ := model.ParseAmount("9000")
	m.ObserveEquity(eq, t0)
	d, err := m.tickAt(&clock, t0.Add(time.Second), Flags{})
	if err != nil || d.Action != risk.ActionPauseEntries || d.EntriesAllowed() {
		t.Fatalf("stale quotes must pause entries: %+v %v", d, err)
	}
	down := newMonitor(t, true, &fakeBalance{equity: "9000", at: t0}, &fakeControl{readErr: errors.New("db down")}, &clock)
	if d, err = down.tickAt(&clock, t0, Flags{}); !errors.Is(err, ErrMonitor) || d.EntriesAllowed() {
		t.Fatalf("db disconnect must block entries: %+v %v", d, err)
	}
	race := &fakeControl{state: storage.ControlState{Revision: 1, Mode: "ACTIVE"}, conflicts: 1}
	m = newMonitor(t, true, &fakeBalance{equity: "7000", at: t0}, race, &clock)
	if d, err = m.tickAt(&clock, t0.Add(time.Second), Flags{}); err != nil || d.Action != risk.ActionNone || d.Mode != risk.ModeHalted || len(race.appended) != 0 {
		t.Fatalf("revision race must re-read and not duplicate: %+v %v", d, err)
	}
	flagged := newMonitor(t, true, &fakeBalance{equity: "9000", at: t0}, &fakeControl{state: storage.ControlState{Revision: 1, Mode: "ACTIVE"}}, &clock)
	if d, _ = flagged.tickAt(&clock, t0.Add(time.Second), Flags{UnknownOrder: true}); d.Action != risk.ActionPauseEntries {
		t.Fatalf("unknown order must pause entries: %+v", d)
	}
}

// tickAt выставляет часы и выполняет Tick.
func (m *Monitor) tickAt(clock *time.Time, at time.Time, flags Flags) (risk.Decision, error) {
	*clock = at
	return m.Tick(context.Background(), flags)
}

// TestMonitorRESTAfterRequest проверяет, что ответ REST со временем позже начала Tick не считается устаревшим.
func TestMonitorRESTAfterRequest(t *testing.T) {
	clock := t0
	control := &fakeControl{state: storage.ControlState{Revision: 1, Mode: "ACTIVE"}}
	m := newMonitor(t, false, &fakeBalance{equity: "9000", at: t0.Add(200 * time.Millisecond)}, control, &clock)
	if d, err := m.Tick(context.Background(), Flags{}); err != nil || d.Action != risk.ActionNone || !d.EntriesAllowed() {
		t.Fatalf("healthy first poll must not pause: %+v %v", d, err)
	}
	m = newMonitor(t, false, &fakeBalance{equity: "7000", at: t0.Add(200 * time.Millisecond)}, &fakeControl{state: storage.ControlState{Revision: 1, Mode: "ACTIVE"}}, &clock)
	if d, _ := m.Tick(context.Background(), Flags{}); d.Action != risk.ActionHalt {
		t.Fatalf("below threshold must halt, not pause: %+v", d)
	}
}
