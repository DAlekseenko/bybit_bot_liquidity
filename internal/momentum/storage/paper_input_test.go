package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// paperJournalFixture готовит пустой журнал и первый зарегистрированный run.
func paperJournalFixture(t *testing.T) (context.Context, *sql.DB, paperwire.InputRecord) {
	t.Helper()
	db := openTestPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	migrationResult(t, MigrateMomentumPostgres(ctx, db), nil)
	return ctx, db, paperInputFixture(t, db, 1, paperwire.InputRecord{}, "", "paper/journal", true)
}

// TestPaperInputLocks проверяет оба порядка SHARE/SRX, отмену и provisional visibility.
func TestPaperInputLocks(t *testing.T) {
	for _, appendFirst := range []bool{false, true} {
		ctx, db, input := paperJournalFixture(t)
		operator, err := db.BeginTx(ctx, nil)
		migrationResult(t, err, nil)
		defer operator.Rollback()
		done := make(chan error, 1)
		if appendFirst {
			_, err = storePaperInput(ctx, db, input, paperInputStoreHooks{beforeCommit: func(tx *sql.Tx) error {
				own, found, e := readPaperInput(ctx, tx, input.Header().Scope, 1)
				if e != nil || !found || own.Input.Hash() != input.Hash() {
					t.Fatal("same TX did not see provisional input", e)
				}
				_, found, e = ReadPaperInput(ctx, completionPool(t, db), input.Header().Scope, 1)
				if e != nil || found {
					t.Fatal("other snapshot saw provisional input", e)
				}
				go func() { done <- lockPaperControls(ctx, operator, true) }()
				waitPaperBlock(t, ctx, db, "momentum.control_revisions", "ShareRowExclusiveLock")
				return nil
			}})
			migrationResult(t, err, nil)
			migrationResult(t, <-done, nil)
		} else {
			migrationResult(t, lockPaperControls(ctx, operator, true), nil)
			waiting, stop := context.WithCancel(ctx)
			go func() {
				rejected, e := StorePaperInput(waiting, db, input)
				if rejected.Input.Hash() != "" || !rejected.CreatedAt.IsZero() {
					t.Error("отмена вернула сохранённый вход")
				}
				done <- e
			}()
			waitPaperBlock(t, ctx, db, "momentum.control_revisions", "ShareLock")
			stop()
			e := <-done
			migrationResult(t, e, context.Canceled)
			migrationResult(t, e, ErrPaperInputUnavailable)
			migrationValue(t, db, "SELECT count(*) FROM momentum.paper_run_inputs", "0")
		}
		migrationResult(t, operator.Rollback(), nil)
	}
}

// TestPaperInputUnknownCommit проверяет rollback и потерю подтверждения без false saved.
func TestPaperInputUnknownCommit(t *testing.T) {
	for committed, finish := range map[bool]func(*sql.Tx) error{
		false: (*sql.Tx).Rollback,
		true:  (*sql.Tx).Commit,
	} {
		ctx, db, input := paperJournalFixture(t)
		saved, err := storePaperInput(ctx, db, input, paperInputStoreHooks{beforeCommit: finish})
		requireInputReject(t, saved, err, ErrPaperInputUnavailable)
		pool := completionPool(t, db)
		got, found, err := ReadPaperInput(ctx, pool, input.Header().Scope, 1)
		if err != nil || found != committed {
			t.Fatal("rollback/commit visibility", err)
		}
		retry, err := StorePaperInput(ctx, pool, input)
		migrationResult(t, err, nil)
		if !bytes.Equal(retry.Input.Payload(), input.Payload()) || (committed && !retry.CreatedAt.Equal(got.CreatedAt)) {
			t.Fatal("unknown commit retry")
		}
	}
}

