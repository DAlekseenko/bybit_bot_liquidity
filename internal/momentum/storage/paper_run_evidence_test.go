package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

type evidenceFixture struct {
	db     *sql.DB
	in     paperwire.InputRecord
	intent OrderIntent
	scope  ControlScope
}

// newEvidenceFixture создаёт именованный M7 с одним ожидаемым исполнением.
func newEvidenceFixture(t *testing.T) evidenceFixture {
	t.Helper()
	_, db, base := paperJournalFixture(t)
	d := base.Values()
	d.Fills = []model.ConfirmedFill{testFill(t, "expected", "2")}
	in, err := paperwire.EncodeInputRecord(d)
	migrationResult(t, err, nil)
	h := in.Header()
	id, err := PaperIntentIdentity(in.Hash(), 0)
	migrationResult(t, err, nil)
	return evidenceFixture{db, in, OrderIntent{Environment: "paper", AccountID: h.Scope.AccountID, IntentID: id, OrderLinkID: id, RunID: h.RunID,
		PlanRevision: 1, PlanHash: h.PlanHash, Symbol: "BTCUSDT", Side: model.FillBuy, Quantity: testQty(t, "2")}, ControlScope(h.Scope)}
}

// read проверяет адресную выдачу через указанный пул.
func (x evidenceFixture) read(t *testing.T, db *sql.DB) PaperRunEvidence {
	t.Helper()
	f, err := ReadPaperRunEvidence(t.Context(), db, x.scope, x.intent.RunID)
	migrationResult(t, err, nil)
	planRepositoryCheck(t, f.Scope == x.scope && f.RunID == x.intent.RunID, "адрес запуска")
	return f
}

// stage сохраняет фазы и сверяет точные факты до записи intent.
func (x evidenceFixture) stage(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	phase := func(input, plan bool) {
		f := x.read(t, x.db)
		lifecyclePhase(t, f.Lifecycle, nil, x.in, input, plan, RunCompletion{})
		executionRowsCounts(t, f.PaperExecutionRows, 0, 0, false)
	}
	phase(false, false)
	_, err := StorePaperInput(ctx, x.db, x.in)
	migrationResult(t, err, nil)
	phase(true, false)
	_, err = StorePlanRevision(ctx, x.db, x.in.Values().Plan)
	migrationResult(t, err, nil)
	phase(true, true)
	migrationResult(t, RecordOrderIntent(ctx, x.db, x.intent), nil)
}

// evidenceError требует нулевой DTO и каждую заявленную категорию ошибки.
func evidenceError(t *testing.T, f PaperRunEvidence, err error, kinds ...error) {
	t.Helper()
	planRepositoryCheck(t, reflect.ValueOf(f).IsZero(), "частичный DTO")
	for _, kind := range kinds {
		migrationResult(t, err, kind)
	}
}

