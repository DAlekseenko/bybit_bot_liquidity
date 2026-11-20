package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"bybit_bot_liquidity/internal/momentum/config"
)

var (
	// ErrRebalanceRun обозначает неподтверждённую операцию без обещания отсутствия commit.
	ErrRebalanceRun = errors.New("momentum run repository failed")
	// ErrRebalanceRunArgument обозначает невалидный кандидат или отсутствующую зависимость.
	ErrRebalanceRunArgument = errors.New("invalid momentum run argument")
	// ErrRebalanceRunCollision обозначает ID, занятый другим логическим запуском.
	ErrRebalanceRunCollision = errors.New("momentum run identity collision")
	// ErrRebalanceRunCorrupt обозначает непроверяемую сохранённую идентичность или конфиг.
	ErrRebalanceRunCorrupt = errors.New("invalid stored momentum run")
)

// RebalanceRunInput задаёт кандидата; пустой OnboardingID означает weekly key.
type RebalanceRunInput struct {
	RunID, AccountID, StrategyID, OnboardingID string
	ScheduledAt                                time.Time
	Config                                     config.Config
}

// StoredRebalanceRun содержит исходные identity, конфиг и точные bytes его снимка.
type StoredRebalanceRun struct {
	RebalanceRunInput
	ConfigHash    string
	ConfigPayload []byte
	CreatedAt     time.Time
}

