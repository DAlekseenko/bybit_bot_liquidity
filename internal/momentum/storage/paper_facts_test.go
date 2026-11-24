package storage

import (
	"context"
	"database/sql"
	"errors"

	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// factsRead проверяет ответ публичного reader и сохраняет DTO для проверки полей.
func factsRead(t *testing.T, x evidenceFixture, db *sql.DB, run string) PaperRunFacts {
	t.Helper()
	f, err := ReadPaperRunFacts(t.Context(), db, x.scope, run)
	migrationResult(t, err, nil)
	return f
}

// newFactsFixture регистрирует актуальную конфигурацию области через штатный INIT.
func newFactsFixture(t *testing.T) evidenceFixture {
	t.Helper()
	x := newEvidenceFixture(t)
	cfg := x.in.Values().Run.Config
	paperRequire(t, x.db, PaperCommandRequest{Scope: x.scope, Config: &cfg, ConfigHash: x.in.Header().ConfigHash, Action: "INITIALIZE",
		Actor: "SYSTEM", ActorID: "system/test", CommandID: "init", Kind: "initialize", Reason: "genesis"}, nil)
	return x
}

// factsEffects проверяет конкретное состояние, число исполнений и запись комиссии.
func factsEffects(t *testing.T, f PaperRunFacts, id, state string, count int) {
	t.Helper()
	if f.Intents()[id].State != state || len(f.Fills()) != count || count > 0 && f.Fills()["partial"].FeeUSDT != "0.1" {
		t.Fatalf("состояние %s, исполнений %d", f.Intents()[id].State, len(f.Fills()))
	}
}

// factsPartial проверяет обе границы видимости через реальный RecordFill и SQL lock wait.
func factsPartial(t *testing.T, x evidenceFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool := completionPool(t, x.db)
	id := x.intent.IntentID
	before := factsRead(t, x, pool, "")
	fill := testFill(t, "partial", "1")
	_, err := recordFill(ctx, x.db, "paper", x.scope.AccountID, id, fill, func(tx *sql.Tx) error {
		own, e := readPaperRunFacts(ctx, tx, x.scope, "")
		migrationResult(t, e, nil)
		factsEffects(t, own, id, "PARTIALLY_FILLED", 1)
		outside := factsRead(t, x, pool, "")
		factsEffects(t, outside, id, "UNKNOWN", 0)
		if own.DataHash == before.DataHash || outside.DataHash != before.DataHash {
			t.Fatal("provisional hash")
		}
		return errors.New("rollback")
	})
	migrationResult(t, err, ErrExecution)
	if factsRead(t, x, pool, "").DataHash != before.DataHash {
		t.Fatal("rollback hash")
	}
	lock := candleReadTx(t, x.db)
	_, err = lock.ExecContext(ctx, "LOCK TABLE momentum.execution_owner_sessions IN ACCESS EXCLUSIVE MODE")
	migrationResult(t, err, nil)
	type result struct {
		f   PaperRunFacts
		err error
	}
	done := make(chan result, 1)
	go func() { f, e := ReadPaperRunFacts(ctx, pool, x.scope, ""); done <- result{f, e} }()
	waitPaperBlock(t, ctx, x.db, "momentum.execution_owner_sessions", "AccessShareLock")
	res, err := RecordFill(ctx, x.db, "paper", x.scope.AccountID, id, fill)
	if err != nil || !res.Inserted {
		t.Fatal(res, err)
	}
	migrationResult(t, lock.Commit(), nil)
	r := <-done
	migrationResult(t, r.err, nil)
	factsEffects(t, r.f, id, "UNKNOWN", 0)
	after := factsRead(t, x, pool, "")
	factsEffects(t, after, id, "PARTIALLY_FILLED", 1)
	if before.DataHash != r.f.DataHash || after.DataHash == before.DataHash {
		t.Fatal("snapshot hash")
	}
}

// TestPaperFactsRF1RF3RF4 проверяет полный цикл записи, переключение снимка и историю.
func TestPaperFactsRF1RF3RF4(t *testing.T) {
	x := newFactsFixture(t)
	ctx, id := t.Context(), x.intent.IntentID
	read := func() PaperRunFacts { return factsRead(t, x, x.db, "") }
	f := read()
	planRepositoryCheck(t, !f.HeadFound && f.RunID == "" && f.OwnerSessionID == "" && f.Control.Actor == "SYSTEM" &&
		f.DataHash == "9d8fb8ea6480d908986464e2f29c9bf8577ae90bcb225bf2d8330ba0faf858d6", "genesis")
	run := factsRead(t, x, x.db, x.intent.RunID)
	planRepositoryCheck(t, run.RunID == x.intent.RunID && !run.InputFound && !run.Completed && !run.ScopeHasIntents, "run-only")
	_, err := StorePaperInput(ctx, x.db, x.in)
	migrationResult(t, err, nil)
	f = read()
	planRepositoryCheck(t, f.HeadFound && f.Head == x.in.Header() && f.InputFound && !f.PlanFound && !f.Completed, "input-before-plan")
	_, err = StorePlanRevision(ctx, x.db, x.in.Values().Plan)
	migrationResult(t, err, nil)
	f = read()
	planRepositoryCheck(t, f.PlanFound && f.Plan.Hash() == x.intent.PlanHash && len(f.Intents()) == 0, "plan-before-intent")
	migrationResult(t, RecordOrderIntent(ctx, x.db, x.intent), nil)
	pending := read()
	factsEffects(t, pending, id, "PENDING", 0)
	migrationResult(t, SetOrderIntentState(ctx, x.db, "paper", x.scope.AccountID, id, "UNKNOWN"), nil)
	f = read()
	factsEffects(t, f, id, "UNKNOWN", 0)
	planRepositoryCheck(t, f.DataHash != pending.DataHash, "смена state")
	factsPartial(t, x)
	partial := read()
	result, err := RecordFill(ctx, x.db, "paper", x.scope.AccountID, id, testFill(t, "full", "1"))
	migrationResult(t, err, nil)
	f = read()
	factsEffects(t, f, id, "FILLED", 2)
	planRepositoryCheck(t, result.Inserted && f.DataHash != partial.DataHash && f.Fills()["full"].Quantity == "1", "full fill")
	c, receipt := paperInputCompletion(t, x.db, x.in, "COMPLETED")
	saved, err := CompleteRebalanceRun(ctx, x.db, c)
	migrationResult(t, err, nil)
	pool := completionPool(t, x.db)
	f = factsRead(t, x, pool, "")
	evidence := x.read(t, pool)
	planRepositoryCheck(t, f.Completed && f.Completion == saved && f.Completion == evidence.Lifecycle.Completion && len(c.Reason) > 10000 &&
		f.Completion.Reason == c.Reason && !f.Completion.CompletedAt.Equal(x.in.Header().ScheduledAt) && f.DataHash == evidence.DataHash &&
		f.Input.Hash() == x.in.Hash() && bytes.Equal(f.Input.Payload(), x.in.Payload()), "durable receipt и вход")
	second := paperInputFixture(t, x.db, 2, x.in, receipt, x.scope.AccountID, true)
	_, err = StorePaperInput(ctx, x.db, second)
	migrationResult(t, err, nil)
	before := lifecycleSnapshot(t, x.db)
	const effects = `SELECT (SELECT jsonb_agg(i ORDER BY intent_id) FROM momentum.order_intents i)::text ||
 (SELECT jsonb_agg(f ORDER BY execution_id) FROM momentum.fills f)::text ||
 (SELECT jsonb_agg(l ORDER BY kind,source_id) FROM momentum.ledger_entries l)::text ||
 (SELECT jsonb_agg(r ORDER BY revision) FROM momentum.control_revisions r)::text`
	rows := migrationValue(t, x.db, effects)
	old := factsRead(t, x, pool, x.intent.RunID)
	planRepositoryCheck(t, old.RunID == x.intent.RunID && old.Head == second.Header() && old.Input.Hash() == x.in.Hash() &&
		old.Plan.Hash() == x.intent.PlanHash && old.DataHash == f.DataHash && old.Completion == saved && lifecycleSnapshot(t, x.db) == before, "исторический run")
	migrationValue(t, x.db, effects, rows)
}

// TestPaperFactsOutcomes различает durable ABORTED и обычный SKIPPED.
func TestPaperFactsOutcomes(t *testing.T) {
	for _, outcome := range []RunOutcome{RunFailed, RunSkipped} {
		x := newFactsFixture(t)
		c := RunCompletion{RunID: x.intent.RunID, Outcome: outcome, Reason: "обычный пропуск"}
		if outcome == RunFailed {
			_, err := StorePaperInput(t.Context(), x.db, x.in)
			migrationResult(t, err, nil)
			c, _ = paperInputCompletion(t, x.db, x.in, "ABORTED")
		}
		saved, err := CompleteRebalanceRun(t.Context(), x.db, c)
		migrationResult(t, err, nil)
		f := factsRead(t, x, completionPool(t, x.db), c.RunID)
		planRepositoryCheck(t, f.Completed && f.Completion == saved && f.InputFound == (outcome == RunFailed) && f.HeadFound == f.InputFound &&
			f.PlanFound == f.InputFound && !f.ScopeHasIntents && len(f.Fills()) == 0, "исход")
	}
}

// factsError проверяет нулевой DTO, безопасные категории и отсутствие текста SQL.
func factsError(t *testing.T, f PaperRunFacts, err error, kinds ...error) {
	t.Helper()
	planRepositoryCheck(t, reflect.ValueOf(f).IsZero() && err != nil && !strings.Contains(err.Error(), "momentum."), "частичный DTO или SQL")
	for _, kind := range kinds {
		migrationResult(t, err, kind)
	}
}

// TestPaperFactsRF2 проверяет повреждения в отдельных disposable базах через публичный reader.
func TestPaperFactsRF2(t *testing.T) {
	const control = "UPDATE momentum.control_revisions SET "
	const siblingPlan = `INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,payload,payload_sha256)
 VALUES('paper-run-2',1,sha256('plan'::bytea),'plan',sha256('plan'::bytea))`
	const siblingIntent = `INSERT INTO momentum.order_intents(intent_id,environment,account_id,order_link_id,run_id,plan_revision,plan_hash,symbol,side,reduce_only,quantity)
 VALUES('sibling','paper','paper/journal','sibling','paper-run-2',1,sha256('plan'::bytea),'BTC','BUY',false,1)`
	for _, tc := range []struct {
		name, query string
		stage       bool
		count       int64
		cause       error
	}{
		{"missing-config", control + "config_hash=sha256('absent'::bytea)", false, 1, errPaperCommandSemantic},
		{"damaged-config", "UPDATE momentum.config_snapshots SET payload=payload||payload WHERE config_hash=(SELECT config_hash FROM momentum.control_revisions)", false, 1, errPaperCommandSemantic},
		{"malformed-audit", control + "reason='bad'", false, 1, errPaperCommandSemantic},
		{"invalid-legacy", control + "confirmation_id=NULL,actor='RISK_CONTROLLER',reason='legacy'", false, 1, ErrPaperFactsCorrupt},
		{"scope-config", control + "config_hash=(SELECT config_hash FROM momentum.rebalance_runs WHERE run_id='paper-run-2')", false, 1, ErrPaperFactsCorrupt},
		{"receipt", "UPDATE momentum.run_completions SET reason='broken'", true, 1, ErrPaperRunLifecycleCorrupt},
		{"empty-sibling-plan", siblingPlan, false, 1, ErrPaperFactsCorrupt},
		{"empty-sibling-intent", siblingIntent, false, 1, ErrPaperExecutionRowsCorrupt},
		{"sibling-plan", siblingPlan, true, 1, ErrPaperFactsCorrupt},
		{"sibling-intent", siblingIntent, true, 1, ErrPaperFactsCorrupt},
		{"wrong-id", "UPDATE momentum.order_intents SET intent_id='other'", true, 1, ErrPaperRunEvidenceCorrupt},
		{"ledger", "DELETE FROM momentum.ledger_entries WHERE kind='FEE'", true, 1, ErrPaperExecutionRowsCorrupt},
		{"cap", `INSERT INTO momentum.order_intents(intent_id,environment,account_id,order_link_id,run_id,plan_revision,plan_hash,symbol,side,reduce_only,quantity)
 SELECT 'i'||n,environment,account_id,'l'||n,run_id,plan_revision,plan_hash,symbol,side,reduce_only,quantity
 FROM momentum.order_intents CROSS JOIN generate_series(1,256)n`, true, 256, paperwire.ErrPaperResourceLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newFactsFixture(t)
			paperInputFixture(t, x.db, 2, x.in, x.in.Hash(), x.scope.AccountID, true)
			if tc.stage {
				x.stage(t)
				_, err := RecordFill(t.Context(), x.db, "paper", x.scope.AccountID, x.intent.IntentID, testFill(t, "partial", "1"))
				migrationResult(t, err, nil)
				c, _ := paperInputCompletion(t, x.db, x.in, "COMPLETED")
				_, err = CompleteRebalanceRun(t.Context(), x.db, c)
				migrationResult(t, err, nil)
			}
			tx := lifecycleMutationTx(t, t.Context(), x.db)
			executionRowsExec(t, tx, tc.count, tc.query)
			migrationResult(t, tx.Commit(), nil)
			f, err := ReadPaperRunFacts(t.Context(), x.db, x.scope, x.intent.RunID)
			factsError(t, f, err, ErrPaperFactsCorrupt, tc.cause)
		})
	}
}