// paperInputCandidate пересобирает valid plan/config после изменения входного DTO.
func paperInputCandidate(t *testing.T, base paperwire.InputRecord, mutate func(*paperwire.CycleDocument)) paperwire.InputRecord {
	t.Helper()
	d := base.Values()
	mutate(&d)
	raw, _, _ := d.Run.Config.Snapshot()
	_, profileHash, err := paperwire.EncodeProfile(d.Profile)
	migrationResult(t, err, nil)
	d.Run.Config, err = config.Parse(bytes.ReplaceAll(raw, []byte(d.Run.Config.Values().ModelID), []byte(profileHash)))
	migrationResult(t, err, nil)
	v := d.Run.Config.Values()
	_, hash, _ := d.Run.Config.Snapshot()
	d.Cutoff.AsOf, d.Cutoff.FundingThrough = d.Run.ScheduledAt, d.Run.ScheduledAt
	snapshot, err := model.NewPortfolioSnapshot(model.SnapshotInput{Environment: "paper", AccountID: d.Run.AccountID, ConfigVersion: v.ProfileVersion, CapturedAt: d.Run.ScheduledAt, Equity: d.Cutoff.EquityUSDT, Prices: d.Plan.Snapshot().Prices(), Positions: d.Plan.Snapshot().Positions()})
	migrationResult(t, err, nil)
	metadata := d.Plan.Metadata()
	metadata.RunID, metadata.StrategyID, metadata.ModelID, metadata.ConfigHash = d.Run.RunID, d.Run.StrategyID, v.ModelID, hash
	metadata.ScheduledAt, metadata.EquityCutoffAt = d.Run.ScheduledAt, d.Run.ScheduledAt
	d.Plan, err = model.NewTradePlan(model.PlanInput{Snapshot: snapshot, Targets: d.Plan.Targets(), Metadata: metadata})
	migrationResult(t, err, nil)
	candidate, err := paperwire.EncodeInputRecord(d)
	migrationResult(t, err, nil)
	return candidate
}

// TestPaperInputCandidates проверяет codec-valid мутации без замены сохранённых bytes.
func TestPaperInputCandidates(t *testing.T) {
	ctx, db, first := paperJournalFixture(t)
	saved, err := StorePaperInput(ctx, db, first)
	migrationResult(t, err, nil)
	completion, receipt := paperInputCompletion(t, db, first, "COMPLETED")
	_, err = CompleteRebalanceRun(ctx, db, completion)
	migrationResult(t, err, nil)
	child := paperInputFixture(t, db, 2, first, receipt, "paper/journal", true)
	for name, tc := range map[string]struct {
		base   paperwire.InputRecord
		mutate func(*paperwire.CycleDocument)
	}{
		"payload": {first, func(d *paperwire.CycleDocument) { d.Before.WalletUSDT = model.Zero() }},
		"model":   {first, func(d *paperwire.CycleDocument) { d.Profile.Poll += time.Second }},
		"run":     {first, func(d *paperwire.CycleDocument) { d.Run.RunID = "missing-run" }},
		"scope":   {first, func(d *paperwire.CycleDocument) { d.Run.AccountID = "paper/other" }},
		"config": {first, func(d *paperwire.CycleDocument) {
			raw, _, _ := d.Run.Config.Snapshot()
			d.Run.Config, _ = config.Parse(bytes.ReplaceAll(raw, []byte("next_monday"), []byte("now")))
		}},
		"time":         {first, func(d *paperwire.CycleDocument) { d.Run.ScheduledAt = d.Run.ScheduledAt.Add(time.Second) }},
		"occupied-run": {child, func(d *paperwire.CycleDocument) { d.Run.RunID = first.Header().RunID }},
		"gap":          {child, func(d *paperwire.CycleDocument) { d.Sequence = 3 }},
		"fork":         {child, func(d *paperwire.CycleDocument) { d.PreviousInputHash = strings.Repeat("b", 64) }},
		"receipt-hash": {child, func(d *paperwire.CycleDocument) { d.PreviousResolutionHash = strings.Repeat("b", 64) }},
		"cross-scope":  {child, func(d *paperwire.CycleDocument) { d.Run.AccountID = "paper/other" }},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := paperInputCandidate(t, tc.base, tc.mutate)
			got, e := StorePaperInput(ctx, db, candidate)
			requireInputReject(t, got, e, ErrPaperInputConflict)
			requireOriginalInput(t, db, saved, "1")
		})
	}
}

