package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"
)

var ownerT0 = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

// ownerDatabase создаёт базу с полным каталогом миграций.
func ownerDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	return db
}

// ownerID строит identity процесса для области testnet/acc/strategy.
func ownerID(session, strategy string) OwnerIdentity {
	return OwnerIdentity{SessionID: session, Environment: "testnet", AccountID: "acc", StrategyID: strategy, Host: "host", ProcessID: 7}
}

// acquireOwner захватывает владение и регистрирует освобождение в cleanup.
func acquireOwner(t *testing.T, db *sql.DB, id OwnerIdentity, now time.Time) *ExecutionOwner {
	t.Helper()
	owner, err := AcquireExecutionOwner(context.Background(), db, id, now)
	migrationResult(t, err, nil)
	t.Cleanup(func() { _ = owner.Release(context.Background(), now) })
	return owner
}

// ownerHistory возвращает историю сессий области в порядке session_id.
const ownerHistory = "SELECT string_agg(session_id||':'||coalesce(end_reason,'active'),',' ORDER BY session_id) " +
	"FROM momentum.execution_owner_sessions WHERE strategy_id='s'"

// TestExecutionOwnerSingle проверяет одного владельца на область и чистую передачу.
func TestExecutionOwnerSingle(t *testing.T) {
	ctx := context.Background()
	db := ownerDatabase(t)
	a := acquireOwner(t, db, ownerID("a", "s"), ownerT0)
	migrationResult(t, a.Permit(ctx), nil)
	_, err := AcquireExecutionOwner(ctx, db, ownerID("b", "s"), ownerT0)
	migrationResult(t, err, ErrOwnerBusy)
	acquireOwner(t, db, ownerID("c", "other"), ownerT0)
	migrationResult(t, a.Heartbeat(ctx, ownerT0.Add(-time.Second)), ErrOwnerArgument)
	migrationResult(t, a.Heartbeat(ctx, ownerT0.Add(time.Minute)), nil)
	migrationResult(t, a.Release(ctx, ownerT0.Add(2*time.Minute)), nil)
	<-a.Done()
	migrationResult(t, a.Permit(ctx), ErrOwnerLost)
	b := acquireOwner(t, db, ownerID("b", "s"), ownerT0.Add(3*time.Minute))
	planRepositoryCheck(t, !b.NeedsReconciliation(), "clean handover must not require reconciliation")
	migrationResult(t, b.Permit(ctx), nil)
	migrationValue(t, db, ownerHistory, "a:CLEAN_SHUTDOWN,b:active")
	_, err = AcquireExecutionOwner(ctx, db, OwnerIdentity{SessionID: "x", Environment: "testnet"}, ownerT0)
	migrationResult(t, err, ErrOwnerArgument)
}

// TestExecutionOwnerLockLoss проверяет, что потеря соединения запрещает отправку и требует сверки у преемника.
func TestExecutionOwnerLockLoss(t *testing.T) {
	ctx := context.Background()
	db := ownerDatabase(t)
	a := acquireOwner(t, db, ownerID("a", "s"), ownerT0)
	var pid int
	migrationResult(t, a.conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid), nil)
	marketStatement(t, db, "SELECT pg_terminate_backend("+strconv.Itoa(pid)+")", true)
	migrationResult(t, a.Heartbeat(ctx, ownerT0.Add(time.Minute)), ErrOwnerLost)
	select {
	case <-a.Done():
	default:
		t.Fatal("Done must close on lock loss")
	}
	migrationResult(t, a.Permit(ctx), ErrOwnerLost)
	migrationResult(t, a.MarkReconciled("rec"), ErrOwnerLost)
	migrationValue(t, db, ownerHistory, "a:LOCK_LOST")
	b := acquireOwner(t, db, ownerID("b", "s"), ownerT0.Add(2*time.Minute))
	planRepositoryCheck(t, b.NeedsReconciliation(), "takeover after lock loss must require reconciliation")
	migrationResult(t, b.Permit(ctx), ErrOwnerReconciliationRequired)
	migrationResult(t, b.MarkReconciled(" "), ErrOwnerArgument)
	migrationResult(t, b.MarkReconciled("rec-1"), nil)
	migrationResult(t, b.Permit(ctx), nil)
}

// TestExecutionOwnerStaleTakeover проверяет захват после упавшего процесса с сохранением истории.
func TestExecutionOwnerStaleTakeover(t *testing.T) {
	ctx := context.Background()
	db := ownerDatabase(t)
	marketStatement(t, db, `INSERT INTO momentum.execution_owner_sessions(session_id,environment,account_id,strategy_id,
 host,process_id,started_at,last_seen_at) VALUES ('crashed','testnet','acc','s','old',1,'2026-01-04+00','2026-01-04 12:00+00')`, true)
	b := acquireOwner(t, db, ownerID("b", "s"), ownerT0)
	planRepositoryCheck(t, b.NeedsReconciliation(), "stale takeover must require reconciliation")
	migrationResult(t, b.Permit(ctx), ErrOwnerReconciliationRequired)
	migrationValue(t, db, ownerHistory, "b:active,crashed:STALE_TAKEOVER")
	migrationValue(t, db, "SELECT ended_at='2026-01-05+00' FROM momentum.execution_owner_sessions WHERE session_id='crashed'", "true")
}

// TestExecutionOwnerParallel проверяет, что из параллельных кандидатов владельцем становится ровно один.
func TestExecutionOwnerParallel(t *testing.T) {
	db := ownerDatabase(t)
	results := make(chan *ExecutionOwner, 8)
	for i := 0; i < 8; i++ {
		go func(i int) {
			owner, err := AcquireExecutionOwner(context.Background(), db, ownerID("p"+strconv.Itoa(i), "s"), ownerT0)
			if err != nil && !errors.Is(err, ErrOwnerBusy) {
				t.Errorf("candidate %d: %v", i, err)
			}
			results <- owner
		}(i)
	}
	won := 0
	for i := 0; i < 8; i++ {
		if owner := <-results; owner != nil {
			won++
			defer owner.Release(context.Background(), ownerT0)
		}
	}
	planRepositoryCheck(t, won == 1, "exactly one candidate must own the scope, got "+strconv.Itoa(won))
	migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions WHERE ended_at IS NULL", "1")
}
