package storage

import (
	"context"
	"database/sql"
	"errors"

	"bybit_bot_liquidity/internal/momentum/config"
)

// ReadUnfinishedPlannedRun возвращает самый ранний run области account+strategy,
// у которого сохранён план, но нет итога; found=false — таких run нет. Такой run
// нужно довести до итога раньше любого нового цикла, иначе его неизменяемый план
// останется без разрешения. Run читается через CreateOrReadRebalanceRun с его
// сохранённым config, поэтому identity и config проходят ту же проверку.
func ReadUnfinishedPlannedRun(ctx context.Context, db *sql.DB, accountID, strategyID string) (StoredRebalanceRun, bool, error) {
	if ctx == nil || db == nil || !validRebalanceRunID(accountID) || !validRebalanceRunID(strategyID) {
		return StoredRebalanceRun{}, false, errors.Join(ErrRebalanceRun, ErrRebalanceRunArgument)
	}
	var in RebalanceRunInput
	var payload []byte
	err := db.QueryRowContext(ctx, `SELECT r.run_id,r.scheduled_at,coalesce(r.onboarding_id,''),c.payload
 FROM momentum.rebalance_runs r LEFT JOIN momentum.config_snapshots c ON c.config_hash=r.config_hash
 WHERE r.account_id=$1 AND r.strategy_id=$2
 AND EXISTS (SELECT 1 FROM momentum.plan_revisions p WHERE p.run_id=r.run_id)
 AND NOT EXISTS (SELECT 1 FROM momentum.run_completions d WHERE d.run_id=r.run_id)
 ORDER BY r.scheduled_at, r.run_id LIMIT 1`, accountID, strategyID).Scan(&in.RunID, &in.ScheduledAt, &in.OnboardingID, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredRebalanceRun{}, false, nil
	}
	if err != nil {
		return StoredRebalanceRun{}, false, rebalanceRunFailure(ctx, nil)
	}
	in.AccountID, in.StrategyID, in.ScheduledAt = accountID, strategyID, in.ScheduledAt.UTC()
	if in.Config, err = config.Parse(payload); err != nil {
		return StoredRebalanceRun{}, false, rebalanceRunFailure(ctx, ErrRebalanceRunCorrupt)
	}
	stored, err := CreateOrReadRebalanceRun(ctx, db, in)
	if err != nil {
		return StoredRebalanceRun{}, false, err
	}
	return stored, true, nil
}
