package storage

import (
	"context"
	"database/sql"
	"errors"
)

// InitializeEmptyPaperControl записывает начальную ревизию пустой области на соединении владельца.
func InitializeEmptyPaperControl(ctx context.Context, db *sql.DB, owner *ExecutionOwner, in PaperCommandRequest) (PaperCommandState, error) {
	return initializeEmptyPaperControl(ctx, db, owner, in, (*sql.Tx).Commit)
}

// initializeEmptyPaperControl применяет INITIALIZE через заданное завершение транзакции владельца.
func initializeEmptyPaperControl(ctx context.Context, db *sql.DB, owner *ExecutionOwner, in PaperCommandRequest, finish func(*sql.Tx) error) (PaperCommandState, error) {
	if ctx == nil || db == nil || owner == nil || !paperControlScope(in.Scope) || in.Action != "INITIALIZE" || in.ExpectedRevision != 0 {
		return PaperCommandState{}, ErrPaperFenceArgument
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.db != db || owner.conn == nil || in.Scope != (ControlScope{owner.id.Environment, owner.id.AccountID, owner.id.StrategyID}) {
		return PaperCommandState{}, ErrPaperFenceDenied
	}
	if err := owner.checkLocked(ctx); err != nil {
		return PaperCommandState{}, errors.Join(ErrPaperFenceDenied, err, ctx.Err())
	}
	var checked bool
	var guardErr error
	state, err := applyPaperCommandOn(ctx, owner.conn, in, func(tx *sql.Tx, _ PaperCommandState) error {
		guardErr = checkEmptyPaperScope(ctx, tx, in.Scope, owner.id.SessionID)
		checked = guardErr == nil
		return guardErr
	}, finish)
	if err != nil {
		return PaperCommandState{}, errors.Join(err, guardErr)
	}
	if !checked {
		// Штатный replay возвращает старую запись, минуя проверку пустоты.
		return PaperCommandState{}, ErrPaperFenceConflict
	}
	return state, nil
}

// checkEmptyPaperScope подтверждает владельца и отсутствие данных до фиксации предварительной ревизии.
func checkEmptyPaperScope(ctx context.Context, tx *sql.Tx, scope ControlScope, session string) error {
	var active string
	err := tx.QueryRowContext(ctx, `SELECT session_id FROM momentum.execution_owner_sessions
 WHERE environment=$1 AND account_id=$2 AND strategy_id=$3 AND ended_at IS NULL FOR SHARE`,
		scope.Environment, scope.AccountID, scope.StrategyID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) || err == nil && active != session {
		return ErrPaperFenceDenied
	}
	if err != nil {
		return paperFenceError(ctx, err)
	}
	_, err = tx.ExecContext(ctx, `LOCK TABLE momentum.rebalance_runs, momentum.paper_run_inputs, momentum.order_intents,
 momentum.fills, momentum.ledger_entries, momentum.equity_snapshots, momentum.risk_events IN SHARE MODE`)
	if err != nil {
		return paperFenceError(ctx, err)
	}
	var nonempty bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT FROM momentum.rebalance_runs WHERE account_id=$2 AND strategy_id=$3)
 OR EXISTS (SELECT FROM momentum.equity_snapshots WHERE environment=$1 AND account_id=$2 AND strategy_id=$3)
 OR EXISTS (SELECT FROM momentum.risk_events WHERE environment=$1 AND account_id=$2 AND strategy_id=$3)`,
		scope.Environment, scope.AccountID, scope.StrategyID).Scan(&nonempty)
	if err != nil {
		return paperFenceError(ctx, err)
	}
	if nonempty {
		return ErrPaperFenceConflict
	}
	facts, err := readPaperRunFacts(ctx, tx, scope, "")
	if err != nil {
		return paperFenceError(ctx, err)
	}
	if facts.HeadFound || facts.ScopeHasIntents || facts.OwnerSessionID != session || facts.Control.State.Revision != 1 {
		return ErrPaperFenceConflict
	}
	return nil
}
