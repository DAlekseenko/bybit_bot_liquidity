package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

// ownerSessionIdentity возвращает неизменяемые поля тестового процесса.
func ownerSessionIdentity() OwnerIdentity {
	return OwnerIdentity{"session-1", "testnet", "acc", "momentum-v1", "host-a", 101}
}

// ownerSessionConn выделяет соединение, не возвращая удержанные locks в pool.
func ownerSessionConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock_all()")
		_ = conn.Close()
	})
	return conn
}

// ownerSessionSQL выполняет проверяемую операцию на dedicated соединении.
func ownerSessionSQL(t *testing.T, conn *sql.Conn, query string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

// TestOwnerSessionArguments проверяет точную hash-область и отказ до любого I/O.
func TestOwnerSessionArguments(t *testing.T) {
	id := ownerSessionIdentity()
	if key := ExecutionOwnerLockKey(id); key != -2820332489113149044 {
		t.Fatalf("golden key: %d", key)
	}
	changed := id
	changed.SessionID, changed.Host, changed.ProcessID = "other", "host-b", 202
	if ExecutionOwnerLockKey(changed) != ExecutionOwnerLockKey(id) {
		t.Fatal("process identity changed scope")
	}
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	conn := new(sql.Conn)
	cases := []OwnerIdentity{id}
	for field := 0; field < 5; field++ {
		for _, invalid := range []string{" \t", "a\x00b"} {
			bad := id
			fields := []*string{&bad.SessionID, &bad.Environment, &bad.AccountID, &bad.StrategyID, &bad.Host}
			*fields[field] = invalid
			cases = append(cases, bad)
		}
	}
	bad := id
	bad.ProcessID = 0
	cases = append(cases, bad)
	for index, invalid := range cases {
		ctx := context.Background()
		if index == 0 {
			ctx = nil
		}
		result, err := RegisterExecutionOwnerSession(ctx, conn, invalid, now)
		if !errors.Is(err, ErrOwnerArgument) || !errors.Is(err, ErrOwner) || result != (OwnerSessionRegistration{}) {
			t.Fatalf("argument %d: %+v %v", index, result, err)
		}
	}
	for _, input := range []struct {
		conn *sql.Conn
		now  time.Time
	}{{nil, now}, {conn, time.Time{}}} {
		if _, err := RegisterExecutionOwnerSession(context.Background(), input.conn, id, input.now); !errors.Is(err, ErrOwnerArgument) {
			t.Fatalf("nil conn/zero time: %v", err)
		}
	}
}

// TestOwnerSessionLockProof проверяет реальные mode, scope, backend и transaction ownership.
func TestOwnerSessionLockProof(t *testing.T) {
	db := controlDatabase(t)
	id, now := ownerSessionIdentity(), time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	for _, mode := range []string{"none", "wrong", "shared", "other-backend", "xact", "caller-tx", "ended-xact"} {
		t.Run(mode, func(t *testing.T) {
			conn := ownerSessionConn(t, db)
			key := ExecutionOwnerLockKey(id)
			switch mode {
			case "wrong":
				ownerSessionSQL(t, conn, "SELECT pg_advisory_lock($1)", key+1)
			case "shared":
				ownerSessionSQL(t, conn, "SELECT pg_advisory_lock_shared($1)", key)
			case "other-backend":
				other := ownerSessionConn(t, db)
				ownerSessionSQL(t, other, "SELECT pg_advisory_lock($1)", key)
			case "xact", "caller-tx", "ended-xact":
				ownerSessionSQL(t, conn, "BEGIN")
				ownerSessionSQL(t, conn, "SELECT pg_advisory_xact_lock($1)", key)
				if mode == "caller-tx" {
					ownerSessionSQL(t, conn, "SELECT pg_advisory_lock($1)", key)
				}
				if mode == "ended-xact" {
					ownerSessionSQL(t, conn, "COMMIT")
				}
			}
			result, err := RegisterExecutionOwnerSession(context.Background(), conn, id, now)
			if err == nil || !errors.Is(err, ErrOwner) || result != (OwnerSessionRegistration{}) {
				t.Fatalf("accepted %s: %+v %v", mode, result, err)
			}
			if mode != "xact" && mode != "caller-tx" && !errors.Is(err, ErrOwnerSessionLockRequired) {
				t.Fatalf("wrong sentinel: %v", err)
			}
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions", "0")
		})
	}
}

// TestOwnerSessionHistory проверяет takeover, монотонность и сохранность исходных полей.
func TestOwnerSessionHistory(t *testing.T) {
	db := controlDatabase(t)
	conn := ownerSessionConn(t, db)
	id := ownerSessionIdentity()
	ownerSessionSQL(t, conn, "SELECT pg_advisory_lock($1)", ExecutionOwnerLockKey(id))
	now := time.Date(2026, 1, 5, 0, 0, 0, 987654321, time.FixedZone("offset", 3600))
	start := now.UTC().Truncate(time.Microsecond)
	for index, reason := range []string{"first", "CLEAN_SHUTDOWN", "active", "LOCK_LOST", "CLEAN_SHUTDOWN", "active"} {
		if index > 0 && reason != "active" {
			ownerSessionSQL(t, conn, `UPDATE momentum.execution_owner_sessions SET ended_at=last_seen_at,end_reason=$1 WHERE ended_at IS NULL`, reason)
		}
		id.SessionID = fmt.Sprintf("session-%d", index)
		clock := now.Add(-time.Duration(index) * time.Hour)
		if index == 1 {
			clock = now
		}
		result, err := RegisterExecutionOwnerSession(context.Background(), conn, id, clock)
		want := OwnerSessionRegistration{start.Add(time.Duration(index) * time.Microsecond), reason == "active" || reason == "LOCK_LOST"}
		if err != nil || result != want {
			t.Fatalf("%s: got %+v, want %+v: %v", reason, result, want, err)
		}
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions", "6")
	migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions WHERE ended_at IS NULL", "1")
	migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions WHERE end_reason='STALE_TAKEOVER'", "2")
	migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions WHERE host='host-a' AND process_id=101 AND account_id='acc' AND strategy_id='momentum-v1'", "6")
	var original time.Time
	if err := conn.QueryRowContext(context.Background(), "SELECT started_at FROM momentum.execution_owner_sessions WHERE session_id='session-0'").Scan(&original); err != nil || !original.Equal(start) {
		t.Fatalf("immutable start: %s %v", original, err)
	}
	// Последнее падение не скрывается более старым CLEAN_SHUTDOWN.
	id.SessionID = "reopen"
	ownerSessionSQL(t, conn, "UPDATE momentum.execution_owner_sessions SET last_seen_at=$1 WHERE ended_at IS NULL", start.Add(time.Hour))
	ownerSessionSQL(t, conn, "SELECT pg_advisory_unlock_all()")
	_ = conn.Close()
	conn = ownerSessionConn(t, db)
	ownerSessionSQL(t, conn, "SELECT pg_advisory_lock($1)", ExecutionOwnerLockKey(id))
	result, err := RegisterExecutionOwnerSession(context.Background(), conn, id, now)
	if err != nil || !result.RequiresReconciliation || !result.StartedAt.Equal(start.Add(6*time.Microsecond)) {
		t.Fatalf("reopen: %+v %v", result, err)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions WHERE session_id='session-5' AND ended_at=last_seen_at AND end_reason='STALE_TAKEOVER'", "1")
}

// TestOwnerSessionAtomicFailure проверяет rollback, отмену и ошибку commit без ложного success.
func TestOwnerSessionAtomicFailure(t *testing.T) {
	db := controlDatabase(t)
	conn := ownerSessionConn(t, db)
	ctx, id := context.Background(), ownerSessionIdentity()
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	ownerSessionSQL(t, conn, "SELECT pg_advisory_lock($1)", ExecutionOwnerLockKey(id))
	if _, err := RegisterExecutionOwnerSession(ctx, conn, id, now); err != nil {
		t.Fatal(err)
	}
	if result, err := RegisterExecutionOwnerSession(ctx, conn, id, now); err == nil || result != (OwnerSessionRegistration{}) {
		t.Fatalf("duplicate accepted: %+v %v", result, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	id.SessionID = "cancel"
	if _, err := RegisterExecutionOwnerSession(canceled, conn, id, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions WHERE ended_at IS NULL", "1")
	_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock_all()")
	_ = conn.Close()
	conn = ownerSessionConn(t, db)
	ownerSessionSQL(t, conn, "SELECT pg_advisory_lock($1)", ExecutionOwnerLockKey(id))
	ownerSessionSQL(t, conn, `CREATE FUNCTION momentum.reject_owner_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced commit failure'; END $$`)
	ownerSessionSQL(t, conn, `CREATE CONSTRAINT TRIGGER reject_owner_commit AFTER INSERT ON momentum.execution_owner_sessions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION momentum.reject_owner_commit()`)
	id.SessionID = "commit-fail"
	if result, err := RegisterExecutionOwnerSession(ctx, conn, id, now); err == nil || result != (OwnerSessionRegistration{}) {
		t.Fatalf("commit failure accepted: %+v %v", result, err)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions", "1")
	migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions WHERE ended_at IS NULL", "1")
	ownerSessionSQL(t, conn, "DROP TRIGGER reject_owner_commit ON momentum.execution_owner_sessions")
	if _, err := RegisterExecutionOwnerSession(ctx, conn, id, now); err != nil {
		t.Fatal(err)
	}
	ownerSessionSQL(t, conn, "SELECT pg_advisory_unlock_all()")
	_ = conn.Close()
	if result, err := RegisterExecutionOwnerSession(ctx, conn, id, now); err == nil || result != (OwnerSessionRegistration{}) {
		t.Fatalf("closed connection accepted: %+v %v", result, err)
	}
}

// TestOwnerSessionInterruptedCommit проверяет неизвестный исход при потере backend во время commit.
func TestOwnerSessionInterruptedCommit(t *testing.T) {
	db := controlDatabase(t)
	conn := ownerSessionConn(t, db)
	id, ctx := ownerSessionIdentity(), context.Background()
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	ownerSessionSQL(t, conn, "SELECT pg_advisory_lock($1)", ExecutionOwnerLockKey(id))
	if _, err := RegisterExecutionOwnerSession(ctx, conn, id, now); err != nil {
		t.Fatal(err)
	}
	ownerSessionSQL(t, conn, `CREATE FUNCTION momentum.interrupt_owner_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_terminate_backend(pg_backend_pid()); RETURN NEW; END $$`)
	ownerSessionSQL(t, conn, `CREATE CONSTRAINT TRIGGER interrupt_owner_commit AFTER INSERT ON momentum.execution_owner_sessions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION momentum.interrupt_owner_commit()`)
	id.SessionID = "unknown"
	if result, err := RegisterExecutionOwnerSession(ctx, conn, id, now); err == nil || !errors.Is(err, ErrOwner) || result != (OwnerSessionRegistration{}) {
		t.Fatalf("lost backend returned success: %+v %v", result, err)
	}
	// Историю проверяет независимое соединение, отсутствие строки не доказывает общий исход I/O.
	migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions", "1")
	migrationValue(t, db, "SELECT count(*) FROM momentum.execution_owner_sessions WHERE ended_at IS NULL", "1")
}
