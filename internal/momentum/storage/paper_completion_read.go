package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"bybit_bot_liquidity/internal/momentum/paperwire"
)

var (
	// ErrPaperCompletionArgument означает неподходящий аргумент чтения.
	ErrPaperCompletionArgument = errors.New("invalid paper completion argument")
	// ErrPaperCompletionCorrupt означает повреждение сохранённой записи или её связей.
	ErrPaperCompletionCorrupt = errors.New("corrupt paper completion")
	// ErrPaperCompletionUnavailable означает, что получить результат чтения не удалось.
	ErrPaperCompletionUnavailable = errors.New("paper completion unavailable")
)

// paperCompletionReader читает строку через пул или переданную транзакцию.
type paperCompletionReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ReadPaperCompletion возвращает подтверждённый receipt конкретного immutable входа.
func ReadPaperCompletion(ctx context.Context, db *sql.DB, input paperwire.InputRecord) (paperwire.ResolutionRecord, bool, error) {
	if db == nil {
		return paperwire.ResolutionRecord{}, false, ErrPaperCompletionArgument
	}
	return readPaperCompletion(ctx, db, input)
}

// readPaperCompletion проверяет вход, run, конфигурацию и результат на одном снимке.
func readPaperCompletion(ctx context.Context, q paperCompletionReader, input paperwire.InputRecord) (paperwire.ResolutionRecord, bool, error) {
	h := input.Header()
	if ctx == nil || input.Hash() == "" || !paperControlScope(ControlScope(h.Scope)) {
		return paperwire.ResolutionRecord{}, false, ErrPaperCompletionArgument
	}
	run := input.Values().Run
	configBytes, configHash, err := run.Config.Snapshot()
	if err != nil || configHash != h.ConfigHash || h.ModelHash != run.Config.Values().ModelID {
		return paperwire.ResolutionRecord{}, false, ErrPaperCompletionArgument
	}

	var valid sql.NullBool
	var scheduled sql.NullTime
	var outcome, reason, revision, planHash sql.NullString
	err = q.QueryRowContext(ctx, `SELECT
 i.payload=$2 AND i.input_hash=pg_catalog.sha256(i.payload) AND i.input_hash=decode($3,'hex')
 AND i.environment=$4 AND i.account_id=$5 AND i.strategy_id=$6 AND i.sequence=$7::numeric
 AND i.previous_input_hash IS NOT DISTINCT FROM decode(nullif($8,''),'hex')
 AND i.previous_resolution_hash IS NOT DISTINCT FROM decode(nullif($9,''),'hex')
 AND r.account_id=$5 AND r.strategy_id=$6 AND r.onboarding_id IS NOT DISTINCT FROM nullif($12,'')
 AND r.config_hash=decode($10,'hex') AND c.payload=$11 AND isfinite(x.completed_at),
 CASE WHEN isfinite(r.scheduled_at) THEN r.scheduled_at END,
 x.outcome,x.reason,
 CASE WHEN x.plan_revision BETWEEN 1 AND 18446744073709551615 AND x.plan_revision=trunc(x.plan_revision)
      THEN trunc(x.plan_revision)::text END,encode(x.plan_hash,'hex')
 FROM momentum.run_completions x
 LEFT JOIN momentum.paper_run_inputs i ON i.run_id=x.run_id
 LEFT JOIN momentum.rebalance_runs r ON r.run_id=x.run_id
 LEFT JOIN momentum.config_snapshots c ON c.config_hash=r.config_hash
 WHERE x.run_id=$1`, h.RunID, input.Payload(), input.Hash(), h.Scope.Environment, h.Scope.AccountID, h.Scope.StrategyID,
		strconv.FormatUint(h.Sequence, 10), h.PreviousInputHash, h.PreviousResolutionHash, configHash, configBytes, run.OnboardingID).
		Scan(&valid, &scheduled, &outcome, &reason, &revision, &planHash)
	if errors.Is(err, sql.ErrNoRows) {
		return paperwire.ResolutionRecord{}, false, nil
	}
	if err != nil {
		return paperwire.ResolutionRecord{}, false, errors.Join(ErrPaperCompletionUnavailable, ctx.Err())
	}
	if !valid.Bool || !scheduled.Valid || !scheduled.Time.Equal(h.ScheduledAt) {
		return paperwire.ResolutionRecord{}, false, ErrPaperCompletionCorrupt
	}
	record, ok := decodePaperCompletion(input, outcome.String, reason.String, revision.String, planHash.String)
	if !ok {
		return paperwire.ResolutionRecord{}, false, ErrPaperCompletionCorrupt
	}
	return record, true, nil
}

// decodePaperCompletion сверяет точные байты receipt с исходом и ревизией плана.
func decodePaperCompletion(input paperwire.InputRecord, outcome, reason, revision, planHash string) (paperwire.ResolutionRecord, bool) {
	record, err := paperwire.DecodeResolutionRecord([]byte(reason), input)
	if err != nil || planHash != input.Header().PlanHash || revision != strconv.FormatUint(input.Values().Plan.Metadata().Revision, 10) {
		return paperwire.ResolutionRecord{}, false
	}
	kind := record.Values().Kind
	if (outcome == "COMPLETED" && kind == "COMPLETED") || (outcome == "FAILED" && kind == "ABORTED") {
		return record, true
	}
	return paperwire.ResolutionRecord{}, false
}
