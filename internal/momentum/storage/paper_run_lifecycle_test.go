package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// lifecyclePhase сверяет область, флаги, точные байты и фактическое SQL-время.
func lifecyclePhase(t *testing.T, f PaperRunLifecycle, err error, in paperwire.InputRecord, input, plan bool, c RunCompletion) {
	t.Helper()
	migrationResult(t, err, nil)
	h := in.Header()
	if f.RunID != h.RunID || f.Scope != ControlScope(h.Scope) || f.ConfigHash != h.ConfigHash ||
		f.InputFound != input || f.PlanFound != plan || f.Completed != (c.RunID != "") || f.Completion != c {
		t.Fatal("неверная идентичность, фаза или итог", h.RunID)
	}
	if input && (!bytes.Equal(in.Payload(), f.Input.Payload()) || f.Input.Hash() != in.Hash()) ||
		plan && (f.Plan.Hash() != h.PlanHash || f.Plan.Metadata().Revision != in.Values().Plan.Metadata().Revision) {
		t.Fatal("изменился вход или план")
	}
	if !input && !reflect.ValueOf(f.Input).IsZero() || !plan && !reflect.ValueOf(f.Plan).IsZero() {
		t.Fatal("лишние данные до сохранения")
	}
}

// lifecycleError требует нулевой результат и все безопасные категории ошибки.
func lifecycleError(t *testing.T, f PaperRunLifecycle, err error, kinds ...error) {
	t.Helper()
	if !reflect.ValueOf(f).IsZero() {
		t.Fatal("частичный результат")
	}
	for _, kind := range kinds {
		migrationResult(t, err, kind)
	}
}

// lifecycleSnapshot возвращает полные строки и количество по каждой таблице запуска.
func lifecycleSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var rows []string
	for _, table := range []string{"config_snapshots", "rebalance_runs", "paper_run_inputs", "plan_revisions", "run_completions"} {
		rows = append(rows, migrationValue(t, db, "SELECT count(*)||'/'||coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r))::text,'[]') FROM momentum."+table+" r"))
	}
	return strings.Join(rows, "\n")
}

// TestPaperRunLifecyclePhases проверяет каждую фазу через настоящий writer и отдельный pool.
func TestPaperRunLifecyclePhases(t *testing.T) {
	ctx, db, first := paperJournalFixture(t)
	pool := completionPool(t, db)
	read := func(in paperwire.InputRecord, input, plan bool, c RunCompletion) PaperRunLifecycle {
		got, err := ReadPaperRunLifecycle(ctx, pool, ControlScope(in.Header().Scope), in.Header().RunID)
		lifecyclePhase(t, got, err, in, input, plan, c)
		return got
	}
	in := first
	var firstCompletion RunCompletion
	for _, kind := range []string{"COMPLETED", "ABORTED"} {
		read(in, false, false, RunCompletion{})
		_, err := StorePaperInput(ctx, db, in)
		migrationResult(t, err, nil)
		read(in, true, false, RunCompletion{})
		completion, receipt := paperInputCompletion(t, db, in, kind)
		read(in, true, true, RunCompletion{})
		saved, err := CompleteRebalanceRun(ctx, db, completion)
		migrationResult(t, err, nil)
		got := read(in, true, true, saved)
		n, d, e := got.Input.Values().Cutoff.WalletUSDT.CanonicalRatio()
		huge, _, he := got.Input.Values().Before.WalletUSDT.CanonicalRatio()
		value, ok := new(big.Int).SetString(huge, 10)
		if e != nil || he != nil || n != "1" || d != "3" || !ok || value.BitLen() != 4096 ||
			got.Completion.CompletedAt.Equal(in.Header().ScheduledAt) || got.Completion.Reason != completion.Reason || len(got.Completion.Reason) <= 10000 {
			t.Fatal("нет точного большого receipt, суммы или времени БД")
		}
		if kind == "COMPLETED" {
			firstCompletion = saved
		}
		in = paperInputFixture(t, db, in.Header().Sequence+1, in, receipt, in.Header().Scope.AccountID, true)
	}
	skipped, err := CompleteRebalanceRun(ctx, db, RunCompletion{RunID: in.Header().RunID, Outcome: RunSkipped, Reason: "no rebalance"})
	migrationResult(t, err, nil)
	before := lifecycleSnapshot(t, db)
	read(in, false, false, skipped)
	read(first, true, true, firstCompletion)
	if lifecycleSnapshot(t, db) != before {
		t.Fatal("чтение изменило таблицы")
	}
}