// TestPaperRunEvidenceE1 сверяет durable фазы, точную арифметику, историю и копии.
func TestPaperRunEvidenceE1(t *testing.T) {
	x := newEvidenceFixture(t)
	x.stage(t)
	ctx, id, pool := t.Context(), x.intent.IntentID, completionPool(t, x.db)
	previous := x.read(t, pool)
	want := PaperIntentFact{id, id, "BTCUSDT", "BUY", "2", "1", x.intent.PlanHash, "PENDING", false}
	executionRowsCounts(t, previous.PaperExecutionRows, 1, 0, true)
	planRepositoryCheck(t, previous.Intents()[id] == want, "исходный intent")
	badRevision, badHash := want, want
	badRevision.PlanRevision = "2"
	badHash.PlanHash = strings.Repeat("a", 64)
	for _, bad := range []PaperIntentFact{badRevision, badHash} {
		rows := previous.PaperExecutionRows
		rows.intents = map[string]PaperIntentFact{id: bad}
		planRepositoryCheck(t, !boundPaperExecution(rows, previous.Lifecycle), "принята чужая ревизия или hash")
	}
	migrationResult(t, SetOrderIntentState(ctx, x.db, "paper", x.scope.AccountID, id, "UNKNOWN"), nil)
	for i, state := range []string{"UNKNOWN", "PARTIALLY_FILLED", "FILLED"} {
		if i > 0 {
			fill := testFill(t, []string{"", "partial", "full"}[i], "1")
			result, err := RecordFill(ctx, x.db, "paper", x.scope.AccountID, id, fill)
			if err != nil || !result.Inserted {
				t.Fatal(result, err)
			}
		}
		f := x.read(t, pool)
		want.State = state
		if f.Intents()[id] != want || len(f.Fills()) != i || f.DataHash == previous.DataHash {
			t.Fatal(state)
		}
		for exec, fill := range f.Fills() {
			if fill != (PaperFillFact{id, exec, "BTCUSDT", "BUY", "1", "90000", "0.1", testFill(t, exec, "1").ExecutedAt}) {
				t.Fatal(fill)
			}
		}
		previous = f
	}
	wide := "1." + strings.Repeat("0", 130)
	tx := candleReadTx(t, x.db, sql.TxOptions{})
	marketStatement(t, tx, "SET LOCAL session_replication_role=replica", true)
	executionRowsExec(t, tx, 1, "UPDATE momentum.fills SET quantity=$1::numeric WHERE execution_id='partial'", wide)
	migrationResult(t, tx.Commit(), nil)
	c, receipt := paperInputCompletion(t, x.db, x.in, "COMPLETED")
	saved, err := CompleteRebalanceRun(ctx, x.db, c)
	migrationResult(t, err, nil)
	f := x.read(t, pool)
	lifecyclePhase(t, f.Lifecycle, nil, x.in, true, true, saved)
	values := f.Lifecycle.Input.Values()
	n, d, e := values.Cutoff.WalletUSDT.CanonicalRatio()
	huge, _, he := values.Before.WalletUSDT.CanonicalRatio()
	value, ok := new(big.Int).SetString(huge, 10)
	if e != nil || he != nil || n != "1" || d != "3" || !ok || value.BitLen() != 4096 || saved.CompletedAt.Equal(x.in.Header().ScheduledAt) ||
		len(c.Reason) <= 10000 || saved.Reason != c.Reason || f.Fills()["partial"].Quantity != wide {
		t.Fatal("точность durable данных")
	}
	confirmed, err := f.ConfirmedFills()
	migrationResult(t, err, nil)
	planRepositoryCheck(t, len(confirmed) == 2 && confirmed[1].ExecutionID == "partial", "confirmed fills")
	cmp, err := confirmed[1].Quantity.Cmp(testQty(t, "1"))
	migrationResult(t, err, nil)
	planRepositoryCheck(t, cmp == 0, "точное количество")
	delete(f.Intents(), id)
	delete(f.Fills(), "partial")
	confirmed[0] = model.ConfirmedFill{}
	again, err := f.ConfirmedFills()
	if err != nil || again[0].ExecutionID != "full" || len(f.Intents()) != 1 || len(f.Fills()) != 2 {
		t.Fatal("aliases")
	}
	second := paperInputFixture(t, x.db, 2, x.in, receipt, x.scope.AccountID, true)
	_, err = StorePaperInput(ctx, x.db, second)
	migrationResult(t, err, nil)
	aborted, receipt := paperInputCompletion(t, x.db, second, "ABORTED")
	finish := func(in paperwire.InputRecord, c RunCompletion, hasInput bool) {
		saved, err := CompleteRebalanceRun(ctx, x.db, c)
		migrationResult(t, err, nil)
		result, err := ReadPaperRunEvidence(ctx, pool, x.scope, saved.RunID)
		lifecyclePhase(t, result.Lifecycle, err, in, hasInput, hasInput, saved)
		executionRowsCounts(t, result.PaperExecutionRows, 0, 0, true)
	}
	finish(second, aborted, true)
	third := paperInputFixture(t, x.db, 3, second, receipt, x.scope.AccountID, true)
	finish(third, RunCompletion{RunID: third.Header().RunID, Outcome: RunSkipped, Reason: "нет ребаланса"}, false)
	before := lifecycleSnapshot(t, x.db)
	const effects = `SELECT (SELECT jsonb_agg(i ORDER BY intent_id) FROM momentum.order_intents i)::text ||
 (SELECT jsonb_agg(f ORDER BY execution_id) FROM momentum.fills f)::text ||
 (SELECT jsonb_agg(l ORDER BY kind,source_id) FROM momentum.ledger_entries l)::text`
	rows := migrationValue(t, x.db, effects)
	old := x.read(t, completionPool(t, x.db))
	if old.DataHash != f.DataHash || !reflect.DeepEqual(old, f) || before != lifecycleSnapshot(t, x.db) {
		t.Fatal("исторический запуск")
	}
	migrationValue(t, x.db, effects, rows)
}

