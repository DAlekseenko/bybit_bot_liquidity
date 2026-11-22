package storage

import (
	"context"
	"encoding/hex"
	"testing"
	"time"
)

// TestReadUnfinishedPlannedRun проверяет выбор самого раннего run с планом и без итога.
func TestReadUnfinishedPlannedRun(t *testing.T) {
	ctx := context.Background()
	db, in := planRepositoryFixture(t)
	account, strategy := in.Snapshot.AccountID(), in.Metadata.StrategyID
	_, found, err := ReadUnfinishedPlannedRun(ctx, db, account, strategy)
	planRepositoryCheck(t, err == nil && !found, "run without plan must not be reported")

	key, _ := hex.DecodeString(in.Metadata.ConfigHash)
	later := in.Metadata.ScheduledAt.Add(7 * 24 * time.Hour)
	_, err = db.Exec("INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,config_hash) VALUES('later',$1,$2,$3,$4)",
		account, strategy, later, key)
	migrationResult(t, err, nil)
	plan := planRepositoryPlan(t, in)
	_, err = StorePlanRevision(ctx, db, plan)
	migrationResult(t, err, nil)

	run, found, err := ReadUnfinishedPlannedRun(ctx, db, account, strategy)
	planRepositoryCheck(t, err == nil && found && run.RunID == in.Metadata.RunID &&
		run.ScheduledAt.Equal(in.Metadata.ScheduledAt) && run.ConfigHash == in.Metadata.ConfigHash, "planned unfinished run not restored")
	_, found, err = ReadUnfinishedPlannedRun(ctx, db, account, "other")
	planRepositoryCheck(t, err == nil && !found, "other strategy must not see the run")

	_, err = CompleteRebalanceRun(ctx, db, RunCompletion{RunID: run.RunID, Outcome: RunCompleted, PlanRevision: 1,
		PlanHash: plan.Hash(), Reason: "done"})
	migrationResult(t, err, nil)
	_, found, err = ReadUnfinishedPlannedRun(ctx, db, account, strategy)
	planRepositoryCheck(t, err == nil && !found, "completed run must not be reported")
	_, _, err = ReadUnfinishedPlannedRun(ctx, db, " ", strategy)
	migrationResult(t, err, ErrRebalanceRunArgument)
}