// TestPaperFactsControlOwner сверяет актуальные control/owner отдельно от digest.
func TestPaperFactsControlOwner(t *testing.T) {
	x := newFactsFixture(t)
	x.stage(t)
	ctx := t.Context()
	// Воспроизводим сохранённый INIT до появления command audit.
	tx := lifecycleMutationTx(t, ctx, x.db)
	executionRowsExec(t, tx, 1, "UPDATE momentum.control_revisions SET confirmation_id=NULL,reason='legacy'")
	migrationResult(t, tx.Commit(), nil)
	initial := factsRead(t, x, x.db, "")
	planRepositoryCheck(t, initial.Control.Actor == "SYSTEM" && initial.Control.ActorID == "" && initial.Control.State.Mode == "ACTIVE", "legacy init")
	for i, action := range []string{"PAUSE_ENTRIES", "HALT"} {
		tr := haltTransition(t, uint64(i+1), action)
		tr.Scope, tr.ConfigHash, tr.Action = x.scope, x.in.Header().ConfigHash, action
		want, err := AppendRiskTransition(ctx, x.db, tr)
		migrationResult(t, err, nil)
		got := factsRead(t, x, x.db, "")
		planRepositoryCheck(t, got.Control.State == want && got.Control.Action == action && got.Control.Actor == "RISK_CONTROLLER" &&
			got.Control.ActorID == "" && got.DataHash == initial.DataHash && got.OwnerSessionID == "", "legacy risk transition")
	}
	for _, account := range []string{"paper/foreign", x.scope.AccountID} {
		id := OwnerIdentity{account, "paper", account, x.scope.StrategyID, "host", 1}
		conn := ownerSessionConn(t, x.db)
		ownerSessionSQL(t, conn, "SELECT pg_advisory_lock($1)", ExecutionOwnerLockKey(id))
		_, err := RegisterExecutionOwnerSession(ctx, conn, id, time.Now())
		migrationResult(t, err, nil)
		got := factsRead(t, x, x.db, "")
		want := ""
		if account == x.scope.AccountID {
			want = id.SessionID
		}
		planRepositoryCheck(t, got.OwnerSessionID == want && got.DataHash == initial.DataHash && got.Control.State.Mode == "HALTED", "scope owner")
	}
}