// TestPaperRunEvidenceE2 проверяет реальные межтабличные повреждения и их причины.
func TestPaperRunEvidenceE2(t *testing.T) {
	const intent = "UPDATE momentum.order_intents SET "
	const copyIntents = `INSERT INTO momentum.order_intents(intent_id,environment,account_id,order_link_id,run_id,plan_revision,plan_hash,symbol,side,reduce_only,quantity)
 SELECT 'extra'||n,environment,account_id,'extra'||n,run_id,plan_revision,plan_hash,symbol,side,reduce_only,quantity
 FROM momentum.order_intents CROSS JOIN generate_series(1,%d) n`
	for _, tc := range []struct {
		name, sql string
		count     int64
		cause     error
	}{
		{"wrong-id", intent + "intent_id='wrong'", 1, ErrPaperRunEvidenceCorrupt},
		{"wrong-link", intent + "order_link_id='wrong'", 1, ErrPaperRunEvidenceCorrupt},
		{"extra", fmt.Sprintf(copyIntents, 1), 1, ErrPaperRunEvidenceCorrupt},
		{"revision", intent + "plan_revision=2", 1, ErrPaperExecutionRowsCorrupt},
		{"hash", intent + "plan_hash=sha256('wrong'::bytea)", 1, ErrPaperExecutionRowsCorrupt},
		{"orphan", "UPDATE momentum.fills SET order_link_id='absent'", 1, ErrPaperExecutionRowsCorrupt},
		{"FILL", "DELETE FROM momentum.ledger_entries WHERE kind='FILL'", 1, ErrPaperExecutionRowsCorrupt},
		{"FEE", "DELETE FROM momentum.ledger_entries WHERE kind='FEE'", 1, ErrPaperExecutionRowsCorrupt},
		{"cap", fmt.Sprintf(copyIntents, 256), 256, paperwire.ErrPaperResourceLimit},
		{"onboarding", "UPDATE momentum.rebalance_runs SET onboarding_id=NULL", 1, ErrPaperInputCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newEvidenceFixture(t)
			x.stage(t)
			if tc.name != "wrong-link" {
				_, err := RecordFill(t.Context(), x.db, "paper", x.scope.AccountID, x.intent.IntentID, testFill(t, "partial", "1"))
				migrationResult(t, err, nil)
			}
			tx := lifecycleMutationTx(t, t.Context(), x.db)
			executionRowsExec(t, tx, tc.count, tc.sql)
			migrationResult(t, tx.Commit(), nil)
			f, err := ReadPaperRunEvidence(t.Context(), x.db, x.scope, x.intent.RunID)
			evidenceError(t, f, err, ErrPaperRunEvidenceCorrupt, tc.cause)
		})
	}
}

// TestPaperRunEvidenceE3 проверяет независимые golden-векторы и порядок JSON при обратной вставке.
func TestPaperRunEvidenceE3(t *testing.T) {
	empty := PaperRunEvidence{PaperExecutionRows: PaperExecutionRows{Scope: ControlScope{"paper", "paper/test", "momentum"}}}
	if paperRunDataHash(empty.PaperExecutionRows, empty.Lifecycle) != "3857a3d95af8f12d1f54880b0b7e0e7f4e60999813347e39b0c9928a31480b62" {
		t.Fatal("golden пустой области")
	}
	h := strings.Repeat("a", 64)
	vector := func(hash string, index int, want string, cause error) {
		t.Helper()
		got, err := PaperIntentIdentity(hash, index)
		migrationResult(t, err, cause)
		planRepositoryCheck(t, got == want, "идентификатор intent")
	}
	vector(h, 0, "paper/intent/b2de0a2cba7126835bee67c5161c4e949816ea9262381007f4946ec1bdd491dc", nil)
	vector(h, 255, "paper/intent/6452ab60f09d97987b1a3d986fd097459f340aa8f20e8ac0dd3d53dfe8b98d50", nil)
	vector(h, -1, "", ErrPaperFactsArgument)
	vector(h, 256, "", ErrPaperFactsArgument)
	vector(strings.ToUpper(h), 0, "", ErrPaperFactsArgument)
	vector(strings.Repeat("g", 64), 0, "", ErrPaperFactsArgument)
	vector(h[:63], 0, "", ErrPaperFactsArgument)
	at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	const golden = "c6d7c3b99ff5d1f1bbeda4a5294367dee5ad2f899b5c439940aa5a0e932d89c3"
	for _, order := range [][]string{{"z", "a"}, {"a", "z"}} {
		f := PaperRunEvidence{PaperExecutionRows: PaperExecutionRows{Scope: ControlScope{"paper", "paper/test", "momentum"}, RunID: "r",
			intents: map[string]PaperIntentFact{"l": {"i", "l", "BTC", "BUY", "2", "1", "h", "FILLED", false}}, fills: map[string]PaperFillFact{}},
			Lifecycle: PaperRunLifecycle{Completed: true, Completion: RunCompletion{Outcome: RunSkipped, Reason: "why", CompletedAt: at}}}
		for _, id := range order {
			f.fills[id] = PaperFillFact{"l", id, "BTC", "BUY", "1", "3", "0", at}
		}
		if paperRunDataHash(f.PaperExecutionRows, f.Lifecycle) != golden {
			t.Fatal("golden fingerprint")
		}
		f.Lifecycle.Completion.Reason = "changed"
		if paperRunDataHash(f.PaperExecutionRows, f.Lifecycle) == golden {
			t.Fatal("reason hash")
		}
		f.Lifecycle.Completion.Reason = "why"
		f.Lifecycle.Completion.CompletedAt = at.Add(time.Microsecond)
		if paperRunDataHash(f.PaperExecutionRows, f.Lifecycle) == golden {
			t.Fatal("SQL time hash")
		}
	}
}

