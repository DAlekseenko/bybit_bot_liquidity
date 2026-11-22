package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrOwner — общая категория ошибок владения.
	ErrOwner = errors.New("momentum execution owner failed")
	// ErrOwnerArgument означает невалидную identity или время.
	ErrOwnerArgument = errors.New("invalid momentum execution owner argument")
	// ErrOwnerSessionLockRequired означает отсутствие подтверждённого exclusive session lock.
	ErrOwnerSessionLockRequired = errors.New("momentum execution owner session lock required")
)

// OwnerIdentity описывает процесс-кандидат во владельцы области.
// Тип и алгоритм регистрации сохранены из M2 автора PR #406.
type OwnerIdentity struct {
	SessionID, Environment, AccountID, StrategyID, Host string
	ProcessID                                           int64
}

// OwnerSessionRegistration описывает историю, но не разрешает исполнение.
type OwnerSessionRegistration struct {
	StartedAt              time.Time
	RequiresReconciliation bool
}

// ExecutionOwnerLockKey выводит стабильный 64-битный ключ advisory lock из области.
func ExecutionOwnerLockKey(id OwnerIdentity) int64 {
	sum := sha256.Sum256([]byte("momentum-owner\x00" + id.Environment + "\x00" + id.AccountID + "\x00" + id.StrategyID))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// RegisterExecutionOwnerSession атомарно регистрирует историю под уже удерживаемым lock.
// Caller передаёт свободный dedicated conn с session-level exclusive lock и
// исключает параллельное использование conn. Lock и conn остаются у caller.
// StartedAt упорядочивает историю и не заменяет локальные часы владельца.
func RegisterExecutionOwnerSession(ctx context.Context, conn *sql.Conn, id OwnerIdentity, now time.Time) (OwnerSessionRegistration, error) {
	var result OwnerSessionRegistration
	if ctx == nil || conn == nil || now.IsZero() || id.ProcessID <= 0 ||
		!nonBlankOwner(id.SessionID, id.Environment, id.AccountID, id.StrategyID, id.Host) {
		return result, errors.Join(ErrOwner, ErrOwnerArgument)
	}
	// Собственный BEGIN отвергает активную caller-TX; objsubid не доказывает session lock.
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return result, errors.Join(ErrOwner, fmt.Errorf("begin owner registration: %w", err))
	}
	defer tx.Rollback()
	var held bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_locks
 WHERE locktype='advisory' AND pid=pg_catalog.pg_backend_pid() AND granted AND mode='ExclusiveLock'
 AND classid=(($1::bigint >> 32) & 4294967295)::oid AND objid=($1::bigint & 4294967295)::oid AND objsubid=1)`, ExecutionOwnerLockKey(id)).Scan(&held)
	if err != nil {
		return result, errors.Join(ErrOwner, fmt.Errorf("check owner session lock: %w", err))
	}
	if !held {
		return result, errors.Join(ErrOwner, ErrOwnerSessionLockRequired)
	}
	scope := []any{id.Environment, id.AccountID, id.StrategyID}
	var active int64
	var latest sql.NullTime
	var previous sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE ended_at IS NULL), max(started_at),
 (SELECT end_reason FROM momentum.execution_owner_sessions WHERE environment=$1 AND account_id=$2 AND strategy_id=$3
  ORDER BY started_at DESC, session_id DESC LIMIT 1)
 FROM momentum.execution_owner_sessions WHERE environment=$1 AND account_id=$2 AND strategy_id=$3`, scope...).Scan(&active, &latest, &previous)
	if err != nil {
		return result, errors.Join(ErrOwner, fmt.Errorf("read owner history: %w", err))
	}
	start := now.UTC().Truncate(time.Microsecond)
	if latest.Valid && !start.After(latest.Time) {
		start = latest.Time.UTC().Add(time.Microsecond)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE momentum.execution_owner_sessions
 SET last_seen_at=greatest(last_seen_at,$4), ended_at=greatest(last_seen_at,$4), end_reason='STALE_TAKEOVER'
 WHERE environment=$1 AND account_id=$2 AND strategy_id=$3 AND ended_at IS NULL`, append(scope, start)...); err != nil {
		return result, errors.Join(ErrOwner, fmt.Errorf("close stale owner: %w", err))
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO momentum.execution_owner_sessions
 (session_id,environment,account_id,strategy_id,host,process_id,started_at,last_seen_at) VALUES ($4,$1,$2,$3,$5,$6,$7,$7)`,
		append(scope, id.SessionID, id.Host, id.ProcessID, start)...); err != nil {
		return result, errors.Join(ErrOwner, ErrOwnerArgument, fmt.Errorf("insert owner session: %w", err))
	}
	if err = tx.Commit(); err != nil {
		return result, errors.Join(ErrOwner, fmt.Errorf("commit owner session: %w", err))
	}
	return OwnerSessionRegistration{StartedAt: start, RequiresReconciliation: active > 0 || (latest.Valid && previous.String != "CLEAN_SHUTDOWN")}, nil
}

// nonBlankOwner проверяет, что значения не пусты и не содержат NUL.
func nonBlankOwner(values ...string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) == "" || strings.ContainsRune(v, 0) {
			return false
		}
	}
	return true
}
