package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
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
	// ErrOwnerBusy означает, что область уже принадлежит живому владельцу.
	ErrOwnerBusy = errors.New("momentum execution scope is owned by another session")
	// ErrOwnerLost означает потерю lock или завершение владения; отправка запрещена.
	ErrOwnerLost = errors.New("momentum execution ownership lost")
	// ErrOwnerReconciliationRequired означает takeover без подтверждённой сверки.
	ErrOwnerReconciliationRequired = errors.New("momentum execution takeover requires reconciliation")
)

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
	owner := &ExecutionOwner{db: db, conn: conn, id: id, key: ExecutionOwnerLockKey(id), done: make(chan struct{})}
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_catalog.pg_try_advisory_lock($1)`, owner.key).Scan(&locked); err != nil {
		discardConn(conn) // исход захвата неизвестен
		return nil, errors.Join(ErrOwner, ctx.Err())
	} else if !locked {
		conn.Close()
		return nil, errors.Join(ErrOwner, ErrOwnerBusy)
	}
	registration, err := RegisterExecutionOwnerSession(ctx, conn, id, now)
	if err != nil {
		discardConn(conn)
		return nil, err
	}
	owner.reconcile = registration.RequiresReconciliation
	owner.lastSeen = now.UTC()
	return owner, nil
}

// Done закрывается при потере или завершении владения — сигнал отмены работы.
func (o *ExecutionOwner) Done() <-chan struct{} { return o.done }

// NeedsReconciliation сообщает, ждёт ли takeover подтверждённой сверки.
func (o *ExecutionOwner) NeedsReconciliation() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.reconcile
}

// MarkReconciled снимает локальный latch по непустому ID; реальный proof сверки обязан проверить caller.
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
	res, err := o.conn.ExecContext(ctx, `UPDATE momentum.execution_owner_sessions SET last_seen_at=greatest(last_seen_at,$2)
 WHERE session_id=$1 AND ended_at IS NULL`, o.id.SessionID, now.UTC())
	if n, _ := rowsAffected(res, err); n != 1 {
		if ctx.Err() != nil {
			return errors.Join(ErrOwner, ctx.Err())
		}
		return o.markLost(true)
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

// Release завершает сессию и освобождает lock; после потери lock — no-op.
// CLEAN_SHUTDOWN пишется только без невыполненной сверки: иначе сессия
// остаётся активной, и следующий владелец получит STALE_TAKEOVER и требование
// сверки. Lock снимается закрытием backend-сессии, а не возвратом в пул.
func (o *ExecutionOwner) Release(ctx context.Context, now time.Time) error {
	if ctx == nil {
		return errors.Join(ErrOwner, ErrOwnerArgument)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lost {
		return nil
	}
	if o.reconcile {
		o.markLost(false)
		return nil
	}
	_, err := o.conn.ExecContext(ctx, `UPDATE momentum.execution_owner_sessions
 SET last_seen_at=greatest(last_seen_at,$2), ended_at=greatest(last_seen_at,$2), end_reason='CLEAN_SHUTDOWN'
 WHERE session_id=$1 AND ended_at IS NULL`, o.id.SessionID, now.UTC())
	o.markLost(true)
	if err != nil {
		return errors.Join(ErrOwner, ErrOwnerLost, ctx.Err())
	}
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
		return o.markLost(true)
	}
	return nil
}

// markLost навсегда запрещает отправку, закрывает Done и уничтожает соединение
// (lock не возвращается в пул). record=true фиксирует LOCK_LOST для ещё
// активной сессии через пул.
func (o *ExecutionOwner) markLost(record bool) error {
	if !o.lost {
		o.lost = true
		close(o.done)
		discardConn(o.conn)
		if record {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = o.db.ExecContext(ctx, `UPDATE momentum.execution_owner_sessions
 SET ended_at=last_seen_at, end_reason='LOCK_LOST' WHERE session_id=$1 AND ended_at IS NULL`, o.id.SessionID)
		}
	}
	return errors.Join(ErrOwner, ErrOwnerLost)
}

// discardConn закрывает соединение вместе с backend-сессией PostgreSQL, чтобы
// session-level lock гарантированно снялся: driver.ErrBadConn убирает его из пула.
func discardConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

// rowsAffected объединяет ошибку выполнения и число изменённых строк.
func rowsAffected(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
