package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"time"
)

// Единственный владелец исполнения на область environment + account + strategy.
//
// Владение — session-level advisory lock на выделенном соединении и активная
// строка execution_owner_sessions. Пока lock держит живое соединение, второй
// процесс получает ErrOwnerBusy. Если соединение пропало, lock снимает сам
// PostgreSQL; владелец узнаёт об этом при Heartbeat/Permit и навсегда теряет
// право отправки.
//
// Ограничения in-flight запросов: Permit проверяется перед каждой отправкой, но
// запрос, уже ушедший на биржу до потери lock, отозвать нельзя, и его исход
// неизвестен. Поэтому следующий владелец после любого не-чистого завершения
// (STALE_TAKEOVER, LOCK_LOST) получает permit только после подтверждённой
// сверки с биржей (MarkReconciled).

var (
	// ErrOwner — общая категория ошибок владения.
	ErrOwner = errors.New("momentum execution owner failed")
	// ErrOwnerArgument означает невалидную identity или время.
	ErrOwnerArgument = errors.New("invalid momentum execution owner argument")
	// ErrOwnerBusy означает, что область уже принадлежит живому владельцу.
	ErrOwnerBusy = errors.New("momentum execution scope is owned by another session")
	// ErrOwnerLost означает потерю lock или завершение владения; отправка запрещена.
	ErrOwnerLost = errors.New("momentum execution ownership lost")
	// ErrOwnerReconciliationRequired означает takeover без подтверждённой сверки.
	ErrOwnerReconciliationRequired = errors.New("momentum execution takeover requires reconciliation")
)

// OwnerIdentity описывает процесс-кандидат во владельцы области.
type OwnerIdentity struct {
	SessionID, Environment, AccountID, StrategyID, Host string
	ProcessID                                           int64
}

// ExecutionOwner — удерживаемое владение областью исполнения.
type ExecutionOwner struct {
	mu        sync.Mutex
	db        *sql.DB
	conn      *sql.Conn
	id        OwnerIdentity
	key       int64
	reconcile bool
	lost      bool
	lastSeen  time.Time
	done      chan struct{}
}

// AcquireExecutionOwner захватывает область или возвращает ErrOwnerBusy. Если
// предыдущая сессия завершилась не чисто, владелец создаётся с требованием сверки.
func AcquireExecutionOwner(ctx context.Context, db *sql.DB, id OwnerIdentity, now time.Time) (*ExecutionOwner, error) {
	if ctx == nil || db == nil || now.IsZero() || id.ProcessID <= 0 ||
		!nonBlankOwner(id.SessionID, id.Environment, id.AccountID, id.StrategyID, id.Host) {
		return nil, errors.Join(ErrOwner, ErrOwnerArgument)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, errors.Join(ErrOwner, ctx.Err())
	}
	owner := &ExecutionOwner{db: db, conn: conn, id: id, key: ownerLockKey(id), done: make(chan struct{})}
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_catalog.pg_try_advisory_lock($1)`, owner.key).Scan(&locked); err != nil || !locked {
		conn.Close()
		if err != nil {
			return nil, errors.Join(ErrOwner, ctx.Err())
		}
		return nil, errors.Join(ErrOwner, ErrOwnerBusy)
	}
	if err := owner.open(ctx, now.UTC()); err != nil {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_catalog.pg_advisory_unlock($1)`, owner.key)
		conn.Close()
		return nil, err
	}
	return owner, nil
}

// open под удерживаемым lock закрывает зависшую сессию и создаёт новую.
func (o *ExecutionOwner) open(ctx context.Context, now time.Time) error {
	tx, err := o.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return errors.Join(ErrOwner, ctx.Err())
	}
	defer tx.Rollback()
	scope := []any{o.id.Environment, o.id.AccountID, o.id.StrategyID}
	var previous sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT coalesce(end_reason,'ACTIVE') FROM momentum.execution_owner_sessions
 WHERE environment=$1 AND account_id=$2 AND strategy_id=$3 ORDER BY started_at DESC, session_id DESC LIMIT 1`, scope...).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.Join(ErrOwner, ctx.Err())
	}
	o.reconcile = previous.Valid && previous.String != "CLEAN_SHUTDOWN"
	if _, err = tx.ExecContext(ctx, `UPDATE momentum.execution_owner_sessions
 SET last_seen_at=greatest(last_seen_at,$4), ended_at=greatest(last_seen_at,$4), end_reason='STALE_TAKEOVER'
 WHERE environment=$1 AND account_id=$2 AND strategy_id=$3 AND ended_at IS NULL`, append(scope, now)...); err != nil {
		return errors.Join(ErrOwner, ctx.Err())
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO momentum.execution_owner_sessions
 (session_id,environment,account_id,strategy_id,host,process_id,started_at,last_seen_at) VALUES ($4,$1,$2,$3,$5,$6,$7,$7)`,
		append(scope, o.id.SessionID, o.id.Host, o.id.ProcessID, now)...); err != nil {
		return errors.Join(ErrOwner, ErrOwnerArgument, ctx.Err())
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(ErrOwner, ctx.Err())
	}
	o.lastSeen = now
	return nil
}

