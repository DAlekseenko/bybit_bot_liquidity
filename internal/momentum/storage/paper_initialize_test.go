package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/storage/pgtest"
)

// emptyPaperOwner описывает независимую сессию для области команды.
func emptyPaperOwner(scope ControlScope, session string) OwnerIdentity {
	return OwnerIdentity{Environment: scope.Environment, AccountID: scope.AccountID, StrategyID: scope.StrategyID, SessionID: session, Host: "fixture", ProcessID: 1}
}

// emptyPaperReject проверяет безопасный отказ без частичного результата.
func emptyPaperReject(t *testing.T, ctx context.Context, db *sql.DB, owner *ExecutionOwner, in PaperCommandRequest, want error) {
	t.Helper()
	got, err := InitializeEmptyPaperControl(ctx, db, owner, in)
	migrationResult(t, err, want)
	planRepositoryCheck(t, got == (PaperCommandState{}) && !strings.Contains(err.Error(), "momentum."), "частичный результат или сырой SQL")
}

// emptyPaperSnapshot читает все строки финансового, управляющего и календарного журнала.
func emptyPaperSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows := []string{lifecycleSnapshot(t, db)}
	for _, table := range []string{"control_revisions", "order_intents", "fills", "ledger_entries", "equity_snapshots", "risk_events"} {
		rows = append(rows, migrationValue(t, db, "SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r))::text,'[]') FROM momentum."+table+" r"))
	}
	return strings.Join(rows, "\n")
}

// TestEmptyPaperControlTakeover воспроизводит гибель первого владельца и обязательную последующую сверку.
func TestEmptyPaperControlTakeover(t *testing.T) {
	ctx := t.Context()
	db, in := paperCommandFixture(t)
	pool := completionPool(t, db)
	id := emptyPaperOwner(in.Scope, "first")
	old := acquireOwner(t, db, id, ownerT0)
	second := emptyPaperOwner(in.Scope, "second")
	_, err := AcquireExecutionOwner(ctx, pool, second, ownerT0)
	migrationResult(t, err, ErrOwnerBusy)
	migrationValue(t, pool, "SELECT count(*) FROM momentum.control_revisions", "0")
	var pid int
	migrationResult(t, old.conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid), nil)
	var killed bool
	migrationResult(t, pool.QueryRowContext(ctx, "SELECT pg_terminate_backend($1,5000)", pid).Scan(&killed), nil)
	planRepositoryCheck(t, killed, "старый backend жив")
	owner := acquireOwner(t, pool, second, ownerT0)
	emptyPaperReject(t, ctx, db, old, in, ErrOwnerLost)
	emptyPaperReject(t, ctx, db, owner, in, ErrPaperFenceDenied)
	foreign := in
	foreign.Scope.AccountID = "paper/other"
	emptyPaperReject(t, ctx, pool, owner, foreign, ErrPaperFenceDenied)
	planRepositoryCheck(t, owner.NeedsReconciliation(), "отсутствует latch takeover")
	got, err := InitializeEmptyPaperControl(ctx, pool, owner, in)
	migrationResult(t, err, nil)
	latest, err := ReadPaperCommandState(ctx, db, in.Scope)
	migrationResult(t, err, nil)
	planRepositoryCheck(t, got == latest && got.State == (ControlState{1, "ACTIVE"}) && got.ConfigHash == in.ConfigHash && got.CommandID == in.CommandID, "другая начальная ревизия")
	migrationValue(t, db, "SELECT count(*) FROM momentum.config_snapshots", "1")
	migrationValue(t, db, "SELECT count(*) FROM momentum.control_revisions", "1")
	migrationResult(t, owner.Permit(ctx), ErrOwnerReconciliationRequired)
	before := emptyPaperSnapshot(t, db)
	emptyPaperReject(t, ctx, pool, owner, in, ErrPaperFenceConflict)
	planRepositoryCheck(t, emptyPaperSnapshot(t, db) == before, "повтор изменил историю")
	facts, err := ReadPaperRunFacts(ctx, db, in.Scope, "")
	migrationResult(t, err, nil)
	stamp := PaperRecoveryStamp{Scope: in.Scope, ConfigHash: in.ConfigHash, RecoveryID: strings.Repeat("a", 64), OwnerSessionID: second.SessionID, ControlRevision: 1, DataHash: facts.DataHash}
	err = ReconcilePaperOwner(ctx, pool, owner, stamp, func(f PaperRunFacts) error {
		if f.HeadFound || f.DataHash != facts.DataHash || f.Control != latest {
			return ErrPaperFenceStale
		}
		return nil
	})
	migrationResult(t, err, nil)
	migrationResult(t, owner.Permit(ctx), nil)
}

