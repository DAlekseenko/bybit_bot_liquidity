package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"

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

	done := RunCompletion{RunID: "run", Outcome: RunCompleted, PlanRevision: 2, PlanHash: plans[1].Hash(), Reason: strings.Repeat("r", 10241), CompletedAt: time.Unix(1, 0)}
	first, err := CompleteRebalanceRun(ctx, db, done)
	migrationResult(t, err, nil)
	done.CompletedAt = time.Unix(2, 0)
	again, err := CompleteRebalanceRun(ctx, db, done)
	planRepositoryCheck(t, err == nil && again == first && first.CompletedAt.After(done.CompletedAt) && first.CompletedAt.Location() == time.UTC, "idempotent completion changed")
	u, _ := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
	u.Path = "/" + migrationValue(t, db, "SELECT current_database()")
	reopened, err := OpenMomentumPostgres(ctx, u.String())
	migrationResult(t, err, nil)
	defer reopened.Close()
	restored, found, err := ReadRunCompletion(ctx, reopened, "run")
	planRepositoryCheck(t, err == nil && found && restored == first, "completion not restored on new pool")
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

// TestRunWriterIsolation проверяет оба порядка RC и отказ RR до блокировки с устаревшим snapshot.
func TestRunWriterIsolation(t *testing.T) {
	for _, completionFirst := range []bool{false, true} {
		for _, isolation := range []sql.IsolationLevel{sql.LevelReadCommitted, sql.LevelRepeatableRead} {
			db, in := planRepositoryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			first := planRepositoryPlan(t, in)
			_, err := StorePlanRevision(ctx, db, first)
			migrationResult(t, err, nil)
			in.Metadata.Revision, in.Metadata.PreviousHash = 2, first.Hash()
			second := planRepositoryPlan(t, in)
			payload, _ := model.EncodeTradePlan(second)
			planSQL := "INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,payload,payload_sha256,previous_hash) VALUES('run',2,decode($1,'hex'),$2,sha256($2::bytea),decode($3,'hex'))"
			doneSQL := "INSERT INTO momentum.run_completions(run_id,outcome,plan_revision,plan_hash,reason) VALUES('run','FAILED',1,decode($1,'hex'),'stop')"
			writer, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: isolation})
			migrationResult(t, err, nil)
			defer writer.Rollback()
			var writerPID, snapshotCount int
			migrationResult(t, writer.QueryRowContext(ctx, "SELECT pg_backend_pid(),count(*) FROM momentum.plan_revisions").Scan(&writerPID, &snapshotCount), nil)
			planRepositoryCheck(t, snapshotCount == 1, "writer snapshot not established")
			holder, err := db.BeginTx(ctx, nil)
			migrationResult(t, err, nil)
			defer holder.Rollback()
			var holderPID int
			migrationResult(t, holder.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&holderPID), nil)
			holderSQL, waitingSQL := planSQL, doneSQL
			holderArgs, waitingArgs := []any{second.Hash(), payload, first.Hash()}, []any{first.Hash()}
			if completionFirst {
				holderSQL, waitingSQL, holderArgs, waitingArgs = doneSQL, planSQL, waitingArgs, holderArgs
			}
			_, err = holder.ExecContext(ctx, holderSQL, holderArgs...)
			migrationResult(t, err, nil)
			result := make(chan error, 1)
			go func() { _, err := writer.ExecContext(ctx, waitingSQL, waitingArgs...); result <- err }()
			if isolation == sql.LevelReadCommitted {
				for {
					var blocked bool
					err = db.QueryRowContext(ctx, "SELECT $1=ANY(pg_blocking_pids($2))", holderPID, writerPID).Scan(&blocked)
					migrationResult(t, err, nil)
					if blocked {
						break
					}
				}
				migrationResult(t, holder.Commit(), nil)
			}
			err = <-result
			var failure *pq.Error
			want := "55000"
			if isolation == sql.LevelReadCommitted && !completionFirst {
				want = "23514"
			}
			planRepositoryCheck(t, errors.As(err, &failure) && string(failure.Code) == want, "unsafe writer accepted or blocked")
			if isolation == sql.LevelRepeatableRead {
				migrationResult(t, holder.Commit(), nil)
			}
			migrationResult(t, writer.Rollback(), nil)
			latest, found, err := ReadLatestPlanRevision(ctx, db, "run")
			planRepositoryCheck(t, err == nil && found && (latest.Plan.Hash() == first.Hash()) == completionFirst, "latest snapshot changed")
			_, found, err = ReadRunCompletion(ctx, db, "run")
			planRepositoryCheck(t, err == nil && found == completionFirst, "completion state changed")
		}
	}
}

// TestRunCompletionDefaultRR проверяет публичный отказ без записи при session default RR.
func TestRunCompletionDefaultRR(t *testing.T) {
	db, _ := planRepositoryFixture(t)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err := db.Exec("SET default_transaction_isolation='repeatable read'")
	migrationResult(t, err, nil)
	got, err := CompleteRebalanceRun(context.Background(), db, RunCompletion{RunID: "run", Outcome: RunSkipped, Reason: "week"})
	planRepositoryCheck(t, errors.Is(err, ErrRunCompletion) && got == (RunCompletion{}), "default RR completion succeeded")
	migrationValue(t, db, "SELECT count(*) FROM momentum.run_completions", "0")
}