// Done закрывается при потере или завершении владения — сигнал отмены работы.
func (o *ExecutionOwner) Done() <-chan struct{} { return o.done }

// NeedsReconciliation сообщает, ждёт ли takeover подтверждённой сверки.
func (o *ExecutionOwner) NeedsReconciliation() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.reconcile
}

// MarkReconciled снимает требование сверки по идентификатору подтверждённой сверки.
func (o *ExecutionOwner) MarkReconciled(reconciliationID string) error {
	if !nonBlankOwner(reconciliationID) {
		return errors.Join(ErrOwner, ErrOwnerArgument)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lost {
		return errors.Join(ErrOwner, ErrOwnerLost)
	}
	o.reconcile = false
	return nil
}

// Heartbeat проверяет, что lock ещё удерживается, и продлевает last_seen_at.
func (o *ExecutionOwner) Heartbeat(ctx context.Context, now time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if now.Before(o.lastSeen) {
		return errors.Join(ErrOwner, ErrOwnerArgument)
	}
	if err := o.checkLocked(ctx); err != nil {
		return err
	}
	res, err := o.conn.ExecContext(ctx, `UPDATE momentum.execution_owner_sessions SET last_seen_at=$2
 WHERE session_id=$1 AND ended_at IS NULL`, o.id.SessionID, now.UTC())
	if n, _ := rowsAffected(res, err); n != 1 {
		if ctx.Err() != nil {
			return errors.Join(ErrOwner, ctx.Err())
		}
		return o.markLost()
	}
	o.lastSeen = now.UTC()
	return nil
}

// Permit разрешает одну отправку: lock удерживается и сверка после takeover подтверждена.
func (o *ExecutionOwner) Permit(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(ctx); err != nil {
		return err
	}
	if o.reconcile {
		return errors.Join(ErrOwner, ErrOwnerReconciliationRequired)
	}
	return nil
}

// Release чисто завершает сессию и освобождает lock; после потери lock — no-op.
func (o *ExecutionOwner) Release(ctx context.Context, now time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lost {
		return nil
	}
	defer o.markLost()
	if _, err := o.conn.ExecContext(ctx, `UPDATE momentum.execution_owner_sessions
 SET last_seen_at=greatest(last_seen_at,$2), ended_at=greatest(last_seen_at,$2), end_reason='CLEAN_SHUTDOWN'
 WHERE session_id=$1 AND ended_at IS NULL`, o.id.SessionID, now.UTC()); err != nil {
		return errors.Join(ErrOwner, ErrOwnerLost, ctx.Err())
	}
	_, _ = o.conn.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_unlock($1)`, o.key)
	return nil
}

// checkLocked подтверждает lock запросом к pg_locks на соединении владельца; вызывается под mu.
func (o *ExecutionOwner) checkLocked(ctx context.Context) error {
	if o.lost {
		return errors.Join(ErrOwner, ErrOwnerLost)
	}
	if ctx == nil || ctx.Err() != nil {
		return errors.Join(ErrOwner, ErrOwnerArgument)
	}
	var held bool
	err := o.conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_locks
 WHERE locktype='advisory' AND pid=pg_catalog.pg_backend_pid() AND granted
 AND classid=(($1::bigint >> 32) & 4294967295)::oid AND objid=($1::bigint & 4294967295)::oid AND objsubid=1)`, o.key).Scan(&held)
	if err != nil && ctx.Err() != nil {
		return errors.Join(ErrOwner, ctx.Err())
	}
	if err != nil || !held {
		return o.markLost()
	}
	return nil
}

// markLost навсегда запрещает отправку, закрывает Done и фиксирует LOCK_LOST через пул.
func (o *ExecutionOwner) markLost() error {
	if !o.lost {
		o.lost = true
		close(o.done)
		o.conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = o.db.ExecContext(ctx, `UPDATE momentum.execution_owner_sessions
 SET ended_at=last_seen_at, end_reason='LOCK_LOST' WHERE session_id=$1 AND ended_at IS NULL`, o.id.SessionID)
	}
	return errors.Join(ErrOwner, ErrOwnerLost)
}

// ownerLockKey выводит стабильный 64-битный ключ advisory lock из области.
func ownerLockKey(id OwnerIdentity) int64 {
	sum := sha256.Sum256([]byte("momentum-owner\x00" + id.Environment + "\x00" + id.AccountID + "\x00" + id.StrategyID))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// rowsAffected объединяет ошибку выполнения и число изменённых строк.
func rowsAffected(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
