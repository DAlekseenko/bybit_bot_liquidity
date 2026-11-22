package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
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
	laterPlanInput := in
	laterPlanInput.Metadata.RunID, laterPlanInput.Metadata.ScheduledAt = "later", later
	laterPlan := planRepositoryPlan(t, laterPlanInput)
	_, err = StorePlanRevision(ctx, db, laterPlan)
	migrationResult(t, err, nil)

	run, found, err := ReadUnfinishedPlannedRun(ctx, db, account, strategy)
	planRepositoryCheck(t, err == nil && found && run.RunID == in.Metadata.RunID &&
		run.ScheduledAt.Equal(in.Metadata.ScheduledAt) && run.ConfigHash == in.Metadata.ConfigHash, "planned unfinished run not restored")
	_, found, err = ReadUnfinishedPlannedRun(ctx, db, account, "other")
	planRepositoryCheck(t, err == nil && !found, "other strategy must not see the run")

	_, err = CompleteRebalanceRun(ctx, db, RunCompletion{RunID: run.RunID, Outcome: RunCompleted, PlanRevision: 1,
		PlanHash: plan.Hash(), Reason: "done"})
	migrationResult(t, err, nil)
	run, found, err = ReadUnfinishedPlannedRun(ctx, db, account, strategy)
	planRepositoryCheck(t, err == nil && found && run.RunID == "later", "completed run must be skipped in favor of next planned run")
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

// addUnfinishedPlan добавляет согласованные immutable run и план для SQL fixture.
func addUnfinishedPlan(t *testing.T, db *sql.DB, base model.PlanInput, cfg RebalanceRunInput, id string, account string, at time.Time) model.TradePlan {
	t.Helper()
	cfg.RunID, cfg.AccountID, cfg.ScheduledAt, cfg.OnboardingID = id, account, at, "onboard-"+id
	_, err := CreateOrReadRebalanceRun(context.Background(), db, cfg)
	migrationResult(t, err, nil)
	base.Metadata.RunID, base.Metadata.ScheduledAt = id, at
	base.Metadata.ConfigHash = planRepositoryConfigHash(t, cfg)
	if account != base.Snapshot.AccountID() {
		one, _ := model.ParseAmount("1")
		base.Snapshot, err = model.NewPortfolioSnapshot(model.SnapshotInput{Environment: "paper", AccountID: account, ConfigVersion: "opaque/version\x00", CapturedAt: time.Unix(1700000000, 123456789), Equity: one, Prices: map[string]model.Decimal{"BTCUSDT": one}, Positions: map[string]model.Decimal{}})
		migrationResult(t, err, nil)
	}
	plan := planRepositoryPlan(t, base)
	_, err = StorePlanRevision(context.Background(), db, plan)
	migrationResult(t, err, nil)
	return plan
}

// planRepositoryConfigHash возвращает стабильный хэш настроек, уже сохранённых в run fixture.
func planRepositoryConfigHash(t *testing.T, in RebalanceRunInput) string {
	_, hash, err := in.Config.Snapshot()
	migrationResult(t, err, nil)
	return hash
}

