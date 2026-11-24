package storage

import (
	"context"
	"database/sql"
	"errors"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	// ErrPaperWriteArgument обозначает непригодные аргументы.
	ErrPaperWriteArgument = errors.New("invalid paper write argument")
	// ErrPaperWriteDenied обозначает запрет записи.
	ErrPaperWriteDenied = errors.New("paper write denied")
	// ErrPaperWriteConflict обозначает нарушение сохранённых связей.
	ErrPaperWriteConflict = errors.New("paper write conflict")
	// ErrPaperWriteRejected обозначает отказ финансовой проверки.
	ErrPaperWriteRejected = errors.New("paper write rejected")
	// ErrPaperWriteUnknown означает неподтверждённый исход.
	ErrPaperWriteUnknown = errors.New("paper write outcome unknown")
)

// PaperFillRequest задаёт виртуальное исполнение и его владельца.
type PaperFillRequest struct {
	Key         PaperWriteKey
	Owner       *ExecutionOwner
	OrderLinkID string
	Fill        model.ConfirmedFill
}

// PaperFillResult описывает подтверждённые эффекты для одного намерения.
type PaperFillResult struct {
	Confirmed, Inserted, InsertedKnown bool
	FilledQuantity                     model.Decimal
	State                              string
}

// RecordPaperFill фиксирует виртуальное исполнение после проверок владельца, режима и модели.
func RecordPaperFill(ctx context.Context, db *sql.DB, req PaperFillRequest, check PaperFillCheck) (PaperFillResult, error) {
	key, scope, fill, owner := req.Key, req.Key.Scope, req.Fill, req.Owner
	expected := PaperFillExpectation{Key: key, OrderLinkID: req.OrderLinkID, Fill: fill}
	if db == nil || owner == nil || owner.conn == nil || check == nil || !expected.valid(ctx) {
		return PaperFillResult{}, ErrPaperWriteArgument
	}
	id := owner.id
	if owner.db != db || scope != (ControlScope{id.Environment, id.AccountID, id.StrategyID}) {
		return PaperFillResult{}, ErrPaperWriteDenied
	}
	permit := owner.Permit(ctx)
	knownDuplicate := false
	if permit == nil {
		var veto error
		result, err := recordFillCore(ctx, db, scope.Environment, scope.AccountID, req.OrderLinkID, fill, func(tx *sql.Tx, state string, inserted bool) error {
			if !inserted {
				return nil
			}
			facts, guardErr := lockPaperRunWrite(ctx, tx, key, owner)
			switch {
			case guardErr != nil:
				veto = guardErr
			case state != "PENDING" && state != "PARTIALLY_FILLED" || !paperFillAllowed(facts, fill.Side):
				veto = ErrPaperWriteDenied
			default:
				_, found, err := expected.confirmation(facts, check)
				veto = err
				if err == nil && !found {
					veto = ErrPaperWriteConflict
				}
			}
			return veto
		})
		switch {
		case err == nil && result.Inserted:
			return PaperFillResult{Confirmed: true, Inserted: true, InsertedKnown: true, FilledQuantity: result.FilledQuantity, State: result.State}, nil
		case err == nil:
			knownDuplicate = true
		case veto != nil:
			return PaperFillResult{}, paperWriteFailure(ctx, veto)
		case errors.Is(err, ErrExecutionAbsent), errors.Is(err, ErrExecutionConflict):
			return PaperFillResult{}, ErrPaperWriteConflict
		case errors.Is(err, ErrExecutionArgument):
			return PaperFillResult{}, ErrPaperWriteArgument
		}
	}
	if ctx.Err() != nil {
		return PaperFillResult{}, errors.Join(ErrPaperWriteUnknown, ctx.Err())
	}
	got, found, err := ReadPaperFillConfirmation(ctx, db, expected, check)
	switch {
	case err != nil:
		return PaperFillResult{}, paperWriteFailure(ctx, err)
	case found:
		return PaperFillResult{Confirmed: true, InsertedKnown: knownDuplicate, FilledQuantity: got.FilledQuantity, State: got.State}, nil
	case permit != nil:
		return PaperFillResult{}, errors.Join(ErrPaperWriteDenied, permit)
	default:
		return PaperFillResult{}, ErrPaperWriteUnknown
	}
}