// requireInputReject проверяет отсутствие частичного результата и утечки SQL diagnostics.
func requireInputReject(t *testing.T, got StoredPaperInput, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || err.Error() != want.Error() || got.Input.Hash() != "" || !got.CreatedAt.IsZero() {
		t.Fatalf("неожиданный результат отклонения: %v", err)
	}
}

// requireOriginalInput проверяет неизменность bytes/hash/времени и числа строк.
func requireOriginalInput(t *testing.T, db *sql.DB, saved StoredPaperInput, count string) {
	t.Helper()
	h := saved.Input.Header()
	got, found, err := ReadPaperInput(context.Background(), db, h.Scope, h.Sequence)
	if err != nil || !found || !got.CreatedAt.Equal(saved.CreatedAt) || got.Input.Hash() != saved.Input.Hash() || !bytes.Equal(got.Input.Payload(), saved.Input.Payload()) {
		t.Fatal("изменился исходный вход", err)
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.paper_run_inputs", count)
}

// TestPaperInputJournal проверяет конкурентный INSERT, оба исхода и исторические повторы.
func TestPaperInputJournal(t *testing.T) {
	ctx, db, first := paperJournalFixture(t)
	pool := completionPool(t, db)
	var duplicate StoredPaperInput
	done := make(chan error, 1)
	saved, err := storePaperInput(ctx, db, first, paperInputStoreHooks{beforeCommit: func(tx *sql.Tx) error {
		go func() {
			var e error
			duplicate, e = StorePaperInput(ctx, pool, first)
			done <- e
		}()
		for blocked := false; !blocked; {
			err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks l WHERE l.locktype='transactionid'
 AND NOT l.granted AND pg_backend_pid()=ANY(pg_blocking_pids(l.pid)))`).Scan(&blocked)
			migrationResult(t, err, nil)
		}
		return nil
	}})
	migrationResult(t, err, nil)
	migrationResult(t, <-done, nil)
	requireOriginalInput(t, pool, duplicate, "1")
	if !duplicate.CreatedAt.Equal(saved.CreatedAt) || !bytes.Equal(first.Payload(), saved.Input.Payload()) {
		t.Fatal("concurrent duplicate")
	}
	chain := []StoredPaperInput{saved}
	for index, kind := range []string{"COMPLETED", "ABORTED"} {
		previous := chain[index].Input
		completion, hash := paperInputCompletion(t, db, previous, kind)
		_, err = CompleteRebalanceRun(ctx, db, completion)
		migrationResult(t, err, nil)
		current := paperInputFixture(t, db, uint64(index+2), previous, hash, "paper/journal", true)
		next, e := StorePaperInput(ctx, db, current)
		migrationResult(t, e, nil)
		chain = append(chain, next)
		requireOriginalInput(t, pool, next, []string{"2", "3"}[index])
		if !bytes.Equal(next.Input.Payload(), current.Payload()) {
			t.Fatal("child roundtrip")
		}
	}
	retry, err := StorePaperInput(ctx, pool, first)
	migrationResult(t, err, nil)
	requireOriginalInput(t, pool, retry, "3")
	if !retry.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatal("historical retry timestamp")
	}
	for _, candidate := range []paperwire.InputRecord{
		paperInputCandidate(t, first, func(d *paperwire.CycleDocument) { d.Before.WalletUSDT = model.Zero() }),
		paperInputCandidate(t, chain[1].Input, func(d *paperwire.CycleDocument) { d.PreviousInputHash = strings.Repeat("a", 64) }),
	} {
		got, e := StorePaperInput(ctx, pool, candidate)
		requireInputReject(t, got, e, ErrPaperInputConflict)
		requireOriginalInput(t, pool, saved, "3")
		requireOriginalInput(t, pool, chain[1], "3")
	}
}

// TestPaperInputAdmission проверяет receipt до INSERT и binding onboarding.
func TestPaperInputAdmission(t *testing.T) {
	ctx, db, first := paperJournalFixture(t)
	wrong := paperInputCandidate(t, first, func(d *paperwire.CycleDocument) { d.Run.OnboardingID = "another-start" })
	got, err := StorePaperInput(ctx, db, wrong)
	requireInputReject(t, got, err, ErrPaperInputConflict)
	migrationValue(t, db, "SELECT count(*) FROM momentum.paper_run_inputs", "0")
	saved, err := StorePaperInput(ctx, db, first)
	migrationResult(t, err, nil)
	completed, hash := paperInputCompletion(t, db, first, "COMPLETED")
	aborted, abortedHash := paperInputCompletion(t, db, first, "ABORTED")
	child := paperInputFixture(t, db, 2, first, hash, "paper/journal", true)
	other := paperInputFixture(t, nil, 2, first, abortedHash, "paper/journal", true)
	// Меняем только тестовую completion, сохраняя обычные ограничения самого журнала.
	paperSchemaExec(t, db, "", "ALTER TABLE momentum.run_completions DISABLE TRIGGER ALL")
	defer paperSchemaExec(t, db, "", "ALTER TABLE momentum.run_completions ENABLE TRIGGER ALL")
	for _, tc := range []struct {
		outcome, reason, plan string
		revision              int
		candidate             paperwire.InputRecord
	}{
		{"", "", "", 0, child},
		{"SKIPPED", completed.Reason, completed.PlanHash, 1, child},
		{"COMPLETED", "plain", completed.PlanHash, 1, child},
		{"COMPLETED", "{", completed.PlanHash, 1, child},
		{"FAILED", completed.Reason, completed.PlanHash, 1, child},
		{"COMPLETED", aborted.Reason, completed.PlanHash, 1, other},
		{"COMPLETED", completed.Reason, strings.Repeat("a", 64), 1, child},
		{"COMPLETED", completed.Reason, completed.PlanHash, 2, child},
		{"FAILED", aborted.Reason, completed.PlanHash, 1, child},
	} {
		if tc.outcome != "" {
			paperSchemaExec(t, db, "", `INSERT INTO momentum.run_completions(run_id,outcome,reason,plan_hash,plan_revision)
 VALUES($1,$2,$3,decode($4,'hex'),$5) ON CONFLICT(run_id) DO UPDATE
 SET (outcome,reason,plan_hash,plan_revision)=(excluded.outcome,excluded.reason,excluded.plan_hash,excluded.plan_revision)`,
				completed.RunID, tc.outcome, tc.reason, tc.plan, tc.revision)
		}
		got, e := storePaperInput(ctx, db, tc.candidate, paperInputStoreHooks{beforeReadback: func(*sql.Tx) error { return errors.New("unexpected INSERT") }})
		requireInputReject(t, got, e, ErrPaperInputConflict)
		requireOriginalInput(t, db, saved, "1")
	}
}

// TestPaperInputStoreFailures проверяет аргументы, сбой SELECT и повреждённый head.
func TestPaperInputStoreFailures(t *testing.T) {
	ctx, db, input := paperJournalFixture(t)
	for _, tc := range []struct {
		ctx context.Context
		db  *sql.DB
		in  paperwire.InputRecord
	}{
		{nil, db, input},
		{ctx, nil, input},
		{ctx, db, paperwire.InputRecord{}},
		{ctx, db, paperInputFixture(t, nil, 1, paperwire.InputRecord{}, "", "paper/journal ", true)},
		{ctx, db, paperInputFixture(t, nil, 1, paperwire.InputRecord{}, "", "paper/journal", false)},
	} {
		got, e := StorePaperInput(tc.ctx, tc.db, tc.in)
		requireInputReject(t, got, e, ErrPaperInputArgument)
	}
	got, e := storePaperInput(ctx, db, input, paperInputStoreHooks{beforeReadback: (*sql.Tx).Rollback})
	requireInputReject(t, got, e, ErrPaperInputUnavailable)
	migrationValue(t, db, "SELECT count(*) FROM momentum.paper_run_inputs", "0")
	paperInputSeed(t, db, input)
	paperSchemaExec(t, db, "", `ALTER TABLE momentum.paper_run_inputs DISABLE TRIGGER paper_run_inputs_immutable;
 UPDATE momentum.paper_run_inputs SET payload=payload||decode('20','hex'),input_hash=sha256(payload||decode('20','hex'))`)
	got, e = StorePaperInput(ctx, db, input)
	requireInputReject(t, got, e, ErrPaperInputCorrupt)
}