// TestReadUnfinishedPlannedRunPostgres проверяет SQL-порядок, scope, bytes и fail-closed восстановление.
func TestReadUnfinishedPlannedRunPostgres(t *testing.T) {
	db, base := planRepositoryFixture(t)
	cfg := RebalanceRunInput{AccountID: base.Snapshot.AccountID(), StrategyID: base.Metadata.StrategyID, Config: runRepositoryConfig(t, false)}
	tieAt := base.Metadata.ScheduledAt.Add(-time.Hour)
	z := addUnfinishedPlan(t, db, base, cfg, "tie-z", cfg.AccountID, tieAt)
	a := addUnfinishedPlan(t, db, base, cfg, "tie-a", cfg.AccountID, tieAt)
	addUnfinishedPlan(t, db, base, cfg, "foreign-account", "another-account", tieAt.Add(-time.Hour))
	counts := unfinishedRunCounts(t)
	before := migrationValue(t, db, counts)
	got, found, err := ReadUnfinishedPlannedRun(context.Background(), db, cfg.AccountID, cfg.StrategyID)
	planRepositoryCheck(t, err == nil && found && got.RunID == "tie-a" && got.ScheduledAt.Equal(tieAt), "earliest tie must use run id")
	planRepositoryCheck(t, migrationValue(t, db, counts) == before, "selection changed row counts or saved config byte totals")
	u, err := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
	migrationResult(t, err, nil)
	u.Path = "/" + migrationValue(t, db, "SELECT current_database()")
	reopened, err := OpenMomentumPostgres(context.Background(), u.String())
	migrationResult(t, err, nil)
	defer reopened.Close()
	before = migrationValue(t, db, counts)
	got, found, err = ReadUnfinishedPlannedRun(context.Background(), reopened, cfg.AccountID, cfg.StrategyID)
	planRepositoryCheck(t, err == nil && found && got.RunID == "tie-a", "reopen did not restore exact run")
	payload, hash, err := cfg.Config.Snapshot()
	migrationResult(t, err, nil)
	planRepositoryCheck(t, got.ConfigHash == hash && got.ConfigHash == base.Metadata.ConfigHash && string(got.ConfigPayload) == string(payload), "saved config bytes/hash changed")
	planRepositoryCheck(t, migrationValue(t, db, counts) == before, "reopen selection changed existing records")
	_, err = CompleteRebalanceRun(context.Background(), db, RunCompletion{RunID: got.RunID, Outcome: RunCompleted, PlanRevision: 1, PlanHash: a.Hash(), Reason: "done"})
	migrationResult(t, err, nil)
	got, found, err = ReadUnfinishedPlannedRun(context.Background(), db, cfg.AccountID, cfg.StrategyID)
	planRepositoryCheck(t, err == nil && found && got.RunID == "tie-z", "next tied planned run was not selected")
	if got.RunID == z.Metadata().RunID { // Проверяем точную identity ранее созданного плана.
		_, err = CompleteRebalanceRun(context.Background(), db, RunCompletion{RunID: got.RunID, Outcome: RunCompleted, PlanRevision: 1, PlanHash: z.Hash(), Reason: "done"})
		migrationResult(t, err, nil)
	}

	payload = []byte("{")
	_, err = db.Exec("INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES(sha256($1::bytea),$1)", payload)
	migrationResult(t, err, nil)
	key := "corrupt-config"
	_, err = db.Exec(`INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,config_hash)
 VALUES($1,$2,$3,$4,sha256($5::bytea))`, key, cfg.AccountID, cfg.StrategyID, tieAt.Add(-2*time.Hour), payload)
	migrationResult(t, err, nil)
	planBytes, _ := model.EncodeTradePlan(a)
	planHash := a.Hash()
	_, err = db.Exec(`INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,payload,payload_sha256)
 VALUES($1,1,decode($2,'hex'),$3,sha256($3::bytea))`, key, planHash, planBytes)
	migrationResult(t, err, nil)
	before = migrationValue(t, db, counts)
	if _, found, err = ReadUnfinishedPlannedRun(context.Background(), db, cfg.AccountID, cfg.StrategyID); found || !errors.Is(err, ErrRebalanceRunCorrupt) {
		t.Fatalf("corrupt config: found=%v err=%v", found, err)
	}
	planRepositoryCheck(t, migrationValue(t, db, counts) == before, "corrupt selection changed records")
	_, err = db.Exec("ALTER TABLE momentum.rebalance_runs DROP CONSTRAINT rebalance_runs_config_hash_fkey")
	migrationResult(t, err, nil)
	missing := "missing-config"
	_, err = db.Exec(`INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,config_hash)
 VALUES($1,$2,$3,$4,sha256('absent'::bytea))`, missing, cfg.AccountID, cfg.StrategyID, tieAt.Add(-3*time.Hour))
	migrationResult(t, err, nil)
	_, err = db.Exec(`INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,payload,payload_sha256)
 VALUES($1,1,decode($2,'hex'),$3,sha256($3::bytea))`, missing, planHash, planBytes)
	migrationResult(t, err, nil)
	before = migrationValue(t, db, counts)
	if _, found, err = ReadUnfinishedPlannedRun(context.Background(), db, cfg.AccountID, cfg.StrategyID); found || !errors.Is(err, ErrRebalanceRunCorrupt) {
		t.Fatalf("missing config: found=%v err=%v", found, err)
	}
	planRepositoryCheck(t, migrationValue(t, db, counts) == before, "missing selection changed records")
}

// unfinishedRunCounts сериализует количество строк и длину сохранённых payload для чтения.
func unfinishedRunCounts(t *testing.T) string {
	t.Helper()
	return `SELECT (SELECT count(*) FROM momentum.rebalance_runs)::text||':'||
 (SELECT count(*) FROM momentum.config_snapshots)::text||':'||
 (SELECT coalesce(sum(octet_length(payload)),0) FROM momentum.config_snapshots)::text||':'||
 (SELECT count(*) FROM momentum.plan_revisions)::text||':'||
 (SELECT count(*) FROM momentum.run_completions)::text`
}
