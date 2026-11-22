package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"
)

var (
	// ErrRunCompletion — общая категория ошибок итога run.
	ErrRunCompletion = errors.New("momentum run completion repository failed")
	// ErrRunCompletionArgument означает невалидный итог до обращения к базе.
	ErrRunCompletionArgument = errors.New("invalid momentum run completion argument")
	// ErrRunCompletionAbsent означает отсутствие run или указанной plan revision.
	ErrRunCompletionAbsent = errors.New("momentum run or plan revision absent")
	// ErrRunCompletionConflict означает другой сохранённый итог или не последнюю revision.
	ErrRunCompletionConflict = errors.New("momentum run completion conflict")
	// ErrRunCompleted означает попытку добавить plan revision в завершённый run.
	ErrRunCompleted = errors.New("momentum run is completed")
)

// RunOutcome — итог run.
type RunOutcome string

const (
	// RunCompleted — план исполнен до конца.
	RunCompleted RunOutcome = "COMPLETED"
	// RunSkipped — run пропущен (например, неделя без catch-up).
	RunSkipped RunOutcome = "SKIPPED"
	// RunFailed — run остановлен fail-closed.
	RunFailed RunOutcome = "FAILED"
)

// RunCompletion — неизменяемый итог run; PlanRevision 0 означает «планов не было».
type RunCompletion struct {
	RunID        string
	Outcome      RunOutcome
	PlanRevision uint64
	PlanHash     string
	Reason       string
	CompletedAt  time.Time
}

// CompleteRebalanceRun фиксирует итог run. Повтор того же итога возвращает
// сохранённую запись; другой итог или ссылка не на последнюю revision — конфликт.
func CompleteRebalanceRun(ctx context.Context, db *sql.DB, c RunCompletion) (RunCompletion, error) {
	planKey, err := hex.DecodeString(c.PlanHash)
	if ctx == nil || db == nil || err != nil || !validRunCompletion(c, len(planKey)) {
		return RunCompletion{}, errors.Join(ErrRunCompletion, ErrRunCompletionArgument)
	}
	var revision any
	if c.PlanRevision != 0 {
		revision = strconv.FormatUint(c.PlanRevision, 10)
	} else {
		planKey = nil
	}
	_, err = db.ExecContext(ctx, `INSERT INTO momentum.run_completions(run_id,outcome,plan_revision,plan_hash,reason)
 VALUES ($1,$2,$3::numeric,$4,$5) ON CONFLICT (run_id) DO NOTHING`, c.RunID, string(c.Outcome), revision, planKey, c.Reason)
	var failure *pq.Error
	switch {
	case errors.As(err, &failure) && failure.Code == "23503":
		return RunCompletion{}, errors.Join(ErrRunCompletion, ErrRunCompletionAbsent, ctx.Err())
	case errors.As(err, &failure) && failure.Code == "23514":
		return RunCompletion{}, errors.Join(ErrRunCompletion, ErrRunCompletionConflict, ctx.Err())
	case err != nil:
		return RunCompletion{}, errors.Join(ErrRunCompletion, ctx.Err())
	}
	stored, found, err := ReadRunCompletion(ctx, db, c.RunID)
	if err != nil {
		return RunCompletion{}, err
	}
	if !found {
		return RunCompletion{}, errors.Join(ErrRunCompletion, ctx.Err())
	}
	if stored.Outcome != c.Outcome || stored.PlanRevision != c.PlanRevision || stored.Reason != c.Reason ||
		!bytes.Equal(mustHex(stored.PlanHash), planKey) {
		return RunCompletion{}, errors.Join(ErrRunCompletion, ErrRunCompletionConflict)
	}
	return stored, nil
}

// ReadRunCompletion читает итог run; found=false — run ещё не завершён.
func ReadRunCompletion(ctx context.Context, db *sql.DB, runID string) (RunCompletion, bool, error) {
	if ctx == nil || db == nil || !validRunID(runID) {
		return RunCompletion{}, false, errors.Join(ErrRunCompletion, ErrRunCompletionArgument)
	}
	var c RunCompletion
	var outcome string
	var revision sql.NullString
	var planKey []byte
	err := db.QueryRowContext(ctx, `SELECT run_id,outcome,plan_revision::text,plan_hash,reason,completed_at
 FROM momentum.run_completions WHERE run_id=$1`, runID).Scan(&c.RunID, &outcome, &revision, &planKey, &c.Reason, &c.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RunCompletion{}, false, nil
	}
	if err != nil {
		return RunCompletion{}, false, errors.Join(ErrRunCompletion, ctx.Err())
	}
	c.Outcome, c.CompletedAt, c.PlanHash = RunOutcome(outcome), c.CompletedAt.UTC(), hex.EncodeToString(planKey)
	if revision.Valid {
		if c.PlanRevision, err = strconv.ParseUint(revision.String, 10, 64); err != nil {
			return RunCompletion{}, false, errors.Join(ErrRunCompletion, ErrPlanRevisionCorrupt)
		}
	}
	return c, true, nil
}

// ReadLatestPlanRevision восстанавливает последнюю revision run для resume после
// рестарта; found=false — планов ещё нет. Run и его config берутся из сохранённой
// записи (CreateOrReadRebalanceRun), поэтому resume не подменяет конфигурацию.
func ReadLatestPlanRevision(ctx context.Context, db *sql.DB, runID string) (StoredPlanRevision, bool, error) {
	if err := planRevisionArguments(ctx, db, runID, 1); err != nil {
		return StoredPlanRevision{}, false, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return StoredPlanRevision{}, false, planRevisionFailure(ctx, nil)
	}
	defer tx.Rollback()
	var latest sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT max(revision)::text FROM momentum.plan_revisions WHERE run_id=$1`, runID).Scan(&latest)
	if err != nil {
		return StoredPlanRevision{}, false, planRevisionFailure(ctx, nil)
	}
	if !latest.Valid {
		return StoredPlanRevision{}, false, nil
	}
	revision, err := strconv.ParseUint(latest.String, 10, 64)
	if err != nil {
		return StoredPlanRevision{}, false, planRevisionFailure(ctx, ErrPlanRevisionCorrupt)
	}
	stored, err := readPlanRevision(ctx, tx, runID, revision)
	if err != nil {
		return StoredPlanRevision{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return StoredPlanRevision{}, false, planRevisionFailure(ctx, nil)
	}
	return stored, true, nil
}

// validRunCompletion проверяет согласованность итога, ссылки на план и причины.
func validRunCompletion(c RunCompletion, hashLen int) bool {
	switch c.Outcome {
	case RunCompleted, RunSkipped, RunFailed:
	default:
		return false
	}
	withPlan := c.PlanRevision != 0
	return validRunID(c.RunID) && withPlan == (hashLen == 32) && (withPlan || c.PlanHash == "") &&
		(c.Outcome != RunCompleted || withPlan) && utf8.ValidString(c.Reason) &&
		!strings.ContainsRune(c.Reason, 0) && strings.TrimSpace(c.Reason) != ""
}

// validRunID отвергает пустой, не-UTF-8 или содержащий NUL идентификатор run.
func validRunID(id string) bool {
	return utf8.ValidString(id) && !strings.ContainsRune(id, 0) && strings.TrimSpace(id) != ""
}

// mustHex декодирует hex, сохранённый этим же пакетом; пустая строка даёт nil.
func mustHex(s string) []byte {
	if s == "" {
		return nil
	}
	b, _ := hex.DecodeString(s)
	return b
}