// TestEmptyPaperControlArguments отклоняет неверный запрос, потерю строки и повреждённый control.
func TestEmptyPaperControlArguments(t *testing.T) {
	db, in := paperCommandFixture(t)
	pool := completionPool(t, db)
	ctx := t.Context()
	owner := acquireOwner(t, db, emptyPaperOwner(in.Scope, "first"), ownerT0)
	before := emptyPaperSnapshot(t, pool)
	stopped, stop := context.WithCancel(ctx)
	stop()
	emptyPaperReject(t, stopped, db, owner, in, context.Canceled)
	emptyPaperReject(t, nil, db, owner, in, ErrPaperFenceArgument)
	emptyPaperReject(t, ctx, nil, owner, in, ErrPaperFenceArgument)
	emptyPaperReject(t, ctx, db, nil, in, ErrPaperFenceArgument)
	invalid := in
	invalid.ExpectedRevision = 1
	emptyPaperReject(t, ctx, db, owner, invalid, ErrPaperFenceArgument)
	invalid = in
	invalid.Action = "HALT"
	emptyPaperReject(t, ctx, db, owner, invalid, ErrPaperFenceArgument)
	invalid = in
	invalid.Config = nil
	emptyPaperReject(t, ctx, db, owner, invalid, ErrControlArgument)
	invalid = in
	invalid.ConfigHash = strings.Repeat("0", 64)
	emptyPaperReject(t, ctx, db, owner, invalid, ErrControlArgument)
	paperSchemaExec(t, pool, "", "UPDATE momentum.execution_owner_sessions SET ended_at=last_seen_at, end_reason='CLEAN_SHUTDOWN' WHERE session_id='first'")
	emptyPaperReject(t, ctx, db, owner, in, ErrPaperFenceDenied)
	planRepositoryCheck(t, emptyPaperSnapshot(t, pool) == before, "неподтверждённый владелец создал данные")
	migrationResult(t, owner.Release(ctx, ownerT0), nil)
	owner = acquireOwner(t, db, emptyPaperOwner(in.Scope, "second"), ownerT0)
	paperRequire(t, db, in, nil)
	paperSchemaExec(t, db, "", "ALTER TABLE momentum.control_revisions DISABLE TRIGGER USER")
	paperSchemaExec(t, db, "", "UPDATE momentum.control_revisions SET reason='broken-json'")
	before = emptyPaperSnapshot(t, pool)
	emptyPaperReject(t, ctx, db, owner, in, ErrControl)
	planRepositoryCheck(t, emptyPaperSnapshot(t, pool) == before, "повреждённая ревизия перезаписана")
}

const emptyPaperEquitySQL = `INSERT INTO momentum.equity_snapshots VALUES ($1,$2,$3,now(),'SIMULATION',100,0,100,true)`

