package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// paperInputFixture создаёт полный codec-valid вход с явной profile identity.
func paperInputFixture(t *testing.T, db *sql.DB, sequence uint64, previous paperwire.InputRecord, receipt, account string, bound bool) paperwire.InputRecord {
	t.Helper()
	at := time.Unix(1700000000+int64(sequence)*3600, 0).UTC()
	one, _ := model.ParseAmount("1")
	rules := map[string]planner.MarketDeltaRules{}
	schedules := map[string]paperwire.FundingSchedule{}
	prices, positions := map[string]model.Decimal{}, map[string]model.Decimal{}
	held := map[string]model.LongPosition{}
	targets := []model.Target{}
	cfg := runRepositoryConfig(t, false)
	for _, symbol := range cfg.Values().Symbols {
		rules[symbol] = planner.MarketDeltaRules{QtyStep: one, MinQty: one, MaxMarketQty: one, MinIncreaseNotional: model.Zero()}
		schedules[symbol] = paperwire.FundingSchedule{Anchor: at, Interval: 8 * time.Hour}
		prices[symbol], positions[symbol] = one, model.Zero()
		held[symbol] = model.LongPosition{Quantity: model.Zero(), CostBasisUSDT: model.Zero()}
		targets = append(targets, model.Target{Symbol: symbol, Quantity: model.Zero()})
	}
	profile := paperwire.Profile{
		Parameters:   backtest.SimulationParameters{FeeRate: model.Zero(), SlippageRate: model.Zero(), MarkMaxAge: time.Minute, Rules: rules},
		SourceMaxAge: time.Minute, DataDeadline: time.Minute, Poll: time.Second, RPCTimeout: time.Second,
		FundingSchedules: schedules, ExecutionReference: "closed-hourly-trade-close/v1",
	}
	_, profileHash, err := paperwire.EncodeProfile(profile)
	migrationResult(t, err, nil)
	if bound {
		raw, _, _ := cfg.Snapshot()
		raw = bytes.Replace(raw, []byte(cfg.Values().ModelID), []byte(profileHash), 1)
		cfg, err = config.Parse(raw)
		migrationResult(t, err, nil)
	}
	run := RebalanceRunInput{RunID: fmt.Sprintf("paper-run-%d", sequence), AccountID: account, StrategyID: cfg.Values().StrategyID, ScheduledAt: at, Config: cfg}
	if sequence == 1 {
		run.OnboardingID = "paper-start"
	}
	if db != nil {
		_, err = CreateOrReadRebalanceRun(context.Background(), db, run)
		migrationResult(t, err, nil)
	}
	_, hash, _ := cfg.Snapshot()
	snap, err := model.NewPortfolioSnapshot(model.SnapshotInput{Environment: "paper", AccountID: run.AccountID, ConfigVersion: cfg.Values().ProfileVersion, CapturedAt: at, Equity: one, Prices: prices, Positions: positions})
	migrationResult(t, err, nil)
	plan, err := model.NewTradePlan(model.PlanInput{Snapshot: snap, Targets: targets, Metadata: model.PlanMetadata{StrategyID: run.StrategyID, RunID: run.RunID, ProfileID: cfg.Values().Profile, ModelID: cfg.Values().ModelID, FormulaID: "explicit", ConfigHash: hash, ScheduledAt: at, EquityCutoffAt: at, Revision: 1}})
	migrationResult(t, err, nil)
	three, _ := model.ParseAmount("3")
	ratio, _ := one.Quo(three)
	two, _ := model.ParseAmount("2")
	amount := one
	for i := 0; i < 4095; i++ {
		amount, err = amount.Mul(two)
		migrationResult(t, err, nil)
	}
	state := paperwire.PortfolioState{AsOf: at, FundingThrough: at, WalletUSDT: ratio, EquityUSDT: one, Positions: held}
	d := paperwire.CycleDocument{
		Run:      paperwire.RunBinding{RunID: run.RunID, AccountID: run.AccountID, StrategyID: run.StrategyID, OnboardingID: run.OnboardingID, ScheduledAt: at, Config: cfg},
		Sequence: sequence, Purpose: paperwire.Purpose{Kind: "rebalance"}, Profile: profile,
		Observation: paperwire.Observation{CapturedAt: at, SourceClock: at}, Before: state, Cutoff: state, ExpectedAfter: state, Plan: plan,
	}
	d.Before.WalletUSDT = amount
	if sequence > 1 {
		d.PreviousInputHash, d.PreviousResolutionHash = previous.Hash(), receipt
	}
	input, err := paperwire.EncodeInputRecord(d)
	migrationResult(t, err, nil)
	return input
}

