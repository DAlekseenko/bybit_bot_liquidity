package risk

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var testNow = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

// amount разбирает точное значение USDT для тестов.
func amount(t *testing.T, s string) model.Decimal {
	t.Helper()
	d, err := model.ParseAmount(s)
	if err != nil {
		t.Fatalf("ParseAmount(%q): %v", s, err)
	}
	return d
}

// snapshot возвращает свежий снимок equity за минуту до testNow.
func snapshot(t *testing.T, equity string) *EquitySnapshot {
	t.Helper()
	return &EquitySnapshot{ObservedAt: testNow.Add(-time.Minute), EquityUSDT: amount(t, equity), DataFresh: true}
}

// stopParams возвращает параметры с включённым порогом.
func stopParams(t *testing.T, threshold string) Params {
	t.Helper()
	return Params{EquityStop: EquityStop{Enabled: true, Threshold: amount(t, threshold)}, MaxSnapshotAge: 5 * time.Minute}
}

// TestEvaluateTransitions проверяет таблицу режимов, причин и липкость HALT.
func TestEvaluateTransitions(t *testing.T) {
	stale := snapshot(t, "100")
	stale.ObservedAt = testNow.Add(-6 * time.Minute)
	notFresh := snapshot(t, "100")
	notFresh.DataFresh = false
	future := snapshot(t, "100")
	future.ObservedAt = testNow.Add(time.Second)
	edge := snapshot(t, "100")
	edge.ObservedAt = testNow.Add(-5 * time.Minute)
	staleLow := snapshot(t, "1")
	staleLow.DataFresh = false
	disabled := Params{EquityStop: EquityStop{Threshold: amount(t, "1000")}, MaxSnapshotAge: 5 * time.Minute}
	p := stopParams(t, "90.5")
	tests := []struct {
		name    string
		params  Params
		in      Input
		action  Action
		mode    Mode
		reasons []Reason
	}{
		{"healthy", p, Input{Mode: ModeActive, Equity: snapshot(t, "100")}, ActionNone, ModeActive, nil},
		{"age boundary fresh", p, Input{Mode: ModeActive, Equity: edge}, ActionNone, ModeActive, nil},
		{"disabled stop never fires", disabled, Input{Mode: ModeActive, Equity: snapshot(t, "1")}, ActionNone, ModeActive, nil},
		{"equal threshold halts", p, Input{Mode: ModeActive, Equity: snapshot(t, "90.5")}, ActionHalt, ModeHalted, []Reason{ReasonEquityStop}},
		{"below threshold halts", p, Input{Mode: ModeActive, Equity: snapshot(t, "-3")}, ActionHalt, ModeHalted, []Reason{ReasonEquityStop}},
		{"paused escalates to halt", p, Input{Mode: ModePausedEntries, Equity: snapshot(t, "90"), LockLost: true},
			ActionHalt, ModeHalted, []Reason{ReasonEquityStop, ReasonLockLost}},
		{"missing snapshot pauses", p, Input{Mode: ModeActive}, ActionPauseEntries, ModePausedEntries, []Reason{ReasonStaleData}},
		{"old snapshot pauses", p, Input{Mode: ModeActive, Equity: stale}, ActionPauseEntries, ModePausedEntries, []Reason{ReasonStaleData}},
		{"unfresh flag pauses", p, Input{Mode: ModeActive, Equity: notFresh}, ActionPauseEntries, ModePausedEntries, []Reason{ReasonStaleData}},
		{"future snapshot pauses", p, Input{Mode: ModeActive, Equity: future}, ActionPauseEntries, ModePausedEntries, []Reason{ReasonStaleData}},
		{"stale low equity is not a stop", p, Input{Mode: ModeActive, Equity: staleLow}, ActionPauseEntries, ModePausedEntries, []Reason{ReasonStaleData}},
		{"unknown state pauses", p, Input{Mode: ModeActive, Equity: snapshot(t, "100"), UnknownPosition: true, UnknownOrder: true,
			LockLost: true, ReconciliationMismatch: true}, ActionPauseEntries, ModePausedEntries,
			[]Reason{ReasonUnknownPosition, ReasonUnknownOrder, ReasonLockLost, ReasonReconciliationMismatch}},
		{"paused stays paused", p, Input{Mode: ModePausedEntries}, ActionNone, ModePausedEntries, nil},
		{"paused recovery keeps pause", p, Input{Mode: ModePausedEntries, Equity: snapshot(t, "100")}, ActionNone, ModePausedEntries, nil},
		{"halted below stays", p, Input{Mode: ModeHalted, Equity: snapshot(t, "1")}, ActionNone, ModeHalted, nil},
		{"halted recovery stays", p, Input{Mode: ModeHalted, Equity: snapshot(t, "1000")}, ActionNone, ModeHalted, nil},
		{"halted stale stays", p, Input{Mode: ModeHalted}, ActionNone, ModeHalted, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.Now = testNow
			got, err := Evaluate(tc.params, tc.in)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if got.Action != tc.action || got.Mode != tc.mode || !reflect.DeepEqual(got.Reasons, tc.reasons) {
				t.Fatalf("got %+v, want %s %s %v", got, tc.action, tc.mode, tc.reasons)
			}
			if got.EntriesAllowed() != (tc.mode == ModeActive) || (got.Stop != nil) != (got.Action == ActionHalt) {
				t.Fatalf("entries/stop mismatch: %+v", got)
			}
		})
	}
}

