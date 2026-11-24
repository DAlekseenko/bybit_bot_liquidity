package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lib/pq"

	"bybit_bot_liquidity/internal/momentum/paperwire"
)

var (
	// ErrPaperOwnerAlreadyReconciled означает, что локальная сверка уже завершена.
	ErrPaperOwnerAlreadyReconciled = errors.New("paper owner requires no reconciliation")
	// ErrPaperFenceArgument означает неверные аргументы бумажной сверки.
	ErrPaperFenceArgument = errors.New("invalid paper recovery request")
	// ErrPaperFenceDenied означает отсутствие подходящего владельца.
	ErrPaperFenceDenied = errors.New("paper recovery denied")
	// ErrPaperFenceStale означает, что привязки proof больше не актуальны.
	ErrPaperFenceStale = errors.New("stale paper recovery proof")
	// ErrPaperFenceConflict означает противоречие в сохранённых данных.
	ErrPaperFenceConflict = errors.New("conflicting paper command")
	// ErrPaperFenceRejected означает финансовый отказ проверяющего callback.
	ErrPaperFenceRejected = errors.New("paper recovery proof rejected")
	// ErrPaperFenceRetry означает откат по SQLSTATE 40P01.
	ErrPaperFenceRetry = errors.New("paper recovery transaction rolled back")
	// ErrPaperFenceUnknown означает операционную ошибку с неподтверждённым исходом.
	ErrPaperFenceUnknown = errors.New("paper recovery outcome unknown")
)

// PaperRecoveryStamp связывает снимок paper, сессию и поколение управляющей команды.
type PaperRecoveryStamp struct {
	Scope                                            ControlScope
	ConfigHash, RecoveryID, OwnerSessionID, DataHash string
	ControlRevision                                  uint64
	Head                                             paperwire.CycleBinding
	HeadFound                                        bool
}

// verifyPaperFence проверяет актуальные полномочия и данные в уже ограждённой транзакции.
func verifyPaperFence(ctx context.Context, tx *sql.Tx, session string, p PaperRecoveryStamp, revision uint64, check func(PaperRunFacts) error) error {
	_, idErr := datasetArtifactKey(p.RecoveryID)
	_, hashErr := datasetArtifactKey(p.DataHash)
	if check == nil || idErr != nil || hashErr != nil || p.ControlRevision == 0 || !paperControlScope(p.Scope) {
		return ErrPaperFenceStale
	}
	if session == "" || p.OwnerSessionID != session {
		return ErrPaperFenceDenied
	}
	var active string
	err := tx.QueryRowContext(ctx, `SELECT session_id FROM momentum.execution_owner_sessions
 WHERE environment=$1 AND account_id=$2 AND strategy_id=$3 AND ended_at IS NULL FOR SHARE`, p.Scope.Environment, p.Scope.AccountID, p.Scope.StrategyID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) || err == nil && active != session {
		return ErrPaperFenceDenied
	}
	if err != nil {
		return paperFenceError(ctx, err)
	}
	got, found, err := readPaperInputHead(ctx, tx, paperwire.Scope(p.Scope))
	if err != nil {
		return paperFenceError(ctx, err)
	}
	if found != p.HeadFound || got.Input.Header() != p.Head {
		return ErrPaperFenceStale
	}
	if found {
		var run string
		err = tx.QueryRowContext(ctx, `SELECT run_id FROM momentum.rebalance_runs WHERE run_id=$1 AND account_id=$2 AND strategy_id=$3 FOR UPDATE`, p.Head.RunID, p.Scope.AccountID, p.Scope.StrategyID).Scan(&run)
		if err != nil {
			return paperFenceError(ctx, err)
		}
	}
	_, err = tx.ExecContext(ctx, `LOCK TABLE momentum.plan_revisions, momentum.order_intents, momentum.fills, momentum.ledger_entries IN SHARE MODE`)
	if err != nil {
		return paperFenceError(ctx, err)
	}
	f, err := readPaperRunFacts(ctx, tx, p.Scope, "")
	if err != nil {
		return paperFenceError(ctx, err)
	}
	if f.Control.State.Revision != revision || f.Control.ConfigHash != p.ConfigHash || f.Scope != p.Scope || f.OwnerSessionID != session || f.HeadFound != found || f.Head != p.Head || f.DataHash != p.DataHash {
		return ErrPaperFenceStale
	}
	if err := check(f); err != nil {
		return fenceRejection{err}
	}
	return nil
}

// fenceRejection скрывает произвольный текст callback.
type fenceRejection struct{ reason error }

// Error сообщает только установленную категорию.
func (fenceRejection) Error() string { return ErrPaperFenceRejected.Error() }

// Is сохраняет исходную ошибку для errors.Is.
func (e fenceRejection) Is(target error) bool {
	return target == ErrPaperFenceRejected || errors.Is(e.reason, target)
}

// paperFenceError отличает повреждённые факты и откат известной жертвы.
func paperFenceError(ctx context.Context, err error) error {
	var failure *pq.Error
	if errors.As(err, &failure) && failure.Code == "40P01" {
		return errors.Join(ErrPaperFenceRetry, &pq.Error{Code: failure.Code}, ctx.Err())
	}
	if errors.Is(err, errPaperCommandSemantic) || errors.Is(err, ErrPaperCommandOutcomeCorrupt) || errors.Is(err, ErrPaperFactsCorrupt) || errors.Is(err, ErrPaperInputCorrupt) || errors.Is(err, ErrPaperFactsAbsent) || errors.Is(err, sql.ErrNoRows) {
		return ErrPaperFenceConflict
	}
	return errors.Join(ErrPaperFenceUnknown, ctx.Err())
}

// ReconcilePaperOwner снимает latch takeover после подтверждённой проверки журнала на собственном backend.
func ReconcilePaperOwner(ctx context.Context, db *sql.DB, o *ExecutionOwner, p PaperRecoveryStamp, check func(PaperRunFacts) error) error {
	if ctx == nil || db == nil || o == nil || !paperControlScope(p.Scope) {
		return ErrPaperFenceArgument
	}
	if check == nil {
		return ErrPaperFenceStale
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.db != db || o.conn == nil || p.Scope != (ControlScope{o.id.Environment, o.id.AccountID, o.id.StrategyID}) {
		return ErrPaperFenceDenied
	}
	if err := o.checkLocked(ctx); err != nil {
		return errors.Join(ErrPaperFenceDenied, err, ctx.Err())
	}
	if !o.reconcile {
		return ErrPaperOwnerAlreadyReconciled
	}
	tx, err := o.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return paperFenceError(ctx, err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `LOCK TABLE momentum.control_revisions IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return paperFenceError(ctx, err)
	}
	err = verifyPaperFence(ctx, tx, o.id.SessionID, p, p.ControlRevision, check)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return paperFenceError(ctx, err)
	}
	if err = o.checkLocked(ctx); err != nil {
		return errors.Join(ErrPaperFenceDenied, err, ctx.Err())
	}
	o.reconcile = false
	return nil
}