// TestEmptyPaperControlData проверяет откат поверх каждой независимо непустой области.
func TestEmptyPaperControlData(t *testing.T) {
	for _, kind := range []string{"bare", "skipped", "equity", "risk", "ledger"} {
		t.Run(kind, func(t *testing.T) {
			db, in := paperCommandFixture(t)
			pool := completionPool(t, db)
			scope := in.Scope
			owner := acquireOwner(t, db, emptyPaperOwner(scope, "owner"), ownerT0)
			switch kind {
			case "bare", "skipped":
				run := RebalanceRunInput{RunID: "bare", AccountID: scope.AccountID, StrategyID: scope.StrategyID, Config: *in.Config, ScheduledAt: ownerT0}
				_, err := CreateOrReadRebalanceRun(t.Context(), pool, run)
				migrationResult(t, err, nil)
				if kind == "skipped" {
					_, err = CompleteRebalanceRun(t.Context(), pool, RunCompletion{RunID: run.RunID, Outcome: RunSkipped, Reason: "data_deadline_missed"})
					migrationResult(t, err, nil)
				}
			case "equity":
				paperSchemaExec(t, pool, "", emptyPaperEquitySQL, scope.Environment, scope.AccountID, scope.StrategyID)
			case "risk":
				raw, _, err := in.Config.Snapshot()
				migrationResult(t, err, nil)
				paperSchemaExec(t, pool, "", "INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES(sha256($1::bytea),$1)", raw)
				paperSchemaExec(t, pool, "", `INSERT INTO momentum.risk_events(event_id,environment,account_id,strategy_id,kind,config_hash,detail,occurred_at)
 VALUES('stale',$1,$2,$3,'STALE_DATA',decode($4,'hex'),'test',now())`, scope.Environment, scope.AccountID, scope.StrategyID, in.ConfigHash)
			case "ledger":
				paperSchemaExec(t, pool, "", `INSERT INTO momentum.ledger_entries(environment,account_id,kind,source_id,amount_usdt,occurred_at)
 VALUES($1,$2,'TRANSFER','deposit',10,now())`, scope.Environment, scope.AccountID)
			}
			before := emptyPaperSnapshot(t, pool)
			emptyPaperReject(t, t.Context(), db, owner, in, ErrPaperFenceConflict)
			planRepositoryCheck(t, emptyPaperSnapshot(t, pool) == before, "изменены финансовые или управляющие данные")
		})
	}
}

// TestEmptyPaperControlBarrier дожидается реальной блокировки конкурентной записи перед проверкой пустоты.
func TestEmptyPaperControlBarrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	db, in := paperCommandFixture(t)
	observer := completionPool(t, db)
	owner := acquireOwner(t, db, emptyPaperOwner(in.Scope, "owner"), ownerT0)
	tx, err := observer.BeginTx(ctx, nil)
	migrationResult(t, err, nil)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, emptyPaperEquitySQL, in.Scope.Environment, in.Scope.AccountID, in.Scope.StrategyID)
	migrationResult(t, err, nil)
	var pid int
	migrationResult(t, owner.conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid), nil)
	finished := make(chan error, 1)
	go func() {
		got, err := InitializeEmptyPaperControl(ctx, db, owner, in)
		if got != (PaperCommandState{}) {
			err = errors.New("получено неподтверждённое состояние")
		}
		finished <- err
	}()
	j := &pgtest.Journal{T: t}
	waiting := j.Wait(ctx, tx.QueryRowContext, "momentum.equity_snapshots", "ShareLock", false, -1)
	planRepositoryCheck(t, waiting == pid, "проверка не выполняется на backend владельца")
	migrationValue(t, observer, "SELECT count(*) FROM momentum.control_revisions", "0")
	migrationValue(t, observer, "SELECT count(*) FROM momentum.config_snapshots", "0")
	migrationResult(t, tx.Commit(), nil)
	migrationResult(t, <-finished, ErrPaperFenceConflict)
	migrationValue(t, observer, "SELECT count(*) FROM momentum.control_revisions", "0")
	migrationValue(t, observer, "SELECT count(*) FROM momentum.config_snapshots", "0")
	migrationValue(t, observer, "SELECT count(*) FROM momentum.equity_snapshots", "1")
}

