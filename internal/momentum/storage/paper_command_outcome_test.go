package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
)

// TestCommandOutcome читает реально зафиксированную операторскую команду после потерянного ответа.
func TestCommandOutcome(t *testing.T) {
	db, init := paperCommandFixture(t)
	ctx := t.Context()
	scope := init.Scope
	first := paperRequire(t, db, init, nil)
	pause := paperNext(init, "PAUSE_ENTRIES", "pause", "pause", 1)
	savedPause := paperRequire(t, db, pause, nil)
	resume := paperNext(init, "RESUME", "resume", "resume", 2)
	resume.RecoveryID = "proof/recorded"
	var committed PaperCommandState
	lost, err := applyPaperCommandState(ctx, db, resume, func(tx *sql.Tx) error {
		var readErr error
		committed, readErr = readPaperCommand(ctx, tx, scope, resume.CommandID)
		migrationResult(t, readErr, nil)
		migrationResult(t, tx.Commit(), nil)
		return errors.New("соединение потеряно после COMMIT")
	})
	migrationResult(t, err, ErrControl)
	if lost != (PaperCommandState{}) {
		t.Fatal("после потери ответа producer вернул успех")
	}
	fresh := completionPool(t, db)
	got, found, err := ReadPaperCommandOutcome(ctx, fresh, scope, resume.CommandID)
	if err != nil || !found || !got.Matches(resume) || got.Applied != committed || got.Applied != got.Current || got.Applied.CreatedAt.IsZero() {
		t.Fatal("нет исходного подтверждения", got, err)
	}
	original := got.Applied
	paused, seen, e := ReadPaperCommandOutcome(ctx, fresh, scope, pause.CommandID)
	if e != nil || !seen || !paused.Matches(pause) || paused.Applied != savedPause || paused.Current != original {
		t.Fatal("потеряно историческое событие pause", e)
	}
	_, err = AppendRiskTransition(ctx, db, RiskTransition{Scope: scope, ExpectedRevision: 3, ConfigHash: init.ConfigHash, Action: "HALT", Reason: "data stale", OccurredAt: time.Now(), Events: []RiskEventRecord{{EventID: "risk", Kind: "STALE_DATA", Detail: "data stale"}}})
	migrationResult(t, err, nil)
	got, found, err = ReadPaperCommandOutcome(ctx, fresh, scope, resume.CommandID)
	current, readErr := ReadPaperCommandState(ctx, fresh, scope)
	if err != nil || readErr != nil || !found || got.Applied != original || got.Current != current || current.Actor != "RISK_CONTROLLER" || current.State != (ControlState{4, "HALTED"}) {
		t.Fatal("неверное историческое или текущее состояние", got, err, readErr)
	}
	for name, mutate := range map[string]func(*PaperCommandRequest){
		"scope":       func(q *PaperCommandRequest) { q.Scope.AccountID += "x" },
		"config":      func(q *PaperCommandRequest) { q.ConfigHash += "x" },
		"revision":    func(q *PaperCommandRequest) { q.ExpectedRevision++ },
		"command":     func(q *PaperCommandRequest) { q.CommandID += "x" },
		"actor":       func(q *PaperCommandRequest) { q.Actor = "SYSTEM" },
		"actorID":     func(q *PaperCommandRequest) { q.ActorID += "x" },
		"kind":        func(q *PaperCommandRequest) { q.Kind = "pause" },
		"action":      func(q *PaperCommandRequest) { q.Action = "HALT" },
		"reason":      func(q *PaperCommandRequest) { q.Reason += "x" },
		"recovery":    func(q *PaperCommandRequest) { q.RecoveryID += "x" },
		"confirmed":   func(q *PaperCommandRequest) { q.Confirmed = false },
		"INIT config": func(q *PaperCommandRequest) { q.Config = init.Config },
	} {
		candidate := resume
		mutate(&candidate)
		if got.Matches(candidate) {
			t.Fatal("принято изменённое поле", name)
		}
	}
	got, found, err = ReadPaperCommandOutcome(ctx, fresh, scope, init.CommandID)
	if err != nil || !found || got.Applied != first || !got.Matches(init) || got.Current != current {
		t.Fatal("потеряно событие INIT", got, err)
	}
	otherConfig := runRepositoryConfig(t, true)
	for _, changed := range []*config.Config{nil, &otherConfig} {
		candidate := init
		candidate.Config = changed
		if got.Matches(candidate) {
			t.Fatal("INIT принял чужой снимок")
		}
	}
	foreign := scope
	foreign.AccountID = "paper/other"
	for _, q := range []struct {
		scope ControlScope
		id    string
	}{{foreign, resume.CommandID}, {scope, "missing"}} {
		got, found, err = ReadPaperCommandOutcome(ctx, fresh, q.scope, q.id)
		if err != nil || found || got != (PaperCommandOutcome{}) {
			t.Fatal("не подтверждено отсутствие", got, found, err)
		}
	}
	for i, kind := range []string{"halt", "close-all"} {
		command := paperNext(init, "HALT", kind, kind, uint64(4+i))
		saved := paperRequire(t, db, command, nil)
		out, seen, e := ReadPaperCommandOutcome(ctx, fresh, scope, command.CommandID)
		if e != nil || !seen || !out.Matches(command) || out.Applied != saved || out.Current != saved {
			t.Fatal("неверная операторская команда", kind, e)
		}
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.control_revisions", "6")
	migrationValue(t, db, "SELECT count(*) FROM momentum.risk_events", "1")
	for _, table := range []string{"order_intents", "fills", "ledger_entries", "paper_run_inputs"} {
		migrationValue(t, db, "SELECT count(*) FROM momentum."+table, "0")
	}
	damage := lifecycleMutationTx(t, ctx, db)
	executionRowsExec(t, damage, 1, `UPDATE momentum.control_revisions SET confirmation_id='halt',
 reason=replace(reason,'"command_id":"close-all"','"command_id":"halt"') WHERE revision=6`)
	migrationResult(t, damage.Commit(), nil)
	got, found, err = ReadPaperCommandOutcome(ctx, fresh, scope, "halt")
	if found || got != (PaperCommandOutcome{}) || !errors.Is(err, ErrPaperCommandOutcomeCorrupt) {
		t.Fatal("неоднозначный ID подтверждён", err)
	}
}

// TestCommandOutcomeFailure различает отсутствующую команду, повреждённый audit и недоступное текущее состояние.
func TestCommandOutcomeFailure(t *testing.T) {
	db, init := paperCommandFixture(t)
	ctx := t.Context()
	first := paperRequire(t, db, init, nil)
	stop := paperNext(init, "HALT", "halt", "halt", 1)
	paperRequire(t, db, stop, nil)
	_, err := db.ExecContext(ctx, "ALTER TABLE momentum.control_revisions DISABLE TRIGGER control_revisions_immutable")
	migrationResult(t, err, nil)
	executionRowsExec(t, db, 1, "UPDATE momentum.control_revisions SET reason='broken' WHERE revision=2")
	got, found, err := ReadPaperCommandOutcome(ctx, db, init.Scope, init.CommandID)
	if !found || got.Applied != first || got.Current != (PaperCommandState{}) || !errors.Is(err, ErrPaperCommandCurrentUnavailable) || !errors.Is(err, ErrPaperCommandOutcomeCorrupt) {
		t.Fatal("потеряно подтверждённое событие", got, err)
	}
	got, found, err = ReadPaperCommandOutcome(ctx, db, init.Scope, stop.CommandID)
	if found || got != (PaperCommandOutcome{}) || !errors.Is(err, ErrPaperCommandOutcomeCorrupt) {
		t.Fatal("повреждённая строка признана отсутствующей", err)
	}
	executionRowsExec(t, db, 1, "UPDATE momentum.control_revisions SET actor='RECONCILIATION',confirmation_id=NULL,reason='other' WHERE revision=2")
	got, found, err = ReadPaperCommandOutcome(ctx, db, init.Scope, init.CommandID)
	if !found || got.Applied != first || got.Current != (PaperCommandState{}) || !errors.Is(err, ErrPaperCommandOutcomeCorrupt) || !errors.Is(err, ErrPaperCommandCurrentUnavailable) {
		t.Fatal("недопустимый legacy принят", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	got, found, err = ReadPaperCommandOutcome(cancelled, db, init.Scope, init.CommandID)
	if found || got != (PaperCommandOutcome{}) || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrPaperCommandOutcomeUnavailable) {
		t.Fatal("неявное подтверждение отменённого чтения", err)
	}
	between, cancelSecond := context.WithCancel(ctx)
	defer cancelSecond()
	q := &outcomeBarrier{db: db, beforeCurrent: cancelSecond}
	partial, seen, err := readPaperCommandOutcome(between, q, init.Scope, init.CommandID)
	if !seen || partial.Scope != init.Scope || partial.Applied != first || partial.Current != (PaperCommandState{}) || !errors.Is(err, ErrPaperCommandOutcomeUnavailable) || !errors.Is(err, ErrPaperCommandCurrentUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatal("не различены отмена и старое событие", partial, err)
	}
	damage := lifecycleMutationTx(t, ctx, db)
	executionRowsExec(t, damage, 1, "UPDATE momentum.config_snapshots SET payload=payload||payload")
	migrationResult(t, damage.Commit(), nil)
	got, found, err = ReadPaperCommandOutcome(ctx, db, init.Scope, init.CommandID)
	if found || got != (PaperCommandOutcome{}) || !errors.Is(err, ErrPaperCommandOutcomeCorrupt) {
		t.Fatal("неканонический снимок принят", err)
	}
	migrationResult(t, db.Close(), nil)
	for _, id := range []string{"", " stray"} {
		got, found, err = ReadPaperCommandOutcome(ctx, db, init.Scope, id)
		if found || got != (PaperCommandOutcome{}) || !errors.Is(err, ErrControlArgument) {
			t.Fatal("аргумент прошёл в БД", err)
		}
	}
	got, found, err = ReadPaperCommandOutcome(ctx, db, init.Scope, init.CommandID)
	if found || got != (PaperCommandOutcome{}) || !errors.Is(err, ErrPaperCommandOutcomeUnavailable) {
		t.Fatal("подтверждение недоступного readback", err)
	}
}

// TestCommandOutcomeArgument отклоняет небумажные области и неполный адрес до SQL.
func TestCommandOutcomeArgument(t *testing.T) {
	scope := ControlScope{"paper", "paper/test", "strategy"}
	db := new(sql.DB)
	for _, tc := range []struct {
		ctx   context.Context
		db    *sql.DB
		scope ControlScope
		id    string
	}{
		{nil, db, scope, "id"},
		{t.Context(), nil, scope, "id"},
		{t.Context(), db, ControlScope{"testnet", "paper/test", "strategy"}, "id"},
		{t.Context(), db, scope, ""},
		{t.Context(), db, scope, " id"},
	} {
		got, found, err := ReadPaperCommandOutcome(tc.ctx, tc.db, tc.scope, tc.id)
		if found || got != (PaperCommandOutcome{}) || !errors.Is(err, ErrControlArgument) {
			t.Fatal("неполный адрес", got, err)
		}
	}
}

type outcomeBarrier struct {
	db            *sql.DB
	beforeCurrent func()
}

// QueryRowContext выполняет второй реальный запрос после управляемого изменения между statement.
func (q *outcomeBarrier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if args[3] == "" {
		q.beforeCurrent()
	}
	return q.db.QueryRowContext(ctx, query, args...)
}

// TestCommandOutcomeChangedLatest проверяет исчезновение, регресс и замену подтверждённой ревизии.
func TestCommandOutcomeChangedLatest(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		rows        int64
	}{
		{"absent", "DELETE FROM momentum.control_revisions", 2},
		{"regression", "DELETE FROM momentum.control_revisions WHERE revision=2", 1},
		{"replacement", "UPDATE momentum.control_revisions SET created_at=created_at+interval '1 second' WHERE revision=2", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, init := paperCommandFixture(t)
			paperRequire(t, db, init, nil)
			command := paperNext(init, "HALT", "halt", "halt", 1)
			applied := paperRequire(t, db, command, nil)
			reader := &outcomeBarrier{db: db, beforeCurrent: func() {
				tx := lifecycleMutationTx(t, t.Context(), db)
				executionRowsExec(t, tx, tc.rows, tc.query)
				migrationResult(t, tx.Commit(), nil)
			}}
			got, found, err := readPaperCommandOutcome(t.Context(), reader, init.Scope, command.CommandID)
			if !found || got != (PaperCommandOutcome{Scope: init.Scope, Applied: applied}) ||
				!errors.Is(err, ErrPaperCommandCurrentUnavailable) || !errors.Is(err, ErrPaperCommandOutcomeCorrupt) {
				t.Fatal("не подтверждён исходный audit при изменении latest", err)
			}
			if tc.name == "absent" {
				migrationResult(t, err, ErrControlAbsent)
			}
		})
	}
}