// lifecycleMutationTx снимает ограничения только внутри откатываемой disposable-транзакции.
func lifecycleMutationTx(t *testing.T, ctx context.Context, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	migrationResult(t, err, nil)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(ctx, `SET LOCAL session_replication_role=replica; DO $$ DECLARE c record; BEGIN
 FOR c IN SELECT conrelid::regclass AS tbl,conname FROM pg_constraint WHERE contype='c' AND connamespace='momentum'::regnamespace
 LOOP EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I',c.tbl,c.conname); END LOOP; END $$`)
	migrationResult(t, err, nil)
	return tx
}

// TestPaperRunLifecycleCorruption проверяет поломки в выбранной фазе и лимиты SQL.
func TestPaperRunLifecycleCorruption(t *testing.T) {
	ctx, db, first := paperJournalFixture(t)
	scope, run := ControlScope(first.Header().Scope), first.Header().RunID
	check := func(name, query string, kinds ...error) {
		t.Run(name, func(t *testing.T) {
			tx := lifecycleMutationTx(t, ctx, db)
			executionRowsExec(t, tx, 1, query, run)
			got, err := readPaperRunLifecycle(ctx, tx, scope, run)
			lifecycleError(t, got, err, append(kinds, ErrPaperRunLifecycleCorrupt)...)
		})
	}
	const key = " WHERE run_id=$1"
	const conf = " WHERE config_hash=(SELECT config_hash FROM momentum.rebalance_runs WHERE run_id=$1)"
	check("missing-config", "DELETE FROM momentum.config_snapshots"+conf)
	check("damaged-config", "UPDATE momentum.config_snapshots SET payload=payload||payload"+conf)
	const replaceCfg = `WITH c AS (SELECT convert_to(replace(convert_from(payload,'UTF8'),'%s','%s'),'UTF8') p FROM momentum.config_snapshots` + conf + `),
 inserted AS (INSERT INTO momentum.config_snapshots(config_hash,payload) SELECT sha256(p),p FROM c RETURNING config_hash)
 UPDATE momentum.rebalance_runs SET config_hash=(SELECT config_hash FROM inserted)` + key
	check("wrong-strategy", fmt.Sprintf(replaceCfg, scope.StrategyID, "other"))
	v := first.Values().Run.Config.Values()
	v.Mode = "backtest"
	v.DataSource = config.DataSource{Kind: "frozen_dataset", Environment: "testnet", DatasetID: "frozen", ManifestSHA256: strings.Repeat("a", 64)}
	backtest, err := json.Marshal(v)
	migrationResult(t, err, nil)
	_, err = config.Parse(backtest)
	migrationResult(t, err, nil)
	original, _, _ := first.Values().Run.Config.Snapshot()
	check("wrong-mode", fmt.Sprintf(replaceCfg, original, backtest))
	check("failed-without-input", "INSERT INTO momentum.run_completions(run_id,outcome,reason) VALUES($1,'FAILED','failure')")
	_, err = StorePaperInput(ctx, db, first)
	migrationResult(t, err, nil)
	completion, receipt := paperInputCompletion(t, db, first, "COMPLETED")
	_, err = CompleteRebalanceRun(ctx, db, completion)
	migrationResult(t, err, nil)
	second := paperInputFixture(t, db, 2, first, receipt, scope.AccountID, true)
	_, err = StorePaperInput(ctx, db, second)
	migrationResult(t, err, nil)
	const journal = "UPDATE momentum.paper_run_inputs SET "
	const plans = "UPDATE momentum.plan_revisions SET "
	const outcomes = "UPDATE momentum.run_completions SET "
	check("misbound", journal+"account_id='paper/other'"+key)
	check("sequence", journal+"sequence=3"+key, ErrPaperInputCorrupt)
	check("input-hash", journal+"input_hash=sha256(input_hash)"+key, ErrPaperInputCorrupt)
	check("onboarding", "UPDATE momentum.rebalance_runs SET onboarding_id=NULL"+key, ErrPaperInputCorrupt)
	check("plan-without-input", "DELETE FROM momentum.paper_run_inputs"+key)
	check("reverse-kind", outcomes+"outcome='FAILED'"+key)
	check("plain", outcomes+"reason='plain'"+key)
	check("skipped-with-input", outcomes+"outcome='SKIPPED'"+key)
	check("null-hash", outcomes+"plan_hash=NULL"+key)
	check("null-revision", outcomes+"plan_revision=NULL"+key)
	check("infinite-time", outcomes+"completed_at='infinity'"+key)
	check("mismatched-link", outcomes+"plan_revision=2"+key)
	check("fraction", plans+"revision='1.5'"+key)
	check("nan", plans+"revision='NaN'"+key)
	check("infinity", plans+"revision='Infinity'"+key)
	check("overflow", plans+"revision='18446744073709551616'"+key)
	check("oversized-plan", plans+fmt.Sprintf("payload=repeat('a',%d)::bytea", paperwire.MaxPlanBytes+1)+key, paperwire.ErrPaperResourceLimit)
	oversized := outcomes + fmt.Sprintf("reason=repeat('a',%d)", paperwire.MaxCanonicalBytes+1) + key
	check("oversized-reason", oversized, paperwire.ErrPaperResourceLimit)
	for _, revision := range []uint64{1, 2} {
		t.Run(fmt.Sprintf("valid-other-plan-%d", revision), func(t *testing.T) {
			tx := lifecycleMutationTx(t, ctx, db)
			p := first.Values().Plan
			m := p.Metadata()
			m.FormulaID = "other"
			m.Revision = revision
			if revision == 2 {
				m.PreviousHash = p.Hash()
			}
			other := planRepositoryPlan(t, model.PlanInput{Metadata: m, Snapshot: p.Snapshot(), Targets: p.Targets()})
			raw, err := model.EncodeTradePlan(other)
			migrationResult(t, err, nil)
			if revision == 1 {
				executionRowsExec(t, tx, 1, plans+"plan_hash=decode($2,'hex'),payload=$3,payload_sha256=sha256($3)"+key, run, other.Hash(), raw)
			} else {
				executionRowsExec(t, tx, 1, `INSERT INTO momentum.plan_revisions(run_id,revision,previous_hash,plan_hash,payload,payload_sha256)
 VALUES($1,2,decode($2,'hex'),decode($3,'hex'),$4,sha256($4))`, run, p.Hash(), other.Hash(), raw)
			}
			got, err := readPaperRunLifecycle(ctx, tx, scope, run)
			lifecycleError(t, got, err, ErrPaperRunLifecycleCorrupt)
		})
	}
	tx := lifecycleMutationTx(t, ctx, db)
	executionRowsExec(t, tx, 1, plans+"revision=1.0"+key, run)
	executionRowsExec(t, tx, 1, journal+"sequence=1.0"+key, run)
	got, err := readPaperRunLifecycle(ctx, tx, scope, run)
	if err != nil || !got.Completed {
		t.Fatal("1.0", err)
	}
	migrationResult(t, tx.Rollback(), nil)
	t.Run("predecessor", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		migrationResult(t, err, nil)
		defer tx.Rollback()
		_, err = tx.Exec("SET LOCAL session_replication_role=replica")
		migrationResult(t, err, nil)
		executionRowsExec(t, tx, 1, oversized, run)
		migrationResult(t, tx.Commit(), nil)
		got, err := ReadPaperRunLifecycle(ctx, db, scope, second.Header().RunID)
		lifecycleError(t, got, err, ErrPaperRunLifecycleCorrupt, ErrPaperInputCorrupt, paperwire.ErrPaperResourceLimit)
		_, found, err := ReadPaperInput(ctx, db, second.Header().Scope, 2)
		if found {
			t.Fatal("возвращена строка с повреждённым predecessor")
		}
		migrationResult(t, err, ErrPaperInputCorrupt)
		migrationResult(t, err, paperwire.ErrPaperResourceLimit)
	})
}

