package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
)

// planRepositoryCheck проверяет точный инвариант с исходной диагностикой.
func planRepositoryCheck(t *testing.T, ok bool, message string) {
	if !ok {
		t.Fatal(message)
	}
}

// planRepositoryInput создаёт синтетический план с exact 1/3 и opaque полями без mapping.
func planRepositoryInput(t *testing.T) (model.PlanInput, []byte) {
	raw, err := os.ReadFile("../../../configs/momentum.paper.yaml")
	migrationResult(t, err, nil)
	cfg, err := config.Parse(raw)
	migrationResult(t, err, nil)
	payload, hash, err := cfg.Snapshot()
	migrationResult(t, err, nil)
	one, err := model.ParseAmount("1")
	migrationResult(t, err, nil)
	three, err := model.ParseAmount("3")
	migrationResult(t, err, nil)
	third, err := one.Quo(three)
	migrationResult(t, err, nil)
	snapshot, err := model.NewPortfolioSnapshot(model.SnapshotInput{Environment: "paper", AccountID: "account", ConfigVersion: "opaque/version\x00", CapturedAt: time.Unix(1700000000, 123456789), Equity: third, Prices: map[string]model.Decimal{"BTCUSDT": one}, Positions: map[string]model.Decimal{}})
	migrationResult(t, err, nil)
	return model.PlanInput{Snapshot: snapshot, Metadata: model.PlanMetadata{RunID: "run", StrategyID: cfg.Values().StrategyID, ProfileID: cfg.Values().Profile, ModelID: cfg.Values().ModelID, FormulaID: "opaque/formula\x00", ConfigHash: hash, ScheduledAt: time.Unix(1700000000, 123456000), EquityCutoffAt: time.Unix(1700000000, 123456789), Revision: 1}, Targets: []model.Target{{Symbol: "BTCUSDT", Quantity: third}}}, payload
}

// planRepositoryPlan использует только существующий immutable конструктор модели.
func planRepositoryPlan(t *testing.T, in model.PlanInput) model.TradePlan {
	plan, err := model.NewTradePlan(in)
	migrationResult(t, err, nil)
	return plan
}

// planRepositoryFixture создаёт identity fixture на полном действующем migration catalog.
func planRepositoryFixture(t *testing.T) (*sql.DB, model.PlanInput) {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	in, payload := planRepositoryInput(t)
	key, _ := hex.DecodeString(in.Metadata.ConfigHash)
	_, err := db.Exec("INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES($1,$2)", key, payload)
	migrationResult(t, err, nil)
	_, err = db.Exec("INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,config_hash) VALUES($1,$2,$3,$4,$5)", in.Metadata.RunID, in.Snapshot.AccountID(), in.Metadata.StrategyID, in.Metadata.ScheduledAt, key)
	migrationResult(t, err, nil)
	return db, in
}

// requirePlanRevisionFailure проверяет нулевой результат и safe typed error.
func requirePlanRevisionFailure(t *testing.T, got StoredPlanRevision, err, want error) {
	t.Helper()
	planRepositoryCheck(t, reflect.ValueOf(got).IsZero() && errors.Is(err, ErrPlanRevision) && errors.Is(err, want), "unexpected partial/error")
	text := err.Error()
	planRepositoryCheck(t, !strings.Contains(text, "pq:") && !strings.Contains(text, "postgres://") && !strings.Contains(text, "secret-marker"), "unsafe diagnostic")
}

