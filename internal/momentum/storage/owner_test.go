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

// TestExecutionOwnerReleaseEdgeCases проверяет перенос требования сверки через выход и снятие lock при отменённом контексте.
func TestExecutionOwnerReleaseEdgeCases(t *testing.T) {
	ctx := context.Background()
	db := ownerDatabase(t)
	marketStatement(t, db, `INSERT INTO momentum.execution_owner_sessions(session_id,environment,account_id,strategy_id,
 host,process_id,started_at,last_seen_at) VALUES ('a','testnet','acc','s','old',1,'2026-01-04+00','2026-01-04+00')`, true)
	b, err := AcquireExecutionOwner(ctx, db, ownerID("b", "s"), ownerT0)
	migrationResult(t, err, nil)
	migrationResult(t, b.Release(ctx, ownerT0.Add(time.Minute)), nil)
	c := acquireOwner(t, db, ownerID("c", "s"), ownerT0.Add(2*time.Minute))
	planRepositoryCheck(t, c.NeedsReconciliation(), "unreconciled takeover must not become clean on release")
	migrationResult(t, c.MarkReconciled("rec"), nil)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	migrationResult(t, c.Release(canceled, ownerT0.Add(3*time.Minute)), ErrOwnerLost)
	d, err := AcquireExecutionOwner(ctx, db, ownerID("d", "s"), ownerT0.Add(4*time.Minute))
	migrationResult(t, err, nil)
	defer d.Release(ctx, ownerT0.Add(5*time.Minute))
	planRepositoryCheck(t, d.NeedsReconciliation(), "release without confirmed clean shutdown must require reconciliation")
	migrationValue(t, db, ownerHistory, "a:STALE_TAKEOVER,b:STALE_TAKEOVER,c:LOCK_LOST,d:active")
}

// TestExecutionOwnerClockSkew проверяет, что преемник с отстающими часами не скрывает упавшего владельца.
func TestExecutionOwnerClockSkew(t *testing.T) {
	ctx := context.Background()
	db := ownerDatabase(t)
	a := acquireOwner(t, db, ownerID("a", "s"), ownerT0)
	migrationResult(t, a.Release(ctx, ownerT0), nil)
	b := acquireOwner(t, db, ownerID("b", "s"), ownerT0.Add(-time.Minute))
	planRepositoryCheck(t, !b.NeedsReconciliation(), "clean predecessor must not require reconciliation")
	migrationResult(t, b.Heartbeat(ctx, ownerT0.Add(-30*time.Second)), nil)
	migrationValue(t, db, "SELECT (SELECT started_at FROM momentum.execution_owner_sessions WHERE session_id='b') >"+
		" (SELECT started_at FROM momentum.execution_owner_sessions WHERE session_id='a')", "true")
	var pid int
	migrationResult(t, b.conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid), nil)
	marketStatement(t, db, "SELECT pg_terminate_backend("+strconv.Itoa(pid)+")", true)
	c := acquireOwner(t, db, ownerID("c", "s"), ownerT0.Add(-2*time.Minute))
	planRepositoryCheck(t, c.NeedsReconciliation(), "crashed predecessor with earlier clock must require reconciliation")
	migrationResult(t, c.Permit(ctx), ErrOwnerReconciliationRequired)
	migrationValue(t, db, ownerHistory, "a:CLEAN_SHUTDOWN,b:STALE_TAKEOVER,c:active")
}
