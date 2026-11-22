package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var testControlScope = ControlScope{Environment: "testnet", AccountID: "acc", StrategyID: "momentum-v1"}

// controlDecimal разбирает точное значение для тестов управления.
func controlDecimal(t *testing.T, s string) *model.Decimal {
	t.Helper()
	d, err := model.ParseAmount(s)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

// haltTransition собирает переход HALT с событием EQUITY_STOP поверх expected.
func haltTransition(t *testing.T, expected uint64, eventID string) RiskTransition {
	t.Helper()
	cfg := sha256.Sum256([]byte("cfg"))
	return RiskTransition{Scope: testControlScope, ExpectedRevision: expected, Action: "HALT", Reason: "equity stop",
		ConfigHash: hex.EncodeToString(cfg[:]), OccurredAt: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
		Events: []RiskEventRecord{{EventID: eventID, Kind: "EQUITY_STOP", Detail: "equity <= threshold",
			ThresholdUSDT: controlDecimal(t, "90"), EquityUSDT: controlDecimal(t, "89.5")}}}
}

// TestAppendRiskTransitionArguments проверяет отказ на некорректном входе без PostgreSQL.
func TestAppendRiskTransitionArguments(t *testing.T) {
	db, _ := sql.Open("postgres", "postgres://invalid.invalid/none")
	defer db.Close()
	bad := []func(*RiskTransition){
		func(tr *RiskTransition) { tr.Action = "RESUME" },
		func(tr *RiskTransition) { tr.ConfigHash = "zz" },
		func(tr *RiskTransition) { tr.ExpectedRevision = 0 },
		func(tr *RiskTransition) { tr.OccurredAt = time.Time{} },
		func(tr *RiskTransition) { tr.Events = nil },
		func(tr *RiskTransition) { tr.Scope.AccountID = " " },
	}
	for i, mutate := range bad {
		tr := haltTransition(t, 1, "e")
		mutate(&tr)
		if _, err := AppendRiskTransition(context.Background(), db, tr); !errors.Is(err, ErrControlArgument) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	if _, err := LatestControlState(context.Background(), nil, testControlScope); !errors.Is(err, ErrControlArgument) {
		t.Fatalf("nil db: %v", err)
	}
	if a, b, ok := optionalWire(nil, &model.Decimal{}); ok || a != nil || b != nil {
		t.Fatal("invalid decimal accepted")
	}
}

// TestAppendRiskTransition проверяет атомарную запись HALT, конфликт повтора и откат.
func TestAppendRiskTransition(t *testing.T) {
	db := controlDatabase(t)
	ctx := context.Background()
	if _, err := LatestControlState(ctx, db, testControlScope); !errors.Is(err, ErrControlAbsent) {
		t.Fatalf("absent: %v", err)
	}
	if _, err := AppendRiskTransition(ctx, db, haltTransition(t, 1, "e1")); !errors.Is(err, ErrControlConflict) {
		t.Fatalf("no chain: %v", err)
	}
	marketStatement(t, db, controlInsert+controlRow(1, "ACTIVE", "INITIALIZE", "SYSTEM", "NULL", "NULL"), true)

	injected := errors.New("injected")
	_, err := appendRiskTransition(ctx, db, haltTransition(t, 1, "e1"), func(*sql.Tx) error { return injected })
	if !errors.Is(err, ErrControl) {
		t.Fatalf("injected: %v", err)
	}
	invalid := haltTransition(t, 1, "e1")
	invalid.Events[0].EquityUSDT = controlDecimal(t, "91")
	if _, err := AppendRiskTransition(ctx, db, invalid); !errors.Is(err, ErrControlArgument) {
		t.Fatalf("equity above threshold: %v", err)
	}
	migrationValue(t, db, "SELECT count(*)::text FROM momentum.control_revisions", "1")
	migrationValue(t, db, "SELECT count(*)::text FROM momentum.risk_events", "0")

	pause := haltTransition(t, 1, "p1")
	pause.Action, pause.Events = "PAUSE_ENTRIES", []RiskEventRecord{{EventID: "p1", Kind: "STALE_DATA", Detail: "no snapshot"}}
	got, err := AppendRiskTransition(ctx, db, pause)
	if err != nil || got != (ControlState{Revision: 2, Mode: "PAUSED_ENTRIES"}) {
		t.Fatalf("pause = %+v, %v", got, err)
	}
	got, err = AppendRiskTransition(ctx, db, haltTransition(t, 2, "e1"))
	if err != nil || got != (ControlState{Revision: 3, Mode: "HALTED"}) {
		t.Fatalf("halt = %+v, %v", got, err)
	}
	if _, err := AppendRiskTransition(ctx, db, haltTransition(t, 2, "e2")); !errors.Is(err, ErrControlConflict) {
		t.Fatalf("stale expected: %v", err)
	}
	latest, err := LatestControlState(ctx, db, testControlScope)
	if err != nil || latest != got {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	migrationValue(t, db, "SELECT count(*)::text FROM momentum.risk_events", "2")
	migrationValue(t, db, `SELECT kind||':'||control_revision||':'||threshold_usdt||':'||equity_usdt
 FROM momentum.risk_events WHERE event_id='e1'`, "EQUITY_STOP:3:90:89.5")
}