// paperInputCompletion сохраняет план и формирует точный receipt указанного исхода.
func paperInputCompletion(t *testing.T, db *sql.DB, input paperwire.InputRecord, kind string) (RunCompletion, string) {
	t.Helper()
	d := paperwire.ResolutionDocument{InputHash: input.Hash(), RunID: input.Header().RunID, PlanHash: input.Header().PlanHash, Kind: kind, ResolvedAt: input.Header().ScheduledAt, ActualAfter: input.Values().ExpectedAfter}
	if kind == "ABORTED" {
		d.CommandID, d.ControlRevision = "halt", 1
	}
	values := input.Values()
	for i := 0; i < 12; i++ {
		d.Fills = append(d.Fills, model.ConfirmedFill{
			ExecutionID: strings.Repeat("учёт", 30) + fmt.Sprint(i), Symbol: values.Run.Config.Values().Symbols[0], Side: model.FillBuy,
			ExecutedAt: d.ResolvedAt, Quantity: values.Before.WalletUSDT, ExecutionPrice: values.Before.WalletUSDT, FeeUSDT: model.Zero(),
		})
	}
	receipt, err := paperwire.EncodeResolutionRecord(d, input)
	migrationResult(t, err, nil)
	if len(receipt.Payload()) <= 10000 {
		t.Fatal("receipt fixture is too small")
	}
	_, err = StorePlanRevision(context.Background(), db, input.Values().Plan)
	migrationResult(t, err, nil)
	outcome := RunCompleted
	if kind == "ABORTED" {
		outcome = RunFailed
	}
	return RunCompletion{RunID: input.Header().RunID, Outcome: outcome, PlanRevision: 1, PlanHash: input.Header().PlanHash, Reason: string(receipt.Payload())}, receipt.Hash()
}

// paperInputSeed вставляет canonical record напрямую в опубликованную M15.
func paperInputSeed(t *testing.T, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, in paperwire.InputRecord) {
	t.Helper()
	h := in.Header()
	_, err := q.ExecContext(context.Background(), `INSERT INTO momentum.paper_run_inputs(run_id,environment,account_id,strategy_id,sequence,previous_input_hash,previous_resolution_hash,input_hash,payload,created_at) VALUES($1,$2,$3,$4,$5,decode(NULLIF($6,''),'hex'),decode(NULLIF($7,''),'hex'),decode($8,'hex'),$9,$10)`, h.RunID, h.Scope.Environment, h.Scope.AccountID, h.Scope.StrategyID, fmt.Sprint(h.Sequence)+".0", h.PreviousInputHash, h.PreviousResolutionHash, in.Hash(), in.Payload(), h.ScheduledAt)
	migrationResult(t, err, nil)
}