// TestEmptyPaperControlUnknownCommit различает потерянный ответ и независимо прочитанную принятую ревизию.
func TestEmptyPaperControlUnknownCommit(t *testing.T) {
	for _, mode := range []string{"commit", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			db, in := paperCommandFixture(t)
			other := completionPool(t, db)
			owner := acquireOwner(t, db, emptyPaperOwner(in.Scope, "owner"), ownerT0)
			paperSchemaExec(t, other, "", emptyPaperEquitySQL, in.Scope.Environment, in.Scope.AccountID, "other-strategy")
			finish := func(tx *sql.Tx) error {
				if mode == "commit" {
					migrationResult(t, tx.Commit(), nil)
				} else {
					migrationResult(t, tx.Rollback(), nil)
				}
				return errors.New("sensitive_sql_marker lost response")
			}
			got, err := initializeEmptyPaperControl(t.Context(), db, owner, in, finish)
			migrationResult(t, err, ErrControl)
			planRepositoryCheck(t, got == (PaperCommandState{}), "потерянный ответ не подтверждает запись")
			saved, err := ReadPaperCommandState(t.Context(), other, in.Scope)
			if mode == "commit" {
				migrationResult(t, err, nil)
				planRepositoryCheck(t, saved.ConfigHash == in.ConfigHash && saved.CommandID == in.CommandID && saved.State.Revision == 1, "неверный сохранённый результат")
				emptyPaperReject(t, t.Context(), db, owner, in, ErrPaperFenceConflict)
			} else {
				migrationResult(t, err, ErrControlAbsent)
				migrationValue(t, other, "SELECT count(*) FROM momentum.config_snapshots", "0")
				fresh, retry := InitializeEmptyPaperControl(t.Context(), db, owner, in)
				migrationResult(t, retry, nil)
				planRepositoryCheck(t, fresh.State.Revision == 1, "ошибка повтора после отката")
			}
		})
	}
}

// TestEmptyPaperControlUnavailable проверяет rollback при операционной ошибке после provisional ревизии.
func TestEmptyPaperControlUnavailable(t *testing.T) {
	db, in := paperCommandFixture(t)
	other := completionPool(t, db)
	owner := acquireOwner(t, db, emptyPaperOwner(in.Scope, "owner"), ownerT0)
	paperSchemaExec(t, other, "", "ALTER TABLE momentum.risk_events RENAME TO sensitive_sql_marker")
	emptyPaperReject(t, t.Context(), db, owner, in, ErrPaperFenceUnknown)
	migrationValue(t, other, "SELECT count(*) FROM momentum.control_revisions", "0")
	migrationValue(t, other, "SELECT count(*) FROM momentum.config_snapshots", "0")
}

// TestEmptyPaperControlPosted отклоняет полноценный цикл с уже подтверждённым исполнением и комиссией.
func TestEmptyPaperControlPosted(t *testing.T) {
	x := newEvidenceFixture(t)
	x.stage(t)
	fill := testFill(t, "partial", "1")
	result, err := RecordFill(t.Context(), x.db, x.scope.Environment, x.scope.AccountID, x.intent.OrderLinkID, fill)
	migrationResult(t, err, nil)
	planRepositoryCheck(t, result.Inserted, "исполнение не зафиксировано")
	observer := completionPool(t, x.db)
	migrationValue(t, observer, "SELECT count(*) FROM momentum.ledger_entries", "2")
	in := PaperCommandRequest{Scope: x.scope, ConfigHash: x.in.Header().ConfigHash, Action: "INITIALIZE",
		Actor: "SYSTEM", ActorID: "system/test", CommandID: "first", Kind: "initialize", Reason: "startup"}
	cfg := x.in.Values().Run.Config
	in.Config = &cfg
	owner := acquireOwner(t, x.db, emptyPaperOwner(x.scope, "owner"), ownerT0)
	before := emptyPaperSnapshot(t, observer)
	emptyPaperReject(t, t.Context(), x.db, owner, in, ErrPaperFenceConflict)
	planRepositoryCheck(t, emptyPaperSnapshot(t, observer) == before, "изменился исходный цикл, ledger или управление")
}