// TestPaperRunLifecycleTransactions проверяет supplied TX, rollback и ошибки доступа.
func TestPaperRunLifecycleTransactions(t *testing.T) {
	ctx, db, in := paperJournalFixture(t)
	scope, run := ControlScope(in.Header().Scope), in.Header().RunID
	pool := completionPool(t, db)
	_, source, original := paperJournalFixture(t)
	c, _ := paperInputCompletion(t, source, original, "COMPLETED")
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	migrationResult(t, err, nil)
	defer tx.Rollback()
	paperInputSeed(t, tx, in)
	raw, err := model.EncodeTradePlan(in.Values().Plan)
	migrationResult(t, err, nil)
	executionRowsExec(t, tx, 1, `INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,payload,payload_sha256)
 VALUES($1,1,decode($2,'hex'),$3,sha256($3))`, run, c.PlanHash, raw)
	c.CompletedAt = in.Header().ScheduledAt.Add(time.Hour)
	executionRowsExec(t, tx, 1, `INSERT INTO momentum.run_completions(run_id,outcome,plan_hash,plan_revision,reason,completed_at)
 VALUES($1,'COMPLETED',decode($2,'hex'),1,$3,$4)`, run, c.PlanHash, c.Reason, c.CompletedAt)
	got, err := readPaperRunLifecycle(ctx, tx, scope, run)
	lifecyclePhase(t, got, err, in, true, true, c)
	got, err = ReadPaperRunLifecycle(ctx, pool, scope, run)
	lifecyclePhase(t, got, err, in, false, false, RunCompletion{})
	migrationResult(t, tx.Rollback(), nil)
	got, err = ReadPaperRunLifecycle(ctx, pool, scope, run)
	lifecyclePhase(t, got, err, in, false, false, RunCompletion{})
	got, err = readPaperRunLifecycle(ctx, tx, scope, run)
	lifecycleError(t, got, err, ErrPaperRunLifecycleUnavailable)
	got, err = readPaperRunLifecycle(ctx, nil, scope, run)
	lifecycleError(t, got, err, ErrPaperRunLifecycleArgument)
	for _, arg := range []struct {
		ctx   context.Context
		db    *sql.DB
		scope ControlScope
		run   string
	}{
		{nil, db, scope, run},
		{ctx, nil, scope, run},
		{ctx, db, ControlScope{}, run},
		{ctx, db, scope, ""},
		{ctx, db, scope, "\x00bad"},
	} {
		got, err = ReadPaperRunLifecycle(arg.ctx, arg.db, arg.scope, arg.run)
		lifecycleError(t, got, err, ErrPaperRunLifecycleArgument)
	}
	for _, unknown := range []struct {
		scope ControlScope
		run   string
	}{
		{scope, "missing"},
		{ControlScope{Environment: "paper", AccountID: "paper/other", StrategyID: scope.StrategyID}, run},
	} {
		got, err = ReadPaperRunLifecycle(ctx, pool, unknown.scope, unknown.run)
		lifecycleError(t, got, err, ErrPaperRunLifecycleAbsent)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	expired, stop := context.WithDeadline(ctx, time.Unix(1, 0))
	defer stop()
	for _, done := range []context.Context{cancelled, expired} {
		got, err = ReadPaperRunLifecycle(done, pool, scope, run)
		lifecycleError(t, got, err, ErrPaperRunLifecycleUnavailable, done.Err())
	}
	migrationResult(t, pool.Close(), nil)
	got, err = ReadPaperRunLifecycle(ctx, pool, scope, run)
	lifecycleError(t, got, err, ErrPaperRunLifecycleUnavailable)
	_, err = db.ExecContext(ctx, `ALTER TABLE momentum.run_completions RENAME TO unavailable`)
	migrationResult(t, err, nil)
	got, err = ReadPaperRunLifecycle(ctx, db, scope, run)
	lifecycleError(t, got, err, ErrPaperRunLifecycleUnavailable)
}
