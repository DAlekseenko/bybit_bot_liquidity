package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strconv"

	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// ErrPaperInputConflict означает несовместимый вход или отсутствующую предпосылку записи.
var ErrPaperInputConflict = errors.New("paper input conflict")

// paperInputStoreHooks проверяет стадии чтения и подтверждения текущей транзакции.
type paperInputStoreHooks struct {
	beforeReadback func(*sql.Tx) error
	beforeCommit   func(*sql.Tx) error
}

// StorePaperInput удерживает controls SHARE до подтверждённого commit.
func StorePaperInput(ctx context.Context, db *sql.DB, input paperwire.InputRecord) (StoredPaperInput, error) {
	return storePaperInput(ctx, db, input, paperInputStoreHooks{})
}

// storePaperInput позволяет проверить неизвестный commit без transport effects.
func storePaperInput(ctx context.Context, db *sql.DB, input paperwire.InputRecord, hooks paperInputStoreHooks) (StoredPaperInput, error) {
	h := input.Header()
	if ctx == nil || db == nil || !paperInputScope(h.Scope) || input.Hash() == "" || h.ModelHash != input.Values().Run.Config.Values().ModelID {
		return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputArgument)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputUnavailable)
	}
	defer tx.Rollback()
	if err = lockPaperControls(ctx, tx, false); err != nil {
		return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputUnavailable)
	}
	head, found, err := readPaperInputHead(ctx, tx, h.Scope)
	if err != nil {
		return StoredPaperInput{}, err
	}
	if found && head.Input.Header().Sequence >= h.Sequence {
		saved, exists, e := readPaperInput(ctx, tx, h.Scope, h.Sequence)
		if e != nil {
			return StoredPaperInput{}, e
		}
		if exists && bytes.Equal(saved.Input.Payload(), input.Payload()) {
			return saved, nil
		}
		return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputConflict)
	}
	if (!found && h.Sequence != 1) || found && (head.Input.Header().Sequence != h.Sequence-1 || head.Input.Hash() != h.PreviousInputHash) {
		return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputConflict)
	}
	if found {
		var outcome, reason, revision, planHash sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT outcome,reason,
 CASE WHEN plan_revision BETWEEN 1 AND 18446744073709551615 AND plan_revision=trunc(plan_revision)
      THEN trunc(plan_revision)::text END,encode(plan_hash,'hex')
 FROM momentum.run_completions WHERE run_id=$1 AND isfinite(completed_at)`, head.Input.Header().RunID).
			Scan(&outcome, &reason, &revision, &planHash)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputUnavailable)
		}
		receipt, valid := decodePaperCompletion(head.Input, outcome.String, reason.String, revision.String, planHash.String)
		if !valid || receipt.Hash() != h.PreviousResolutionHash {
			return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputConflict)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO momentum.paper_run_inputs
 (run_id,environment,account_id,strategy_id,sequence,previous_input_hash,previous_resolution_hash,input_hash,payload)
 SELECT $1,$2,$3,$4,$5::numeric,$6,$7,$8,$9 FROM momentum.rebalance_runs r
 JOIN momentum.config_snapshots c ON c.config_hash=r.config_hash
 WHERE r.run_id=$1 AND r.account_id=$3 AND r.strategy_id=$4 AND r.scheduled_at=$10 AND r.config_hash=$11 AND c.payload=$12
 AND r.onboarding_id IS NOT DISTINCT FROM NULLIF($13,'')
 ON CONFLICT DO NOTHING`, h.RunID, h.Scope.Environment, h.Scope.AccountID, h.Scope.StrategyID, strconv.FormatUint(h.Sequence, 10),
		mustHex(h.PreviousInputHash), mustHex(h.PreviousResolutionHash), mustHex(input.Hash()), input.Payload(), h.ScheduledAt, mustHex(h.ConfigHash), inputConfigBytes(input), input.Values().Run.OnboardingID)
	if err != nil {
		return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputUnavailable)
	}
	if hooks.beforeReadback != nil {
		if err = hooks.beforeReadback(tx); err != nil {
			return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputUnavailable)
		}
	}
	saved, found, err := readPaperInput(ctx, tx, h.Scope, h.Sequence)
	if err != nil {
		return StoredPaperInput{}, err
	}
	if !found || !bytes.Equal(saved.Input.Payload(), input.Payload()) {
		return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputConflict)
	}
	if hooks.beforeCommit != nil {
		if err = hooks.beforeCommit(tx); err != nil {
			return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputUnavailable)
		}
	}
	if err = tx.Commit(); err != nil {
		return StoredPaperInput{}, paperInputFailure(ctx, ErrPaperInputUnavailable)
	}
	return saved, nil
}

// inputConfigBytes берёт canonical bytes проверенного sealed input.
func inputConfigBytes(input paperwire.InputRecord) []byte {
	payload, _, _ := input.Values().Run.Config.Snapshot()
	return payload
}
