package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// PaperFinalizeRequest задаёт область, исходный вход и решение об окончании цикла.
type PaperFinalizeRequest struct {
	Key             PaperWriteKey
	Owner           *ExecutionOwner
	Kind, CommandID string
	ControlRevision uint64
}

// PaperResolutionBuilder строит финансовое доказательство по фактам выбранного run.
type PaperResolutionBuilder func(PaperRunFacts) (paperwire.ResolutionRecord, error)

// PaperFinalizeResult содержит подтверждённую неизменяемую строку журнала.
type PaperFinalizeResult struct {
	Completion RunCompletion
	Confirmed  bool
}

// FinalizePaperRun фиксирует ровно один проверенный итог либо подтверждает существующий.
func FinalizePaperRun(ctx context.Context, db *sql.DB, req PaperFinalizeRequest, build PaperResolutionBuilder) (PaperFinalizeResult, error) {
	key, owner := req.Key, req.Owner
	_, hashErr := datasetArtifactKey(key.InputHash)
	validKind := req.Kind == "COMPLETED" && req.CommandID == "" && req.ControlRevision == 0 ||
		req.Kind == "ABORTED" && paperControlText(req.CommandID) && req.ControlRevision != 0
	if db == nil || owner == nil || owner.conn == nil || build == nil || !validKind || hashErr != nil ||
		!validLifecycleRequest(ctx, key.Scope, key.RunID) || len(key.RunID) > paperwire.MaxCanonicalBytes ||
		len(key.Scope.AccountID) > paperwire.MaxCanonicalBytes || len(key.Scope.StrategyID) > paperwire.MaxCanonicalBytes || len(req.CommandID) > paperwire.MaxCanonicalBytes {
		return PaperFinalizeResult{}, ErrPaperWriteArgument
	}
	id := owner.id
	if owner.db != db || key.Scope != (ControlScope{id.Environment, id.AccountID, id.StrategyID}) {
		return PaperFinalizeResult{}, ErrPaperWriteDenied
	}
	var expected RunCompletion
	permit := owner.Permit(ctx)
	var cause error
	if permit != nil {
		cause = errors.Join(ErrPaperWriteDenied, permit)
	} else {
		expected, cause = req.insert(ctx, db, build)
		if cause == nil {
			return PaperFinalizeResult{expected, true}, nil
		}
		if !errors.Is(cause, ErrPaperWriteDenied) && !errors.Is(cause, ErrPaperWriteUnknown) {
			return PaperFinalizeResult{}, errors.Join(cause, ctx.Err())
		}
	}
	if ctx.Err() != nil {
		return PaperFinalizeResult{}, errors.Join(ErrPaperWriteUnknown, ctx.Err())
	}
	f, err := ReadPaperRunFacts(ctx, db, key.Scope, key.RunID)
	if err != nil {
		return PaperFinalizeResult{}, paperWriteFailure(ctx, err)
	}
	if !req.bound(f) {
		return PaperFinalizeResult{}, ErrPaperWriteConflict
	}
	if !f.Completed {
		return PaperFinalizeResult{}, cause
	}
	c, err := req.completion(f, build)
	if ctx.Err() != nil {
		return PaperFinalizeResult{}, errors.Join(ErrPaperWriteUnknown, ctx.Err())
	}
	if err != nil {
		return PaperFinalizeResult{}, err
	}
	c.CompletedAt = f.Completion.CompletedAt
	if c != f.Completion || expected.Reason != "" && expected.Reason != c.Reason {
		return PaperFinalizeResult{}, ErrPaperWriteConflict
	}
	return PaperFinalizeResult{c, true}, nil
}

// bound сверяет адрес и последнюю сохранённую ревизию с исходным входом.
func (r PaperFinalizeRequest) bound(f PaperRunFacts) bool {
	h := f.Input.Header()
	return f.InputFound && f.PlanFound && h.Scope == paperwire.Scope(r.Key.Scope) && h.RunID == r.Key.RunID &&
		f.Scope == r.Key.Scope && f.RunID == r.Key.RunID && f.Input.Hash() == r.Key.InputHash && h.PlanHash == f.Plan.Hash() && f.Plan.Metadata().Revision != 0
}

