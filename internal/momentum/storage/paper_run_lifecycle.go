package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strconv"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

var (
	// ErrPaperRunLifecycleArgument означает недопустимый запрос.
	ErrPaperRunLifecycleArgument = errors.New("invalid paper run lifecycle argument")
	// ErrPaperRunLifecycleAbsent означает отсутствие запуска в указанной области.
	ErrPaperRunLifecycleAbsent = errors.New("paper run absent")
	// ErrPaperRunLifecycleCorrupt означает нарушение целостности данных запуска.
	ErrPaperRunLifecycleCorrupt = errors.New("corrupt paper run lifecycle")
	// ErrPaperRunLifecycleUnavailable означает ошибку доступа к снимку.
	ErrPaperRunLifecycleUnavailable = errors.New("paper run lifecycle unavailable")
)

// PaperRunLifecycle содержит сохранённые фазы одного запуска и исходный хеш конфигурации.
type PaperRunLifecycle struct {
	Scope             ControlScope
	RunID, ConfigHash string
	Input             paperwire.InputRecord
	InputFound        bool
	Plan              model.TradePlan
	PlanFound         bool
	Completion        RunCompletion
	Completed         bool
}

// validLifecycleRequest проверяет точный ключ запуска и область paper.
func validLifecycleRequest(ctx context.Context, scope ControlScope, run string) bool {
	return ctx != nil && paperControlScope(scope) && validRebalanceRunID(run)
}

// ReadPaperRunLifecycle читает фазы указанного запуска в одном снимке PostgreSQL.
func ReadPaperRunLifecycle(ctx context.Context, db *sql.DB, scope ControlScope, run string) (PaperRunLifecycle, error) {
	if db == nil || !validLifecycleRequest(ctx, scope, run) {
		return PaperRunLifecycle{}, ErrPaperRunLifecycleArgument
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return PaperRunLifecycle{}, errors.Join(ErrPaperRunLifecycleUnavailable, ctx.Err())
	}
	defer func() { _ = tx.Rollback() }()
	f, err := readPaperRunLifecycle(ctx, tx, scope, run)
	if err != nil {
		return PaperRunLifecycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return PaperRunLifecycle{}, errors.Join(ErrPaperRunLifecycleUnavailable, ctx.Err())
	}
	return f, nil
}

// readPaperRunLifecycle использует переданную транзакцию и отбрасывает частичный результат.
func readPaperRunLifecycle(ctx context.Context, tx *sql.Tx, scope ControlScope, run string) (PaperRunLifecycle, error) {
	if tx == nil || !validLifecycleRequest(ctx, scope, run) {
		return PaperRunLifecycle{}, ErrPaperRunLifecycleArgument
	}
	f := PaperRunLifecycle{Scope: scope, RunID: run}
	if err := f.readPhases(ctx, tx); err != nil {
		switch {
		case errors.Is(err, ErrPaperInputCorrupt), errors.Is(err, ErrPlanRevisionAbsent), errors.Is(err, ErrPlanRevisionCorrupt), errors.Is(err, paperwire.ErrPaperResourceLimit):
			err = errors.Join(ErrPaperRunLifecycleCorrupt, err)
		case errors.Is(err, ErrPlanRevision), errors.Is(err, ErrPaperInputUnavailable):
			err = errors.Join(ErrPaperRunLifecycleUnavailable, err)
		}
		return PaperRunLifecycle{}, errors.Join(err, ctx.Err())
	}
	return f, nil
}