// paperWriteRejection скрывает текст veto и сохраняет его проверяемые категории.
type paperWriteRejection struct{ cause error }

// Error возвращает только публичную категорию отказа.
func (paperWriteRejection) Error() string { return ErrPaperWriteRejected.Error() }

// Is сопоставляет отказ с безопасной категорией и исходной причиной.
func (r paperWriteRejection) Is(target error) bool {
	return target == ErrPaperWriteRejected || errors.Is(r.cause, target)
}

// paperWriteFailure преобразует ошибки чтения в категории публичного API.
func paperWriteFailure(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, ErrPaperFillConfirmationRejected):
		return paperWriteRejection{err}
	case errors.Is(err, ErrPaperFillConfirmationConflict), errors.Is(err, ErrPaperFactsCorrupt), errors.Is(err, ErrPaperFactsAbsent):
		return ErrPaperWriteConflict
	case errors.Is(err, ErrPaperWriteDenied), errors.Is(err, ErrPaperWriteConflict), errors.Is(err, ErrPaperWriteRejected):
		return err
	default:
		return errors.Join(ErrPaperWriteUnknown, ctx.Err())
	}
}

// lockPaperRunWrite удерживает ограждения управления, регистрации и цикла до конца записи.
func lockPaperRunWrite(ctx context.Context, tx *sql.Tx, key PaperWriteKey, owner *ExecutionOwner) (PaperRunFacts, error) {
	var session, run string
	s := key.Scope
	if err := lockPaperControls(ctx, tx, false); err != nil {
		return PaperRunFacts{}, err
	}
	err := tx.QueryRowContext(ctx, `SELECT session_id FROM momentum.execution_owner_sessions WHERE
 environment=$1 AND account_id=$2 AND strategy_id=$3 AND ended_at IS NULL FOR SHARE`, s.Environment, s.AccountID, s.StrategyID).Scan(&session)
	if errors.Is(err, sql.ErrNoRows) || err == nil && session != owner.id.SessionID {
		return PaperRunFacts{}, ErrPaperWriteDenied
	}
	if err != nil {
		return PaperRunFacts{}, err
	}
	err = tx.QueryRowContext(ctx, `SELECT run_id FROM momentum.rebalance_runs WHERE run_id=$1 AND account_id=$2 AND strategy_id=$3 FOR UPDATE`, key.RunID, s.AccountID, s.StrategyID).Scan(&run)
	if errors.Is(err, sql.ErrNoRows) {
		return PaperRunFacts{}, ErrPaperWriteConflict
	}
	if err != nil {
		return PaperRunFacts{}, err
	}
	f, err := readPaperRunFacts(ctx, tx, s, run)
	if err == nil && f.Completed {
		return PaperRunFacts{}, ErrPaperWriteDenied
	}
	if err == nil && (!f.HeadFound || f.Head != f.Input.Header() || f.Head.InputHash != key.InputHash) {
		return PaperRunFacts{}, ErrPaperWriteConflict
	}
	return f, err
}

// paperFillAllowed проверяет текущий режим и точную команду закрытия.
func paperFillAllowed(f PaperRunFacts, side model.FillSide) bool {
	p, c := f.Input.Values().Purpose, f.Control
	if p.Kind == "close-all" {
		return side == model.FillSell && c.State.Mode == "HALTED" && c.Actor == "OPERATOR" && c.Action == "HALT" &&
			c.Kind == "close-all" && c.CommandID == p.CommandID && c.State.Revision == p.ControlRevision
	}
	return p.Kind == "rebalance" && (c.State.Mode == "ACTIVE" || c.State.Mode == "PAUSED_ENTRIES" && side == model.FillSell)
}
