package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
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

// TestReadUnfinishedPlannedRunOffline проверяет отказ по аргументам и ошибку базы без PostgreSQL.
func TestReadUnfinishedPlannedRunOffline(t *testing.T) {
	db, err := sql.Open("postgres", "host=127.0.0.1 port=1 sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var nilCtx context.Context
	for _, tc := range []struct {
		ctx               context.Context
		db                *sql.DB
		account, strategy string
	}{{nilCtx, db, "acc", "s"}, {context.Background(), nil, "acc", "s"}, {context.Background(), db, " ", "s"}, {context.Background(), db, "acc", "a\x00"}} {
		if _, _, err := ReadUnfinishedPlannedRun(tc.ctx, tc.db, tc.account, tc.strategy); !errors.Is(err, ErrRebalanceRunArgument) {
			t.Fatalf("want argument error, got %v", err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, found, err := ReadUnfinishedPlannedRun(canceled, db, "acc", "s"); found || !errors.Is(err, ErrRebalanceRun) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx: found=%v err=%v", found, err)
	}
}