// TestPlanRevisionDecode проверяет SQL/model binding и аргументы без PostgreSQL.
func TestPlanRevisionDecode(t *testing.T) {
	in, configPayload := planRepositoryInput(t)
	plan := planRepositoryPlan(t, in)
	payload, err := model.EncodeTradePlan(plan)
	migrationResult(t, err, nil)
	key, _ := hex.DecodeString(plan.Hash())
	digest := sha256.Sum256(payload)
	row := storedPlanRevisionRow{planHash: key, payload: payload, payloadHash: digest[:], created: sql.NullTime{Time: time.Now(), Valid: true}}
	got, err := decodeStoredPlanRevision(row, "run", 1)
	planRepositoryCheck(t, err == nil && got.Plan.Hash() == plan.Hash(), "valid exact row rejected")
	runKey, _ := hex.DecodeString(in.Metadata.ConfigHash)
	runRow := planRevisionRunRow{account: in.Snapshot.AccountID(), strategy: in.Metadata.StrategyID, scheduled: sql.NullTime{Time: in.Metadata.ScheduledAt, Valid: true}, created: row.created, configCreated: row.created, key: runKey, payload: configPayload}
	planRepositoryCheck(t, validatePlanRevisionRun(runRow, plan) == nil, "valid run/config rejected")
	runRow.payload = []byte("secret-marker")
	planRepositoryCheck(t, errors.Is(validatePlanRevisionRun(runRow, plan), ErrPlanRevisionCorrupt), "corrupt run/config accepted")
	runRow.payload, runRow.account = configPayload, "different"
	planRepositoryCheck(t, errors.Is(validatePlanRevisionRun(runRow, plan), ErrPlanRevisionMismatch), "mismatched run/config accepted")
	for _, mutate := range []func(*storedPlanRevisionRow){
		func(r *storedPlanRevisionRow) { r.payload = []byte("secret-marker") },
		func(r *storedPlanRevisionRow) { r.planHash = make([]byte, 32) },
		func(r *storedPlanRevisionRow) { r.payloadHash = make([]byte, 32) },
		func(r *storedPlanRevisionRow) { r.previous = []byte{} },
		func(r *storedPlanRevisionRow) { r.created.Valid = false },
	} {
		altered := row
		mutate(&altered)
		_, err := decodeStoredPlanRevision(altered, "run", 1)
		planRepositoryCheck(t, errors.Is(err, ErrPlanRevisionCorrupt), "corrupt accepted")
	}
	for revision, id := range map[uint64]string{1: "different", 2: "run", ^uint64(0): "run"} {
		_, err := decodeStoredPlanRevision(row, id, revision)
		planRepositoryCheck(t, errors.Is(err, ErrPlanRevisionCorrupt), "row key not bound")
	}
	db, err := sql.Open("postgres", "")
	migrationResult(t, err, nil)
	migrationResult(t, db.Close(), nil)
	for id, revision := range map[string]uint64{" ": 1, "x\x00": 1, string([]byte{255}): 1, "run": 0} {
		got, err := ReadPlanRevision(context.Background(), db, id, revision)
		requirePlanRevisionFailure(t, got, err, ErrPlanRevisionArgument)
	}
	got, err = ReadPlanRevision(nil, db, "run", 1) //nolint:staticcheck // Проверяем явный отказ при nil context.
	requirePlanRevisionFailure(t, got, err, ErrPlanRevisionArgument)
	got, err = ReadPlanRevision(context.Background(), nil, "run", 1)
	requirePlanRevisionFailure(t, got, err, ErrPlanRevisionArgument)
	got, err = StorePlanRevision(context.Background(), db, model.TradePlan{})
	requirePlanRevisionFailure(t, got, err, ErrPlanRevisionArgument)
	got, err = StorePlanRevision(context.Background(), db, plan)
	requirePlanRevisionFailure(t, got, err, ErrPlanRevision)
	got, err = ReadPlanRevision(context.Background(), db, "run", 1)
	requirePlanRevisionFailure(t, got, err, ErrPlanRevision)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for want, ctx := range map[error]context.Context{context.Canceled: canceled, context.DeadlineExceeded: expired} {
		got, err = ReadPlanRevision(ctx, db, "run", 1)
		requirePlanRevisionFailure(t, got, err, want)
		got, err = StorePlanRevision(ctx, db, plan)
		requirePlanRevisionFailure(t, got, err, want)
	}
}