// insert удерживает все ограждения до завершения собственной транзакции.
func (r PaperFinalizeRequest) insert(ctx context.Context, db *sql.DB, build PaperResolutionBuilder) (RunCompletion, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RunCompletion{}, errors.Join(ErrPaperWriteUnknown, ctx.Err())
	}
	defer tx.Rollback()
	f, err := lockPaperRunWrite(ctx, tx, r.Key, r.Owner)
	if err != nil {
		return RunCompletion{}, paperWriteFailure(ctx, err)
	}
	if !r.bound(f) {
		return RunCompletion{}, ErrPaperWriteConflict
	}
	p, c := f.Input.Values().Purpose, f.Control
	closeCommand := c.State.Mode == "HALTED" && c.Actor == "OPERATOR" && c.Action == "HALT" && c.Kind == "close-all"
	matches := p.CommandID == c.CommandID && p.ControlRevision == c.State.Revision
	superseded := r.Kind == "ABORTED" && p.CommandID != c.CommandID && p.ControlRevision < c.State.Revision
	if p.Kind == "close-all" && (!closeCommand || !matches && !superseded) ||
		r.Kind == "ABORTED" && (!closeCommand || r.CommandID != c.CommandID || r.ControlRevision != c.State.Revision) {
		return RunCompletion{}, ErrPaperWriteDenied
	}
	row, err := r.completion(f, build)
	if err != nil {
		return RunCompletion{}, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO momentum.run_completions(run_id,outcome,plan_revision,plan_hash,reason)
 VALUES($1,$2,$3::numeric,$4,$5) RETURNING completed_at`, row.RunID, string(row.Outcome), strconv.FormatUint(row.PlanRevision, 10), mustHex(row.PlanHash), row.Reason).Scan(&row.CompletedAt)
	if err != nil {
		return row, errors.Join(ErrPaperWriteUnknown, ctx.Err())
	}
	row.CompletedAt = row.CompletedAt.UTC()
	if err := tx.Commit(); err != nil {
		return row, errors.Join(ErrPaperWriteUnknown, ctx.Err())
	}
	return row, nil
}

// historicalProofError сохраняет причину непригодности исторического receipt.
type historicalProofError struct{ cause error }

// Error возвращает безопасную категорию исторического расхождения.
func (historicalProofError) Error() string { return ErrPaperWriteConflict.Error() }

// Is сопоставляет категорию и причину повторной финансовой проверки.
func (e historicalProofError) Is(target error) bool {
	return target == ErrPaperWriteConflict || errors.Is(e.cause, target)
}

// completion сопоставляет проверенный финансовый итог с ожидаемой строкой M14.
func (r PaperFinalizeRequest) completion(f PaperRunFacts, build PaperResolutionBuilder) (RunCompletion, error) {
	record, err := build(f)
	if err != nil {
		if f.Completed {
			return RunCompletion{}, historicalProofError{err}
		}
		return RunCompletion{}, paperWriteRejection{err}
	}
	d := record.Values()
	if record.Header() != f.Input.Header() || d.Kind != r.Kind || d.CommandID != r.CommandID || d.ControlRevision != r.ControlRevision ||
		d.InputHash != r.Key.InputHash || d.PlanHash != f.Plan.Hash() || d.RunID != r.Key.RunID {
		return RunCompletion{}, ErrPaperWriteConflict
	}
	outcome := RunCompleted
	if r.Kind == "ABORTED" {
		outcome = RunFailed
	}
	return RunCompletion{RunID: r.Key.RunID, Outcome: outcome, PlanRevision: f.Plan.Metadata().Revision, PlanHash: d.PlanHash, Reason: string(record.Payload())}, nil
}