// TestPaperFactsErrors разделяет реальные отказы SQL, аргументы и отсутствие в области.
func TestPaperFactsErrors(t *testing.T) {
	x := newFactsFixture(t)
	ctx := t.Context()
	check := func(call context.Context, db *sql.DB, s ControlScope, run string, kinds ...error) {
		f, err := ReadPaperRunFacts(call, db, s, run)
		factsError(t, f, err, kinds...)
	}
	foreign := paperInputFixture(t, x.db, 2, x.in, x.in.Hash(), "paper/foreign", true)
	check(ctx, x.db, x.scope, foreign.Header().RunID, ErrPaperFactsAbsent, ErrPaperRunEvidenceAbsent)
	check(ctx, x.db, x.scope, "missing", ErrPaperFactsAbsent, ErrPaperRunEvidenceAbsent)
	check(ctx, x.db, ControlScope(foreign.Header().Scope), "", ErrPaperFactsAbsent, ErrControlAbsent)
	check(nil, x.db, x.scope, "", ErrPaperFactsArgument)
	check(ctx, nil, x.scope, "", ErrPaperFactsArgument)
	check(ctx, x.db, ControlScope{}, "", ErrPaperFactsArgument)
	check(ctx, x.db, x.scope, "\x00bad", ErrPaperFactsArgument)
	for _, call := range []context.Context{ctx, nil} {
		f, err := readPaperRunFacts(call, nil, x.scope, "")
		factsError(t, f, err, ErrPaperFactsArgument)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	check(cancelled, x.db, x.scope, "", context.Canceled, ErrPaperFactsUnavailable)
	expired, stop := context.WithDeadline(ctx, time.Unix(1, 0))
	defer stop()
	check(expired, x.db, x.scope, "", context.DeadlineExceeded, ErrPaperFactsUnavailable)
	tx := candleReadTx(t, x.db)
	migrationResult(t, tx.Rollback(), nil)
	f, err := readPaperRunFacts(ctx, tx, x.scope, "")
	factsError(t, f, err, ErrPaperFactsUnavailable, ErrPaperInputUnavailable)
	pool := completionPool(t, x.db)
	migrationResult(t, pool.Close(), nil)
	check(ctx, pool, x.scope, "", ErrPaperFactsUnavailable)
	marketStatement(t, x.db, "ALTER TABLE momentum.execution_owner_sessions RENAME TO unavailable", true)
	check(ctx, x.db, x.scope, "", ErrPaperFactsUnavailable)
}