// TestPlanRevisionRepository проверяет exact chain, immutable repeat и другой payload того же key.
func TestPlanRevisionRepository(t *testing.T) {
	db, in := planRepositoryFixture(t)
	plan := planRepositoryPlan(t, in)
	missing := in
	missing.Metadata.Revision, missing.Metadata.PreviousHash = 2, plan.Hash()
	got, err := StorePlanRevision(context.Background(), db, planRepositoryPlan(t, missing))
	requirePlanRevisionFailure(t, got, err, ErrPlanRevisionAbsent)
	first, err := StorePlanRevision(context.Background(), db, plan)
	migrationResult(t, err, nil)
	xmin := migrationValue(t, db, "SELECT xmin::text FROM momentum.plan_revisions")
	var created time.Time
	migrationResult(t, db.QueryRow("SELECT created_at FROM momentum.plan_revisions").Scan(&created), nil)
	planRepositoryCheck(t, first.CreatedAt.Equal(created.UTC()) && first.CreatedAt.Location() == time.UTC, "changed persisted UTC time")
	again, err := StorePlanRevision(context.Background(), db, plan)
	migrationResult(t, err, nil)
	planRepositoryCheck(t, reflect.DeepEqual(first, again), "idempotent repeat changed row")
	migrationValue(t, db, "SELECT xmin::text FROM momentum.plan_revisions", xmin)
	changed := in
	changed.Targets = []model.Target{{Symbol: "BTCUSDT", Quantity: model.Zero()}}
	got, err = StorePlanRevision(context.Background(), db, planRepositoryPlan(t, changed))
	requirePlanRevisionFailure(t, got, err, ErrPlanRevisionConflict)
	for revision := uint64(2); revision <= 3; revision++ {
		in.Metadata.Revision = revision
		in.Metadata.PreviousHash = plan.Hash()
		plan = planRepositoryPlan(t, in)
		_, err = StorePlanRevision(context.Background(), db, plan)
		migrationResult(t, err, nil)
	}
	stored, err := ReadPlanRevision(context.Background(), db, "run", 3)
	migrationResult(t, err, nil)
	before, _ := model.EncodeTradePlan(plan)
	after, _ := model.EncodeTradePlan(stored.Plan)
	planRepositoryCheck(t, bytes.Equal(before, after) && stored.Plan.Snapshot().CapturedAt().Nanosecond() == 123456789 && stored.Plan.Metadata().EquityCutoffAt.Nanosecond() == 123456789, "lossy stored plan")
	n, d, _ := stored.Plan.Targets()[0].Quantity.CanonicalRatio()
	planRepositoryCheck(t, n == "1" && d == "3", "rounded target")
	in.Metadata.Revision = 4
	in.Metadata.PreviousHash = strings.Repeat("b", 64)
	got, err = StorePlanRevision(context.Background(), db, planRepositoryPlan(t, in))
	requirePlanRevisionFailure(t, got, err, ErrPlanRevisionMismatch)
	got, err = ReadPlanRevision(context.Background(), db, "run", ^uint64(0))
	requirePlanRevisionFailure(t, got, err, ErrPlanRevisionAbsent)
	migrationValue(t, db, "SELECT count(*) FROM momentum.plan_revisions", "3")
}

// TestPlanRevisionIdentity проверяет несовместимость плана с immutable run/config.
func TestPlanRevisionIdentity(t *testing.T) {
	db, base := planRepositoryFixture(t)
	for name, mutate := range map[string]func(*model.PlanInput){
		"config":    func(in *model.PlanInput) { in.Metadata.ConfigHash = strings.Repeat("a", 64) },
		"strategy":  func(in *model.PlanInput) { in.Metadata.StrategyID = "different" },
		"profile":   func(in *model.PlanInput) { in.Metadata.ProfileID = "different" },
		"model":     func(in *model.PlanInput) { in.Metadata.ModelID = "different" },
		"scheduled": func(in *model.PlanInput) { in.Metadata.ScheduledAt = in.Metadata.ScheduledAt.Add(time.Nanosecond) },
		"account": func(in *model.PlanInput) {
			s := in.Snapshot
			in.Snapshot, _ = model.NewPortfolioSnapshot(model.SnapshotInput{Environment: s.Environment(), AccountID: "different", ConfigVersion: s.ConfigVersion(), CapturedAt: s.CapturedAt(), Equity: s.Equity(), Prices: s.Prices(), Positions: s.Positions()})
		},
		"mode": func(in *model.PlanInput) {
			s := in.Snapshot
			in.Snapshot, _ = model.NewPortfolioSnapshot(model.SnapshotInput{Environment: "backtest", AccountID: s.AccountID(), ConfigVersion: s.ConfigVersion(), CapturedAt: s.CapturedAt(), Equity: s.Equity(), Prices: s.Prices(), Positions: s.Positions()})
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := base
			mutate(&in)
			got, err := StorePlanRevision(context.Background(), db, planRepositoryPlan(t, in))
			requirePlanRevisionFailure(t, got, err, ErrPlanRevisionMismatch)
		})
	}
	base.Metadata.RunID = "missing"
	got, err := StorePlanRevision(context.Background(), db, planRepositoryPlan(t, base))
	requirePlanRevisionFailure(t, got, err, ErrPlanRevisionAbsent)
	migrationValue(t, db, "SELECT count(*) FROM momentum.plan_revisions", "0")
	_, err = db.Exec("CREATE TRIGGER reject_test_insert BEFORE INSERT ON momentum.plan_revisions FOR EACH ROW EXECUTE FUNCTION momentum.reject_plan_revision_mutation()")
	migrationResult(t, err, nil)
	base.Metadata.RunID = "run"
	got, err = StorePlanRevision(context.Background(), db, planRepositoryPlan(t, base))
	requirePlanRevisionFailure(t, got, err, ErrPlanRevision)
	migrationValue(t, db, "SELECT count(*) FROM momentum.plan_revisions", "0")
}

