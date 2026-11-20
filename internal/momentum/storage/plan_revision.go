package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
	"github.com/lib/pq"
)

var (
	// ErrPlanRevision обозначает неподтверждённую операцию с immutable revision.
	ErrPlanRevision = errors.New("momentum plan revision repository failed")
	// ErrPlanRevisionArgument обозначает непригодный ключ, план или отсутствующую зависимость.
	ErrPlanRevisionArgument = errors.New("invalid momentum plan revision argument")
	// ErrPlanRevisionAbsent обозначает отсутствующий run либо запрошенную revision.
	ErrPlanRevisionAbsent = errors.New("momentum plan revision or run absent")
	// ErrPlanRevisionMismatch обозначает несовместимость с исходным run/config/predecessor.
	ErrPlanRevisionMismatch = errors.New("momentum plan revision identity mismatch")
	// ErrPlanRevisionConflict обозначает другое содержимое уже занятой revision.
	ErrPlanRevisionConflict = errors.New("momentum plan revision conflict")
	// ErrPlanRevisionCorrupt обозначает непроверяемые сохранённые bytes или metadata.
	ErrPlanRevisionCorrupt = errors.New("invalid stored momentum plan revision")
)

// StoredPlanRevision содержит восстановленный immutable план и исходный SQL timestamp.
type StoredPlanRevision struct {
	Plan      model.TradePlan
	CreatedAt time.Time
}

type storedPlanRevisionRow struct {
	planHash, previous, payload, payloadHash []byte
	created                                  sql.NullTime
}

type planRevisionRunRow struct {
	account, strategy                 string
	scheduled, created, configCreated sql.NullTime
	key, payload                      []byte
}

// StorePlanRevision добавляет одну revision либо подтверждает полностью совпадающую запись.
func StorePlanRevision(ctx context.Context, db *sql.DB, plan model.TradePlan) (StoredPlanRevision, error) {
	meta := plan.Metadata()
	payload, encodeErr := model.EncodeTradePlan(plan)
	if err := planRevisionArguments(ctx, db, meta.RunID, meta.Revision); err != nil {
		return StoredPlanRevision{}, err
	}
	if encodeErr != nil {
		return StoredPlanRevision{}, planRevisionFailure(ctx, ErrPlanRevisionArgument)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StoredPlanRevision{}, planRevisionFailure(ctx, nil)
	}
	defer tx.Rollback()
	if err := checkPlanRevisionRun(ctx, tx, plan); err != nil {
		return StoredPlanRevision{}, err
	}
	if meta.Revision > 1 {
		previous, err := readPlanRevision(ctx, tx, meta.RunID, meta.Revision-1)
		if err != nil {
			return StoredPlanRevision{}, err
		}
		if previous.Plan.Hash() != meta.PreviousHash {
			return StoredPlanRevision{}, planRevisionFailure(ctx, ErrPlanRevisionMismatch)
		}
	}
	planKey, _ := hex.DecodeString(plan.Hash())
	var previousKey []byte
	if meta.PreviousHash != "" {
		previousKey, _ = hex.DecodeString(meta.PreviousHash)
	}
	digest := sha256.Sum256(payload)
	_, err = tx.ExecContext(ctx, `INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,previous_hash,payload,payload_sha256)
 VALUES ($1,$2::numeric,$3,$4,$5,$6) ON CONFLICT (run_id,revision) DO NOTHING`, meta.RunID, strconv.FormatUint(meta.Revision, 10), planKey, previousKey, payload, digest[:])
	if err != nil {
		var failure *pq.Error
		if errors.As(err, &failure) && failure.Code == "23503" {
			return StoredPlanRevision{}, planRevisionFailure(ctx, ErrPlanRevisionMismatch)
		}
		return StoredPlanRevision{}, planRevisionFailure(ctx, nil)
	}
	stored, err := readPlanRevision(ctx, tx, meta.RunID, meta.Revision)
	if err != nil {
		return StoredPlanRevision{}, err
	}
	actual, _ := model.EncodeTradePlan(stored.Plan)
	if stored.Plan.Hash() != plan.Hash() || !bytes.Equal(actual, payload) {
		return StoredPlanRevision{}, planRevisionFailure(ctx, ErrPlanRevisionConflict)
	}
	if err := tx.Commit(); err != nil {
		return StoredPlanRevision{}, planRevisionFailure(ctx, nil)
	}
	return stored, nil
}

// ReadPlanRevision восстанавливает ровно одну revision и проверяет её исходный run/config.
func ReadPlanRevision(ctx context.Context, db *sql.DB, runID string, revision uint64) (StoredPlanRevision, error) {
	if err := planRevisionArguments(ctx, db, runID, revision); err != nil {
		return StoredPlanRevision{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return StoredPlanRevision{}, planRevisionFailure(ctx, nil)
	}
	defer tx.Rollback()
	stored, err := readPlanRevision(ctx, tx, runID, revision)
	if err != nil {
		return StoredPlanRevision{}, err
	}
	if err := tx.Commit(); err != nil {
		return StoredPlanRevision{}, planRevisionFailure(ctx, nil)
	}
	return stored, nil
}

// readPlanRevision проверяет exact SQL columns и не выдаёт частичный результат.
func readPlanRevision(ctx context.Context, tx *sql.Tx, runID string, revision uint64) (StoredPlanRevision, error) {
	var row storedPlanRevisionRow
	err := tx.QueryRowContext(ctx, `SELECT plan_hash,previous_hash,payload,payload_sha256,
 CASE WHEN isfinite(created_at) AND created_at>='epoch'::timestamptz THEN created_at END
 FROM momentum.plan_revisions WHERE run_id=$1 AND revision=$2::numeric`, runID, strconv.FormatUint(revision, 10)).Scan(&row.planHash, &row.previous, &row.payload, &row.payloadHash, &row.created)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredPlanRevision{}, planRevisionFailure(ctx, ErrPlanRevisionAbsent)
	}
	if err != nil {
		return StoredPlanRevision{}, planRevisionFailure(ctx, nil)
	}
	stored, err := decodeStoredPlanRevision(row, runID, revision)
	if err != nil {
		return StoredPlanRevision{}, planRevisionFailure(ctx, err)
	}
	if err := checkPlanRevisionRun(ctx, tx, stored.Plan); err != nil {
		if errors.Is(err, ErrPlanRevisionMismatch) {
			return StoredPlanRevision{}, planRevisionFailure(ctx, ErrPlanRevisionCorrupt)
		}
		return StoredPlanRevision{}, err
	}
	return stored, nil
}

