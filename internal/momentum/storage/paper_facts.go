package storage

import (
	"context"
	"database/sql"
	"errors"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

var (
	// ErrPaperFactsAbsent означает отсутствие области или запрошенного run.
	ErrPaperFactsAbsent = errors.New("paper facts absent")
	// ErrPaperFactsCorrupt означает нарушение целостности сохранённых фактов.
	ErrPaperFactsCorrupt = errors.New("corrupt paper facts")
	// ErrPaperFactsUnavailable означает ошибку чтения снимка.
	ErrPaperFactsUnavailable = errors.New("paper facts unavailable")
	errPaperCommandSemantic  = errors.New("invalid persisted paper command")
)

// PaperRunFacts содержит подтверждённые факты выбранного run и текущий head области.
type PaperRunFacts struct {
	PaperExecutionRows
	Head                     paperwire.CycleBinding
	HeadFound                bool
	Input                    paperwire.InputRecord
	InputFound               bool
	Plan                     model.TradePlan
	PlanFound                bool
	Completion               RunCompletion
	Completed                bool
	Control                  PaperCommandState
	OwnerSessionID, DataHash string
}

// ReadPaperRunFacts читает единый неизменяемый снимок области paper.
func ReadPaperRunFacts(ctx context.Context, db *sql.DB, scope ControlScope, run string) (PaperRunFacts, error) {
	if db == nil || !validExecutionRead(ctx, scope, run) {
		return PaperRunFacts{}, ErrPaperFactsArgument
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return PaperRunFacts{}, errors.Join(ErrPaperFactsUnavailable, ctx.Err())
	}
	defer func() { _ = tx.Rollback() }()
	f, err := readPaperRunFacts(ctx, tx, scope, run)
	if err != nil {
		return PaperRunFacts{}, err
	}
	if err := tx.Commit(); err != nil {
		return PaperRunFacts{}, errors.Join(ErrPaperFactsUnavailable, ctx.Err())
	}
	return f, nil
}

// readPaperRunFacts собирает факты только в переданной транзакции.
func readPaperRunFacts(ctx context.Context, tx *sql.Tx, scope ControlScope, run string) (PaperRunFacts, error) {
	if tx == nil || !validExecutionRead(ctx, scope, run) {
		return PaperRunFacts{}, ErrPaperFactsArgument
	}
	var f PaperRunFacts
	if err := f.readSnapshot(ctx, tx, scope, run); err != nil {
		kind := ErrPaperFactsUnavailable
		switch {
		case errors.Is(err, ErrPaperFactsCorrupt), errors.Is(err, ErrPaperInputCorrupt), errors.Is(err, errPaperCommandSemantic),
			errors.Is(err, ErrPaperExecutionRowsCorrupt), errors.Is(err, paperwire.ErrPaperResourceLimit), errors.Is(err, ErrPaperRunEvidenceCorrupt):
			kind = ErrPaperFactsCorrupt
		case errors.Is(err, ErrControlAbsent), errors.Is(err, ErrPaperRunEvidenceAbsent):
			kind = ErrPaperFactsAbsent
		}
		return PaperRunFacts{}, errors.Join(kind, err, ctx.Err())
	}
	return f, nil
}

// readSnapshot связывает журнал, регистрацию владельца и проверенные строки исполнения.
func (f *PaperRunFacts) readSnapshot(ctx context.Context, tx *sql.Tx, scope ControlScope, run string) error {
	head, found, err := readPaperInputHead(ctx, tx, paperwire.Scope(scope))
	if err != nil {
		return err
	}
	f.Head, f.HeadFound = head.Input.Header(), found
	if run == "" {
		run = f.Head.RunID
	}
	f.Control, err = readPaperCommand(ctx, tx, scope, "")
	if err != nil {
		return err
	}
	c := f.Control
	if c.ActorID == "" {
		legacy := c.Actor == "SYSTEM" && c.Action == "INITIALIZE" && c.State.Mode == "ACTIVE" ||
			c.Actor == "RISK_CONTROLLER" && (c.Action == "PAUSE_ENTRIES" && c.State.Mode == "PAUSED_ENTRIES" || c.Action == "HALT" && c.State.Mode == "HALTED")
		if !legacy || c.CommandID != "" || c.RecoveryID != "" {
			return ErrPaperFactsCorrupt
		}
	} else if paperCommandMode(c.Action, c.Kind, c.Actor) == "" {
		return ErrPaperFactsCorrupt
	}
	var unjournaled bool
	err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT session_id FROM momentum.execution_owner_sessions
 WHERE environment=$1 AND account_id=$2 AND strategy_id=$3 AND ended_at IS NULL),''),
 EXISTS(SELECT FROM momentum.rebalance_runs r WHERE r.account_id=$2 AND r.strategy_id=$3
 AND (EXISTS(SELECT FROM momentum.plan_revisions p WHERE p.run_id=r.run_id)
 OR EXISTS(SELECT FROM momentum.order_intents i WHERE i.run_id=r.run_id))
 AND NOT EXISTS(SELECT FROM momentum.paper_run_inputs j WHERE j.run_id=r.run_id
 AND j.environment=$1 AND j.account_id=$2 AND j.strategy_id=$3))`, scope.Environment, scope.AccountID, scope.StrategyID).Scan(&f.OwnerSessionID, &unjournaled)
	if err != nil {
		return ErrPaperFactsUnavailable
	}
	if !found {
		probe, err := readPaperExecutionRows(ctx, tx, scope, "")
		if err != nil {
			return err
		}
		f.PaperExecutionRows = probe
		unjournaled = unjournaled || probe.ScopeHasIntents
	}
	if unjournaled {
		return ErrPaperFactsCorrupt
	}
	if run == "" {
		f.DataHash = paperRunDataHash(f.PaperExecutionRows, PaperRunLifecycle{})
		return nil
	}
	evidence, err := readPaperRunEvidence(ctx, tx, scope, run)
	if err != nil {
		if run == f.Head.RunID && errors.Is(err, ErrPaperRunEvidenceAbsent) {
			return errors.Join(ErrPaperFactsCorrupt, err)
		}
		return err
	}
	life := evidence.Lifecycle
	if evidence.Scope != scope || evidence.RunID != run || life.ConfigHash != c.ConfigHash ||
		run == f.Head.RunID && (!life.InputFound || life.Input.Header() != f.Head) {
		return ErrPaperFactsCorrupt
	}
	f.PaperExecutionRows, f.DataHash = evidence.PaperExecutionRows, evidence.DataHash
	f.Input, f.InputFound = life.Input, life.InputFound
	f.Plan, f.PlanFound = life.Plan, life.PlanFound
	f.Completion, f.Completed = life.Completion, life.Completed
	return nil
}