// TestPlanRevisionCorrupt проверяет schema-valid invalid bytes и несовместимый сохранённый план.
func TestPlanRevisionCorrupt(t *testing.T) {
	for _, kind := range []string{"payload", "model-hash", "config", "original-config"} {
		db, in := planRepositoryFixture(t)
		plan := planRepositoryPlan(t, in)
		original := plan
		if kind == "original-config" {
			_, err := db.Exec("INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES(sha256($1::bytea),$1);", []byte("secret-marker"))
			migrationResult(t, err, nil)
			in.Metadata.RunID = "corrupt-config"
			digest := sha256.Sum256([]byte("secret-marker"))
			in.Metadata.ConfigHash = hex.EncodeToString(digest[:])
			_, err = db.Exec("INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,config_hash,onboarding_id) VALUES($1,$2,$3,$4,$5,'other')", in.Metadata.RunID, in.Snapshot.AccountID(), in.Metadata.StrategyID, in.Metadata.ScheduledAt, digest[:])
			migrationResult(t, err, nil)
			plan, original = planRepositoryPlan(t, in), planRepositoryPlan(t, in)
		}
		if kind == "config" {
			in.Metadata.ConfigHash = strings.Repeat("b", 64)
			plan = planRepositoryPlan(t, in)
		}
		payload, _ := model.EncodeTradePlan(plan)
		key, _ := hex.DecodeString(plan.Hash())
		if kind == "payload" {
			payload = []byte("secret-marker")
		}
		if kind == "model-hash" {
			key = make([]byte, 32)
		}
		_, err := db.Exec("INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,payload,payload_sha256) VALUES($1,1,$2,$3,sha256($3::bytea))", plan.Metadata().RunID, key, payload)
		migrationResult(t, err, nil)
		got, err := ReadPlanRevision(context.Background(), db, plan.Metadata().RunID, 1)
		requirePlanRevisionFailure(t, got, err, ErrPlanRevisionCorrupt)
		got, err = StorePlanRevision(context.Background(), db, original)
		requirePlanRevisionFailure(t, got, err, ErrPlanRevisionCorrupt)
	}
}

// TestPlanRevisionConcurrent проверяет duplicate на двух pools с наблюдаемым unique lock.
func TestPlanRevisionConcurrent(t *testing.T) {
	db, in := planRepositoryFixture(t)
	plan := planRepositoryPlan(t, in)
	payload, _ := model.EncodeTradePlan(plan)
	key, _ := hex.DecodeString(plan.Hash())
	holder, err := db.BeginTx(context.Background(), nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.Exec("INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,payload,payload_sha256) VALUES($1,1,$2,$3,sha256($3::bytea))", "run", key, payload)
	migrationResult(t, err, nil)
	u, _ := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
	u.Path = "/" + migrationValue(t, db, "SELECT current_database()")
	second, err := OpenMomentumPostgres(context.Background(), u.String())
	migrationResult(t, err, nil)
	defer second.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		stored StoredPlanRevision
		err    error
	}
	results := make(chan result, 1)
	go func() { stored, err := StorePlanRevision(ctx, second, plan); results <- result{stored, err} }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		blocked := migrationValue(t, db, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE a.datname=current_database() AND cardinality(pg_blocking_pids(a.pid))>0)")
		if blocked == "true" {
			break
		}
		planRepositoryCheck(t, !time.Now().After(deadline), "duplicate did not reach PostgreSQL lock")
		time.Sleep(10 * time.Millisecond)
	}
	hidden, err := ReadPlanRevision(context.Background(), db, "run", 1)
	requirePlanRevisionFailure(t, hidden, err, ErrPlanRevisionAbsent)
	migrationResult(t, holder.Commit(), nil)
	got := <-results
	migrationResult(t, got.err, nil)
	read, err := ReadPlanRevision(context.Background(), second, "run", 1)
	migrationResult(t, err, nil)
	planRepositoryCheck(t, got.stored.Plan.Hash() == plan.Hash() && reflect.DeepEqual(got.stored, read), "concurrent duplicate changed revision")
	migrationValue(t, db, "SELECT count(*) FROM momentum.plan_revisions", "1")
}