// TestEvaluateStopOnceAndSticky проверяет однократный HALT, его повтор и сохранение при восстановлении.
func TestEvaluateStopOnceAndSticky(t *testing.T) {
	p := stopParams(t, "90")
	first, err := Evaluate(p, Input{Mode: ModeActive, Now: testNow, Equity: snapshot(t, "89.99")})
	if err != nil || first.Action != ActionHalt || first.Stop == nil {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if c, _ := first.Stop.EquityUSDT.Cmp(amount(t, "89.99")); c != 0 {
		t.Fatalf("stop equity mismatch")
	}
	if c, _ := first.Stop.ThresholdUSDT.Cmp(amount(t, "90")); c != 0 {
		t.Fatalf("stop threshold mismatch")
	}
	for _, equity := range []string{"89.99", "50", "1000"} {
		again, err := Evaluate(p, Input{Mode: first.Mode, Now: testNow, Equity: snapshot(t, equity)})
		if err != nil || again.Action != ActionNone || again.Mode != ModeHalted || again.EntriesAllowed() {
			t.Fatalf("repeat with %s = %+v, %v", equity, again, err)
		}
	}
}

// TestEvaluateRejectsInvalidInput проверяет fail-closed ошибки входа и параметров.
func TestEvaluateRejectsInvalidInput(t *testing.T) {
	good := stopParams(t, "1")
	noAge := good
	noAge.MaxSnapshotAge = 0
	badThreshold := Params{EquityStop: EquityStop{Enabled: true}, MaxSnapshotAge: time.Minute}
	tests := []struct {
		name   string
		params Params
		in     Input
	}{
		{"unknown mode", good, Input{Mode: "RUNNING", Now: testNow}},
		{"zero now", good, Input{Mode: ModeActive}},
		{"no max age", noAge, Input{Mode: ModeActive, Now: testNow}},
		{"enabled without threshold", badThreshold, Input{Mode: ModeActive, Now: testNow}},
		{"snapshot without equity", good, Input{Mode: ModeActive, Now: testNow, Equity: &EquitySnapshot{ObservedAt: testNow}}},
		{"snapshot without time", good, Input{Mode: ModeActive, Now: testNow, Equity: &EquitySnapshot{EquityUSDT: amount(t, "1")}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Evaluate(tc.params, tc.in)
			if !errors.Is(err, ErrInvalidInput) || !reflect.DeepEqual(got, Decision{}) || got.EntriesAllowed() {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}