// decodeStoredPlanRevision связывает model hash, payload digest и exact row key.
func decodeStoredPlanRevision(row storedPlanRevisionRow, runID string, revision uint64) (StoredPlanRevision, error) {
	digest := sha256.Sum256(row.payload)
	plan, err := model.DecodeTradePlan(row.payload)
	meta := plan.Metadata()
	previous := hex.EncodeToString(row.previous)
	if err != nil || !row.created.Valid || len(row.planHash) != sha256.Size || len(row.payloadHash) != sha256.Size ||
		!bytes.Equal(digest[:], row.payloadHash) || plan.Hash() != hex.EncodeToString(row.planHash) ||
		meta.RunID != runID || meta.Revision != revision || meta.PreviousHash != previous ||
		(revision == 1 && row.previous != nil) || (revision > 1 && len(row.previous) != sha256.Size) {
		return StoredPlanRevision{}, ErrPlanRevisionCorrupt
	}
	return StoredPlanRevision{Plan: plan, CreatedAt: row.created.Time.UTC()}, nil
}

// checkPlanRevisionRun проверяет существующую identity и canonical config без выбора financial rules.
func checkPlanRevisionRun(ctx context.Context, tx *sql.Tx, plan model.TradePlan) error {
	var row planRevisionRunRow
	err := tx.QueryRowContext(ctx, `SELECT r.account_id,r.strategy_id,
 CASE WHEN isfinite(r.scheduled_at) AND r.scheduled_at>='epoch'::timestamptz THEN r.scheduled_at END,
 CASE WHEN isfinite(r.created_at) AND r.created_at>='epoch'::timestamptz THEN r.created_at END,
 r.config_hash,c.payload,
 CASE WHEN isfinite(c.created_at) AND c.created_at>='epoch'::timestamptz THEN c.created_at END
 FROM momentum.rebalance_runs r JOIN momentum.config_snapshots c ON c.config_hash=r.config_hash WHERE r.run_id=$1`, plan.Metadata().RunID).Scan(&row.account, &row.strategy, &row.scheduled, &row.created, &row.key, &row.payload, &row.configCreated)
	if errors.Is(err, sql.ErrNoRows) {
		return planRevisionFailure(ctx, ErrPlanRevisionAbsent)
	}
	if err != nil {
		return planRevisionFailure(ctx, nil)
	}
	if err := validatePlanRevisionRun(row, plan); err != nil {
		return planRevisionFailure(ctx, err)
	}
	return nil
}

// validatePlanRevisionRun проверяет загруженную identity/config независимо от SQL transport.
func validatePlanRevisionRun(row planRevisionRunRow, plan model.TradePlan) error {
	meta, snapshot := plan.Metadata(), plan.Snapshot()
	cfg, parseErr := config.Parse(row.payload)
	canonical, hash, snapshotErr := cfg.Snapshot()
	digest := sha256.Sum256(row.payload)
	if parseErr != nil || snapshotErr != nil || len(row.key) != sha256.Size || !bytes.Equal(row.key, digest[:]) ||
		!bytes.Equal(canonical, row.payload) || !row.scheduled.Valid || !row.created.Valid || !row.configCreated.Valid || cfg.Values().StrategyID != row.strategy {
		return ErrPlanRevisionCorrupt
	}
	values := cfg.Values()
	if meta.ConfigHash != hash || meta.StrategyID != row.strategy || snapshot.AccountID() != row.account ||
		!meta.ScheduledAt.Equal(row.scheduled.Time) || meta.ProfileID != values.Profile || meta.ModelID != values.ModelID || snapshot.Environment() != values.Mode {
		return ErrPlanRevisionMismatch
	}
	return nil
}

// planRevisionArguments проверяет SQL key без потери uint64 или нормализации ID.
func planRevisionArguments(ctx context.Context, db *sql.DB, runID string, revision uint64) error {
	if ctx == nil || db == nil || revision == 0 || !utf8.ValidString(runID) || strings.ContainsRune(runID, 0) || strings.Trim(runID, " \t\n\r\f\v") == "" {
		return errors.Join(ErrPlanRevision, ErrPlanRevisionArgument)
	}
	if ctx.Err() != nil {
		return planRevisionFailure(ctx, nil)
	}
	return nil
}

// planRevisionFailure сохраняет cancellation и безопасную категорию без raw SQL.
func planRevisionFailure(ctx context.Context, kind error) error {
	return errors.Join(ErrPlanRevision, kind, ctx.Err())
}