// completionPool открывает второе независимое подключение к базе теста.
func completionPool(t *testing.T, db *sql.DB) *sql.DB {
	t.Helper()
	u, err := url.Parse(os.Getenv("MOMENTUM_TEST_POSTGRES_DSN"))
	migrationResult(t, err, nil)
	u.Path = "/" + migrationValue(t, db, "SELECT current_database()")
	pool, err := OpenMomentumPostgres(context.Background(), u.String())
	migrationResult(t, err, nil)
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// requireCompletion проверяет точные байты, hash и отсутствие частичного результата.
func requireCompletion(t *testing.T, got paperwire.ResolutionRecord, found bool, err error, payload, hash string, want error) {
	t.Helper()
	if !errors.Is(err, want) || found != (hash != "") || got.Hash() != hash || string(got.Payload()) != payload {
		t.Fatalf("неожиданный результат чтения: found=%t hash=%s err=%v", found, got.Hash(), err)
	}
}

// TestPaperCompletionRead проверяет обе формы receipt после переподключения.
func TestPaperCompletionRead(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	var previous paperwire.InputRecord
	var hash string
	for i, kind := range []string{"COMPLETED", "ABORTED"} {
		input := paperInputFixture(t, db, uint64(i+1), previous, hash, "paper/journal", true)
		paperInputSeed(t, db, input)
		got, found, err := ReadPaperCompletion(ctx, db, input)
		requireCompletion(t, got, found, err, "", "", nil)
		completion, receiptHash := paperInputCompletion(t, db, input, kind)
		_, err = CompleteRebalanceRun(ctx, db, completion)
		migrationResult(t, err, nil)
		got, found, err = ReadPaperCompletion(ctx, completionPool(t, db), input)
		requireCompletion(t, got, found, err, completion.Reason, receiptHash, nil)
		other := input.Values()
		other.Before.WalletUSDT = other.Cutoff.WalletUSDT
		alternate, err := paperwire.EncodeInputRecord(other)
		migrationResult(t, err, nil)
		got, found, err = ReadPaperCompletion(ctx, db, alternate)
		requireCompletion(t, got, found, err, "", "", ErrPaperCompletionCorrupt)
		previous, hash = input, receiptHash
	}
}

// TestPaperCompletionReadCorrupt проверяет повреждения в изолированных откатываемых транзакциях.
func TestPaperCompletionReadCorrupt(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	first := paperInputFixture(t, db, 1, paperwire.InputRecord{}, "", "paper/journal", true)
	paperInputSeed(t, db, first)
	completed, hash := paperInputCompletion(t, db, first, "COMPLETED")
	_, err := CompleteRebalanceRun(ctx, db, completed)
	migrationResult(t, err, nil)
	child := paperInputFixture(t, db, 2, first, hash, "paper/journal", true)
	paperInputSeed(t, db, child)
	aborted, _ := paperInputCompletion(t, db, child, "ABORTED")
	_, err = CompleteRebalanceRun(ctx, db, aborted)
	migrationResult(t, err, nil)
	if first.Header().ConfigHash == child.Header().ConfigHash {
		t.Fatal("для подмены нужны два различных canonical config")
	}
	for name, mutation := range map[string]string{
		"skipped":                "UPDATE momentum.run_completions SET outcome='SKIPPED'",
		"completed-as-failed":    "UPDATE momentum.run_completions SET outcome='FAILED'",
		"aborted-as-completed":   "UPDATE momentum.run_completions SET outcome='COMPLETED'",
		"plain":                  "UPDATE momentum.run_completions SET reason='plain'",
		"malformed":              "UPDATE momentum.run_completions SET reason='{'",
		"noncanonical":           "UPDATE momentum.run_completions SET reason=reason||' '",
		"other-receipt":          "UPDATE momentum.run_completions SET reason=(SELECT reason FROM momentum.run_completions WHERE run_id='paper-run-1')",
		"wrong-plan":             "UPDATE momentum.run_completions SET plan_hash=sha256(plan_hash)",
		"no-plan":                "UPDATE momentum.run_completions SET plan_hash=NULL,plan_revision=NULL",
		"null-hash":              "UPDATE momentum.run_completions SET plan_hash=NULL",
		"null-revision":          "UPDATE momentum.run_completions SET plan_revision=NULL",
		"wrong-revision":         "UPDATE momentum.run_completions SET plan_revision=2",
		"fraction":               "UPDATE momentum.run_completions SET plan_revision=1.1",
		"nan":                    "UPDATE momentum.run_completions SET plan_revision='NaN'",
		"infinite-revision":      "UPDATE momentum.run_completions SET plan_revision='Infinity'",
		"overflow":               "UPDATE momentum.run_completions SET plan_revision=18446744073709551616",
		"infinite-completion":    "UPDATE momentum.run_completions SET completed_at='infinity'",
		"missing-input":          "DELETE FROM momentum.paper_run_inputs",
		"input-payload":          "UPDATE momentum.paper_run_inputs SET payload=payload||payload,input_hash=sha256(payload||payload)",
		"input-hash":             "UPDATE momentum.paper_run_inputs SET input_hash=sha256(input_hash)",
		"environment":            "UPDATE momentum.paper_run_inputs SET environment='testnet'",
		"account":                "UPDATE momentum.paper_run_inputs SET account_id='paper/other'",
		"strategy":               "UPDATE momentum.paper_run_inputs SET strategy_id='other'",
		"sequence":               "UPDATE momentum.paper_run_inputs SET sequence=3 WHERE sequence=2",
		"previous-input":         "UPDATE momentum.paper_run_inputs SET previous_input_hash=NULL",
		"previous-receipt":       "UPDATE momentum.paper_run_inputs SET previous_resolution_hash=NULL",
		"missing-run":            "DELETE FROM momentum.rebalance_runs",
		"run-onboarding":         "UPDATE momentum.rebalance_runs SET onboarding_id='other-start' WHERE run_id='paper-run-2'",
		"run-account":            "UPDATE momentum.rebalance_runs SET account_id='paper/other'",
		"run-strategy":           "UPDATE momentum.rebalance_runs SET strategy_id='other'",
		"run-time":               "UPDATE momentum.rebalance_runs SET scheduled_at=scheduled_at+interval '1 second'",
		"infinite-run":           "UPDATE momentum.rebalance_runs SET scheduled_at='-infinity' WHERE run_id='paper-run-2'",
		"missing-config":         "DELETE FROM momentum.config_snapshots",
		"config-bytes":           "UPDATE momentum.config_snapshots SET payload=payload||payload",
		"other-canonical-config": "UPDATE momentum.rebalance_runs SET config_hash=(SELECT config_hash FROM momentum.rebalance_runs WHERE run_id='paper-run-1') WHERE run_id='paper-run-2'",
	} {
		t.Run(name, func(t *testing.T) {
			tx, e := db.BeginTx(ctx, nil)
			migrationResult(t, e, nil)
			defer tx.Rollback()
			_, e = tx.ExecContext(ctx, `SET LOCAL session_replication_role=replica; DO $$ DECLARE c record; BEGIN FOR c IN SELECT conrelid::regclass AS tbl,conname FROM pg_constraint WHERE contype='c' AND connamespace='momentum'::regnamespace LOOP EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I',c.tbl,c.conname); END LOOP; END $$`)
			migrationResult(t, e, nil)
			_, e = tx.ExecContext(ctx, mutation)
			migrationResult(t, e, nil)
			input := child
			if name == "completed-as-failed" {
				input = first
			}
			got, found, e := readPaperCompletion(ctx, tx, input)
			requireCompletion(t, got, found, e, "", "", ErrPaperCompletionCorrupt)
		})
	}
}

// TestPaperCompletionArguments проверяет границу API до SQL и безопасные ошибки доступа.
func TestPaperCompletionArguments(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	valid := paperInputFixture(t, nil, 1, paperwire.InputRecord{}, "", "paper/a", true)
	for _, tc := range []struct {
		ctx context.Context
		db  *sql.DB
		in  paperwire.InputRecord
	}{
		{nil, db, valid}, {ctx, nil, valid}, {ctx, db, paperwire.InputRecord{}},
		{ctx, db, paperInputFixture(t, nil, 1, paperwire.InputRecord{}, "", "paper/a ", true)},
		{ctx, db, paperInputFixture(t, nil, 1, paperwire.InputRecord{}, "", "paper/a", false)},
	} {
		got, found, err := ReadPaperCompletion(tc.ctx, tc.db, tc.in)
		requireCompletion(t, got, found, err, "", "", ErrPaperCompletionArgument)
	}
	got, found, err := ReadPaperCompletion(ctx, db, valid)
	requireCompletion(t, got, found, err, "", "", ErrPaperCompletionUnavailable)
	if err.Error() != ErrPaperCompletionUnavailable.Error() {
		t.Fatal("раскрыты SQL diagnostics")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	got, found, err = ReadPaperCompletion(cancelled, db, valid)
	requireCompletion(t, got, found, err, "", "", ErrPaperCompletionUnavailable)
	migrationResult(t, err, context.Canceled)
}

// TestPaperCompletionTransaction проверяет видимость provisional записей и read-only TX.
func TestPaperCompletionTransaction(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	input := paperInputFixture(t, db, 1, paperwire.InputRecord{}, "", "paper/journal", true)
	completion, hash := paperInputCompletion(t, db, input, "COMPLETED")
	pool := completionPool(t, db)
	tx, err := db.BeginTx(ctx, nil)
	migrationResult(t, err, nil)
	defer tx.Rollback()
	paperInputSeed(t, tx, input)
	_, err = tx.ExecContext(ctx, `INSERT INTO momentum.run_completions(run_id,outcome,plan_revision,plan_hash,reason) VALUES($1,$2,1.00,decode($3,'hex'),$4)`, completion.RunID, completion.Outcome, completion.PlanHash, completion.Reason)
	migrationResult(t, err, nil)
	got, found, err := readPaperCompletion(ctx, tx, input)
	requireCompletion(t, got, found, err, completion.Reason, hash, nil)
	got, found, err = ReadPaperCompletion(ctx, pool, input)
	requireCompletion(t, got, found, err, "", "", nil)
	migrationResult(t, tx.Commit(), nil)
	ro, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	migrationResult(t, err, nil)
	defer ro.Rollback()
	got, found, err = readPaperCompletion(ctx, ro, input)
	requireCompletion(t, got, found, err, completion.Reason, hash, nil)
}
