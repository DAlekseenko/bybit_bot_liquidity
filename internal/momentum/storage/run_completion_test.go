package storage

import (
	"context"
	"sync"
	"testing"

	"bybit_bot_liquidity/internal/momentum/model"
)

// TestRunResumeAndCompletion проверяет resume по последней revision, итог run и запрет новых планов после него.
func TestRunResumeAndCompletion(t *testing.T) {
	ctx := context.Background()
	db, in := planRepositoryFixture(t)
	_, found, err := ReadLatestPlanRevision(ctx, db, "run")
	planRepositoryCheck(t, err == nil && !found, "latest of empty run")
	plans := []model.TradePlan{planRepositoryPlan(t, in)}
	in.Metadata.Revision, in.Metadata.PreviousHash = 2, plans[0].Hash()
	plans = append(plans, planRepositoryPlan(t, in))
	for _, plan := range plans {
		_, err := StorePlanRevision(ctx, db, plan)
		migrationResult(t, err, nil)
	}
	latest, found, err := ReadLatestPlanRevision(ctx, db, "run")
	planRepositoryCheck(t, err == nil && found && latest.Plan.Hash() == plans[1].Hash(), "latest revision not restored")
	_, found, err = ReadRunCompletion(ctx, db, "run")
	planRepositoryCheck(t, err == nil && !found, "unfinished run reported complete")

	stale := RunCompletion{RunID: "run", Outcome: RunCompleted, PlanRevision: 1, PlanHash: plans[0].Hash(), Reason: "done"}
	_, err = CompleteRebalanceRun(ctx, db, stale)
	migrationResult(t, err, ErrRunCompletionConflict)
	for _, bad := range []RunCompletion{
		{RunID: "run", Outcome: RunCompleted, Reason: "done"},
		{RunID: "run", Outcome: "GUESS", PlanRevision: 2, PlanHash: plans[1].Hash(), Reason: "done"},
		{RunID: "run", Outcome: RunFailed, PlanRevision: 2, PlanHash: "zz", Reason: "done"},
		{RunID: "run", Outcome: RunFailed, PlanRevision: 2, PlanHash: plans[1].Hash(), Reason: " "},
		{RunID: " ", Outcome: RunSkipped, Reason: "week"},
	} {
		_, err = CompleteRebalanceRun(ctx, db, bad)
		migrationResult(t, err, ErrRunCompletionArgument)
	}
	_, err = CompleteRebalanceRun(ctx, db, RunCompletion{RunID: "missing", Outcome: RunSkipped, Reason: "week"})
	migrationResult(t, err, ErrRunCompletionAbsent)

	done := RunCompletion{RunID: "run", Outcome: RunCompleted, PlanRevision: 2, PlanHash: plans[1].Hash(), Reason: "done"}
	first, err := CompleteRebalanceRun(ctx, db, done)
	migrationResult(t, err, nil)
	again, err := CompleteRebalanceRun(ctx, db, done)
	planRepositoryCheck(t, err == nil && again == first && !first.CompletedAt.IsZero(), "idempotent completion changed")
	failed := done
	failed.Outcome = RunFailed
	_, err = CompleteRebalanceRun(ctx, db, failed)
	migrationResult(t, err, ErrRunCompletionConflict)

	_, err = StorePlanRevision(ctx, db, plans[1])
	migrationResult(t, err, nil)
	in.Metadata.Revision, in.Metadata.PreviousHash = 3, plans[1].Hash()
	got, err := StorePlanRevision(ctx, db, planRepositoryPlan(t, in))
	requirePlanRevisionFailure(t, got, err, ErrRunCompleted)
	runSchemaSQLState(t, db, "55000", "UPDATE momentum.run_completions SET reason='x'")
	runSchemaSQLState(t, db, "55000", "DELETE FROM momentum.run_completions")
	migrationValue(t, db, "SELECT count(*)||'/'||max(revision) FROM momentum.plan_revisions", "2/2")
}

// TestRunCompletionRace проверяет, что гонка новой revision и завершения даёт согласованный итог.
func TestRunCompletionRace(t *testing.T) {
	ctx := context.Background()
	db, in := planRepositoryFixture(t)
	first := planRepositoryPlan(t, in)
	_, err := StorePlanRevision(ctx, db, first)
	migrationResult(t, err, nil)
	in.Metadata.Revision, in.Metadata.PreviousHash = 2, first.Hash()
	second := planRepositoryPlan(t, in)
	var wg sync.WaitGroup
	var planErr, doneErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, planErr = StorePlanRevision(ctx, db, second)
	}()
	go func() {
		defer wg.Done()
		_, doneErr = CompleteRebalanceRun(ctx, db, RunCompletion{RunID: "run", Outcome: RunFailed, PlanRevision: 1,
			PlanHash: first.Hash(), Reason: "stop"})
	}()
	wg.Wait()
	if (planErr == nil) == (doneErr == nil) {
		t.Fatalf("exactly one must win: plan=%v completion=%v", planErr, doneErr)
	}
	latest, _, err := ReadLatestPlanRevision(ctx, db, "run")
	migrationResult(t, err, nil)
	stored, found, err := ReadRunCompletion(ctx, db, "run")
	migrationResult(t, err, nil)
	if found && stored.PlanRevision != latest.Plan.Metadata().Revision {
		t.Fatalf("completion references revision %d, latest %d", stored.PlanRevision, latest.Plan.Metadata().Revision)
	}
}