// readPhases связывает исходную конфигурацию, точечный вход и последнюю ревизию.
func (f *PaperRunLifecycle) readPhases(ctx context.Context, tx *sql.Tx) error {
	var payload []byte
	var bound bool
	var sequence, revision sql.NullString
	var planSize int64
	err := tx.QueryRowContext(ctx, `SELECT encode(r.config_hash,'hex'),CASE WHEN r.config_hash=sha256(c.payload) THEN c.payload END,
 j.run_id IS NULL OR (j.environment=$2 AND j.account_id=r.account_id AND j.strategy_id=r.strategy_id),
 trim_scale(j.sequence)::text,trim_scale(p.revision)::text,COALESCE(octet_length(p.payload),0)
 FROM momentum.rebalance_runs r LEFT JOIN momentum.config_snapshots c USING(config_hash)
 LEFT JOIN momentum.paper_run_inputs j USING(run_id)
 LEFT JOIN LATERAL (SELECT revision,payload FROM momentum.plan_revisions WHERE run_id=r.run_id
 ORDER BY revision DESC LIMIT 1) p ON true WHERE r.run_id=$1 AND r.account_id=$3 AND r.strategy_id=$4`,
		f.RunID, f.Scope.Environment, f.Scope.AccountID, f.Scope.StrategyID).Scan(&f.ConfigHash, &payload, &bound, &sequence, &revision, &planSize)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPaperRunLifecycleAbsent
	}
	if err != nil {
		return ErrPaperRunLifecycleUnavailable
	}
	cfg, parseErr := config.Parse(payload)
	canonical, hash, snapshotErr := cfg.Snapshot()
	v := cfg.Values()
	if !bound || parseErr != nil || snapshotErr != nil || hash != f.ConfigHash || !bytes.Equal(canonical, payload) ||
		v.Mode != "paper" || v.StrategyID != f.Scope.StrategyID || v.DataSource.Kind != "bybit_testnet_public" || v.DataSource.Environment != "testnet" {
		return ErrPaperRunLifecycleCorrupt
	}
	if sequence.Valid {
		n, err := strconv.ParseUint(sequence.String, 10, 64)
		if err != nil || n == 0 {
			return ErrPaperRunLifecycleCorrupt
		}
		scope := paperwire.Scope(f.Scope)
		row, found, err := readPaperInput(ctx, tx, scope, n)
		if err != nil {
			return err
		}
		h := row.Input.Header()
		if !found || h.RunID != f.RunID || h.Scope != scope || h.Sequence != n || h.ConfigHash != f.ConfigHash {
			return ErrPaperRunLifecycleCorrupt
		}
		f.Input, f.InputFound = row.Input, true
	}
	if planSize > paperwire.MaxPlanBytes {
		return paperwire.ErrPaperResourceLimit
	}
	if revision.Valid {
		n, err := strconv.ParseUint(revision.String, 10, 64)
		if err != nil || n == 0 || !f.InputFound {
			return ErrPaperRunLifecycleCorrupt
		}
		row, err := readPlanRevision(ctx, tx, f.RunID, n)
		if err != nil {
			return err
		}
		if row.Plan.Hash() != f.Input.Header().PlanHash || f.Input.Values().Plan.Metadata().Revision != n {
			return ErrPaperRunLifecycleCorrupt
		}
		f.Plan, f.PlanFound = row.Plan, true
	}
	return f.readCompletion(ctx, tx, revision)
}

// readCompletion ограничивает размер причины в SQL и проверяет ссылку на последний план.
func (f *PaperRunLifecycle) readCompletion(ctx context.Context, tx *sql.Tx, latest sql.NullString) error {
	c := RunCompletion{RunID: f.RunID}
	var revision, hash sql.NullString
	var at sql.NullTime
	var oversized bool
	err := tx.QueryRowContext(ctx, `SELECT outcome,trim_scale(plan_revision)::text,encode(plan_hash,'hex'),
 octet_length(reason)>$2, CASE WHEN octet_length(reason)<=$2 THEN reason ELSE '' END,
 CASE WHEN isfinite(completed_at) THEN completed_at END FROM momentum.run_completions WHERE run_id=$1`,
		f.RunID, paperwire.MaxCanonicalBytes).Scan(&c.Outcome, &revision, &hash, &oversized, &c.Reason, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return ErrPaperRunLifecycleUnavailable
	}
	if oversized {
		return paperwire.ErrPaperResourceLimit
	}
	if revision != latest || revision.Valid != hash.Valid || !at.Valid || hash.String != f.Plan.Hash() {
		return ErrPaperRunLifecycleCorrupt
	}
	if revision.Valid {
		c.PlanRevision = f.Plan.Metadata().Revision
	}
	c.PlanHash, c.CompletedAt = hash.String, at.Time.UTC()
	if !validRunCompletion(c, len(c.PlanHash)/2) {
		return ErrPaperRunLifecycleCorrupt
	}
	if f.InputFound {
		if _, ok := decodePaperCompletion(f.Input, string(c.Outcome), c.Reason, revision.String, c.PlanHash); !ok {
			return ErrPaperRunLifecycleCorrupt
		}
	} else if c.Outcome != RunSkipped || f.PlanFound {
		return ErrPaperRunLifecycleCorrupt
	}
	f.Completion, f.Completed = c, true
	return nil
}