// TestPaperRunEvidenceE4 проверяет provisional visibility, rollback и отказы чтения.
func TestPaperRunEvidenceE4(t *testing.T) {
	x := newEvidenceFixture(t)
	x.stage(t)
	ctx, pool := t.Context(), completionPool(t, x.db)
	run, id := x.intent.RunID, x.intent.IntentID
	before := x.read(t, pool)
	_, err := recordFill(ctx, x.db, "paper", x.scope.AccountID, id, testFill(t, "partial", "1"), func(tx *sql.Tx) error {
		own, err := readPaperRunEvidence(ctx, tx, x.scope, run)
		migrationResult(t, err, nil)
		if own.DataHash == before.DataHash || own.Intents()[id].State != "PARTIALLY_FILLED" || own.Fills()["partial"].FeeUSDT != "0.1" ||
			!reflect.DeepEqual(x.read(t, pool), before) {
			t.Fatal("видимость до commit")
		}
		return errors.New("rollback")
	})
	migrationResult(t, err, ErrExecution)
	if !reflect.DeepEqual(x.read(t, pool), before) {
		t.Fatal("rollback")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	expired, stop := context.WithDeadline(ctx, time.Unix(1, 0))
	defer stop()
	for _, tc := range []struct {
		ctx         context.Context
		db          *sql.DB
		scope       ControlScope
		run         string
		kind, cause error
	}{
		{nil, pool, x.scope, run, ErrPaperFactsArgument, ErrPaperFactsArgument}, {ctx, nil, x.scope, run, ErrPaperFactsArgument, ErrPaperFactsArgument},
		{ctx, pool, ControlScope{}, run, ErrPaperFactsArgument, ErrPaperFactsArgument}, {ctx, pool, x.scope, "", ErrPaperFactsArgument, ErrPaperFactsArgument},
		{ctx, pool, x.scope, "\x00", ErrPaperFactsArgument, ErrPaperFactsArgument}, {ctx, pool, x.scope, "unknown", ErrPaperRunEvidenceAbsent, ErrPaperRunLifecycleAbsent},
		{ctx, pool, ControlScope{"paper", "paper/other", x.scope.StrategyID}, run, ErrPaperRunEvidenceAbsent, ErrPaperRunLifecycleAbsent},
		{ctx, pool, ControlScope{"paper", x.scope.AccountID, "other"}, run, ErrPaperRunEvidenceAbsent, ErrPaperRunLifecycleAbsent},
		{cancelled, pool, x.scope, run, ErrPaperRunEvidenceUnavailable, context.Canceled}, {expired, pool, x.scope, run, ErrPaperRunEvidenceUnavailable, context.DeadlineExceeded},
	} {
		f, err := ReadPaperRunEvidence(tc.ctx, tc.db, tc.scope, tc.run)
		evidenceError(t, f, err, tc.kind, tc.cause)
	}
	f, err := readPaperRunEvidence(ctx, nil, x.scope, run)
	evidenceError(t, f, err, ErrPaperFactsArgument)
	migrationResult(t, pool.Close(), nil)
	f, err = ReadPaperRunEvidence(ctx, pool, x.scope, run)
	evidenceError(t, f, err, ErrPaperRunEvidenceUnavailable)
}