// CreateOrReadRebalanceRun сохраняет кандидата или возвращает проверенный исходный запуск.
func CreateOrReadRebalanceRun(ctx context.Context, db *sql.DB, input RebalanceRunInput) (StoredRebalanceRun, error) {
	if ctx == nil || db == nil {
		return StoredRebalanceRun{}, errors.Join(ErrRebalanceRun, ErrRebalanceRunArgument)
	}
	if err := ctx.Err(); err != nil {
		return StoredRebalanceRun{}, errors.Join(ErrRebalanceRun, err)
	}
	payload, hash, err := input.Config.Snapshot()
	if err != nil || !validRebalanceRunInput(input) {
		return StoredRebalanceRun{}, errors.Join(ErrRebalanceRun, ErrRebalanceRunArgument)
	}
	validated, err := config.Parse(payload)
	if err != nil || validated.Values().StrategyID != input.StrategyID {
		return StoredRebalanceRun{}, errors.Join(ErrRebalanceRun, ErrRebalanceRunArgument)
	}
	key, _ := hex.DecodeString(hash)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StoredRebalanceRun{}, rebalanceRunFailure(ctx, nil)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO momentum.config_snapshots(config_hash,payload)
 VALUES ($1,$2) ON CONFLICT (config_hash) DO NOTHING`, key, payload)
	if err != nil {
		return StoredRebalanceRun{}, rebalanceRunFailure(ctx, nil)
	}
	var saved []byte
	var stamp sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT payload,CASE WHEN isfinite(created_at) AND created_at>='epoch'::timestamptz
 THEN created_at END FROM momentum.config_snapshots WHERE config_hash=$1 FOR SHARE`, key).Scan(&saved, &stamp)
	if err != nil {
		return StoredRebalanceRun{}, rebalanceRunFailure(ctx, nil)
	}
	if !stamp.Valid || !bytes.Equal(saved, payload) {
		return StoredRebalanceRun{}, rebalanceRunFailure(ctx, ErrRebalanceRunCorrupt)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,onboarding_id,config_hash)
 VALUES ($1,$2,$3,$4,NULLIF($5,''),$6) ON CONFLICT DO NOTHING`, input.RunID, input.AccountID, input.StrategyID, input.ScheduledAt.UTC(), input.OnboardingID, key)
	if err != nil {
		return StoredRebalanceRun{}, rebalanceRunFailure(ctx, nil)
	}
	var stored StoredRebalanceRun
	var storedKey []byte
	var onboarding sql.NullString
	var created sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT r.run_id,r.account_id,r.strategy_id,r.scheduled_at,r.onboarding_id,r.config_hash,
 CASE WHEN isfinite(r.created_at) AND r.created_at>='epoch'::timestamptz THEN r.created_at END,
 c.payload,CASE WHEN isfinite(c.created_at) AND c.created_at>='epoch'::timestamptz THEN c.created_at END
 FROM momentum.rebalance_runs r JOIN momentum.config_snapshots c ON c.config_hash=r.config_hash
 WHERE r.account_id=$1 AND r.strategy_id=$2 AND
 (($3='' AND r.onboarding_id IS NULL AND r.scheduled_at=$4) OR ($3<>'' AND r.onboarding_id=$3))
 FOR SHARE OF r,c`, input.AccountID, input.StrategyID, input.OnboardingID, input.ScheduledAt.UTC()).Scan(
		&stored.RunID, &stored.AccountID, &stored.StrategyID, &stored.ScheduledAt, &onboarding, &storedKey,
		&created, &stored.ConfigPayload, &stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredRebalanceRun{}, rebalanceRunFailure(ctx, ErrRebalanceRunCollision)
	}
	if err != nil {
		return StoredRebalanceRun{}, rebalanceRunFailure(ctx, nil)
	}
	var collision bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM momentum.rebalance_runs WHERE run_id=$1 AND run_id<>$2)`, input.RunID, stored.RunID).Scan(&collision)
	if err != nil {
		return StoredRebalanceRun{}, rebalanceRunFailure(ctx, nil)
	}
	if collision {
		return StoredRebalanceRun{}, rebalanceRunFailure(ctx, ErrRebalanceRunCollision)
	}
	stored.OnboardingID = onboarding.String
	stored.ScheduledAt, stored.CreatedAt = stored.ScheduledAt.UTC(), created.Time.UTC()
	stored.Config, err = config.Parse(stored.ConfigPayload)
	canonical, storedHash, snapshotErr := stored.Config.Snapshot()
	actual := sha256.Sum256(stored.ConfigPayload)
	if err != nil || snapshotErr != nil || !created.Valid || !stamp.Valid || len(storedKey) != sha256.Size ||
		!bytes.Equal(actual[:], storedKey) || !bytes.Equal(canonical, stored.ConfigPayload) ||
		storedHash != hex.EncodeToString(storedKey) || !validRebalanceRunInput(stored.RebalanceRunInput) ||
		stored.Config.Values().StrategyID != input.StrategyID {
		return StoredRebalanceRun{}, rebalanceRunFailure(ctx, ErrRebalanceRunCorrupt)
	}
	if err := tx.Commit(); err != nil {
		return StoredRebalanceRun{}, rebalanceRunFailure(ctx, nil)
	}
	stored.ConfigHash = storedHash
	return stored, nil
}

// validRebalanceRunInput сохраняет opaque IDs и отвергает округление timestamp до микросекунд.
func validRebalanceRunInput(input RebalanceRunInput) bool {
	for _, id := range []string{input.RunID, input.AccountID, input.StrategyID} {
		if !validRebalanceRunID(id) {
			return false
		}
	}
	if input.OnboardingID != "" && !validRebalanceRunID(input.OnboardingID) {
		return false
	}
	year := input.ScheduledAt.UTC().Year()
	return year >= 1970 && year <= 294276 && input.ScheduledAt.Nanosecond()%1000 == 0
}

// validRebalanceRunID проверяет UTF-8 и POSIX ASCII whitespace без нормализации.
func validRebalanceRunID(id string) bool {
	return utf8.ValidString(id) && !strings.ContainsRune(id, 0) && strings.Trim(id, " \t\n\r\f\v") != ""
}

// rebalanceRunFailure сохраняет ожидаемую категорию и отмену без сырых SQL/данных.
func rebalanceRunFailure(ctx context.Context, kind error) error {
	return errors.Join(ErrRebalanceRun, kind, ctx.Err())
}
