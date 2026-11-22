package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// executionRowsDB создаёт только M1–M11 с четырьмя различимыми run.
func executionRowsDB(t *testing.T) (*sql.DB, ControlScope) {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, migrateMomentumPostgres(t.Context(), db, momentumMigrations()[:11]), nil)
	marketStatement(t, db, `INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES(sha256('cfg'::bytea),'cfg');
 INSERT INTO momentum.rebalance_runs(run_id,account_id,strategy_id,scheduled_at,config_hash)
 SELECT id,acc,strat,'2026-01-01'::date+n,sha256('cfg'::bytea) FROM (VALUES
 ('run','paper/a','s',1),('next','paper/a','s',2),('sibling','paper/a','s2',1),('foreign','paper/b','s',1)) v(id,acc,strat,n);
 INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,previous_hash,payload,payload_sha256)
 SELECT run_id,1,sha256('one'::bytea),NULL,'p',sha256('p'::bytea) FROM momentum.rebalance_runs`, true)
	return db, ControlScope{"paper", "paper/a", "s"}
}

// executionRowsRead сверяет категорию ошибки и отсутствие частичных данных.
func executionRowsRead(t *testing.T, db *sql.DB, s ControlScope, run string, want error) PaperExecutionRows {
	t.Helper()
	r, err := ReadPaperExecutionRows(t.Context(), db, s, run)
	migrationResult(t, err, want)
	if err != nil {
		if !reflect.DeepEqual(r, PaperExecutionRows{}) {
			t.Fatal("частичный результат", r)
		}
	} else if r.Scope != s || r.RunID != run {
		t.Fatal("неверная область", r)
	}
	return r
}

// executionRowsCounts проверяет независимые множества и флаг всей области.
func executionRowsCounts(t *testing.T, r PaperExecutionRows, intents, fills int, has bool) {
	t.Helper()
	if len(r.Intents()) != intents || len(r.Fills()) != fills || r.ScopeHasIntents != has {
		t.Fatalf("intents=%d fills=%d has=%t", len(r.intents), len(r.fills), r.ScopeHasIntents)
	}
}

// TestPaperExecutionRowsER1 проверяет reopen, изоляцию областей, точность и владение DTO.
func TestPaperExecutionRowsER1(t *testing.T) {
	db, s := executionRowsDB(t)
	for _, run := range []string{"", "run"} {
		r := executionRowsRead(t, db, s, run, nil)
		executionRowsCounts(t, r, 0, 0, false)
		fills, err := r.ConfirmedFills()
		if err != nil || len(fills) != 0 {
			t.Fatal(fills, err)
		}
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("one")))
	for _, tc := range []struct {
		run, account, link string
		side               model.FillSide
	}{
		{"run", "paper/a", "buy", model.FillBuy}, {"run", "paper/a", "sell", model.FillSell},
		{"next", "paper/a", "next", model.FillBuy}, {"sibling", "paper/a", "sibling", model.FillBuy}, {"foreign", "paper/b", "buy", model.FillBuy},
	} {
		in := OrderIntent{IntentID: tc.run + tc.link, Environment: "paper", AccountID: tc.account, OrderLinkID: tc.link, RunID: tc.run,
			PlanRevision: 1, PlanHash: hash, Symbol: "BTCUSDT", Side: tc.side, ReduceOnly: tc.side == model.FillSell, Quantity: testQty(t, "2")}
		migrationResult(t, RecordOrderIntent(t.Context(), db, in), nil)
	}
	at := time.Date(2026, 1, 5, 0, 0, 0, 123456000, time.UTC)
	expected := []PaperFillFact{
		{"buy", "c", "BTCUSDT", "BUY", "1", "90000", "-0.1", at},
		{"buy", "a", "BTCUSDT", "BUY", "1", "90000", "0", at},
		{"sell", "b", "BTCUSDT", "SELL", "2", "90000", "0.1", at},
		{"next", "next", "BTCUSDT", "BUY", "1", "90000", "0", at},
		{"sibling", "sibling", "BTCUSDT", "BUY", "1", "90000", "0", at},
	}
	for _, fact := range expected {
		f := testFill(t, fact.ExecutionID, fact.Quantity)
		f.FeeUSDT, f.Side, f.ExecutedAt = *controlDecimal(t, fact.FeeUSDT), model.FillSide(fact.Side), at
		result, err := RecordFill(t.Context(), db, "paper", "paper/a", fact.OrderLinkID, f)
		if err != nil || !result.Inserted {
			t.Fatal(result, err)
		}
	}
	migrationResult(t, SetOrderIntentState(t.Context(), db, "paper", "paper/a", "next", "UNKNOWN"), nil)
	marketStatement(t, db, intentInsert+"('scaled','paper','paper/a','scaled','run',1.0,sha256('one'::bytea),'BTCUSDT','BUY',false,1.2300)", true)
	const snapshot = `SELECT (SELECT jsonb_agg(i ORDER BY intent_id) FROM momentum.order_intents i)::text ||
 (SELECT jsonb_agg(f ORDER BY execution_id) FROM momentum.fills f)::text ||
 (SELECT jsonb_agg(l ORDER BY source_id,kind) FROM momentum.ledger_entries l)::text`
	before := migrationValue(t, db, snapshot)
	pool := completionPool(t, db)
	r := executionRowsRead(t, pool, s, "run", nil)
	executionRowsCounts(t, executionRowsRead(t, pool, s, "", nil), 0, 0, true)
	executionRowsCounts(t, r, 3, 3, true)
	want := PaperIntentFact{"scaled", "scaled", "BTCUSDT", "BUY", "1.2300", "1", hash, "PENDING", false}
	if r.Intents()["scaled"] != want || !r.Intents()["sell"].ReduceOnly || executionRowsRead(t, pool, s, "next", nil).Intents()["next"].State != "UNKNOWN" {
		t.Fatal("поля намерений", r.intents)
	}
	for _, fact := range expected[:3] {
		if got := r.Fills()[fact.ExecutionID]; got != fact {
			t.Fatal(got, fact)
		}
	}
	intents, fills := r.Intents(), r.Fills()
	intents["scaled"], intents["x"], fills["b"], fills["x"] = PaperIntentFact{}, want, PaperFillFact{}, fills["a"]
	delete(intents, "buy")
	delete(fills, "a")
	confirmed, err := r.ConfirmedFills()
	migrationResult(t, err, nil)
	if confirmed[0].ExecutionID != "a" || confirmed[1].ExecutionID != "b" || confirmed[2].ExecutionID != "c" {
		t.Fatal(confirmed)
	}
	confirmed[0] = model.ConfirmedFill{}
	again, err := r.ConfirmedFills()
	if err != nil || again[0].ExecutionID != "a" || r.Intents()["scaled"] != want || len(r.Intents()) != 3 || len(r.Fills()) != 3 {
		t.Fatal("изменён DTO")
	}
	migrationValue(t, db, snapshot, before)
}

const rowsIntent = `INSERT INTO momentum.order_intents(intent_id,environment,account_id,order_link_id,run_id,plan_revision,plan_hash,symbol,side,reduce_only,quantity)
 SELECT 'i'||n,'paper','paper/a','l'||n,'run',1,sha256('one'::bytea),'BTC','BUY',false,1 FROM generate_series($1::int,$2::int) n`
const rowsFills = `WITH f AS (INSERT INTO momentum.fills(environment,account_id,execution_id,order_link_id,symbol,side,quantity,execution_price,fee_usdt,executed_at)
 SELECT 'paper','paper/a','e'||n,'l1','BTC','BUY',$3::numeric,1,-0.1,'2026-01-01' FROM generate_series($1::int,$2::int) n RETURNING *)
 INSERT INTO momentum.ledger_entries(environment,account_id,kind,source_id,execution_id,symbol,amount_usdt,occurred_at)
 SELECT environment,account_id,k,execution_id,execution_id,symbol,CASE k WHEN 'FILL' THEN -quantity ELSE -fee_usdt END,executed_at
 FROM f CROSS JOIN (VALUES ('FILL'),('FEE')) kinds(k)`

// executionRowsExec требует успешной мутации и точного числа затронутых строк.
func executionRowsExec(t *testing.T, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, count int64, query string, args ...any) {
	t.Helper()
	res, err := q.ExecContext(t.Context(), query, args...)
	migrationResult(t, err, nil)
	n, err := res.RowsAffected()
	if err != nil || n != count {
		t.Fatalf("затронуто %d, ожидалось %d, ошибка %v", n, count, err)
	}
}

// TestPaperExecutionRowsER2 изолирует каждое доказанное повреждение в отдельной транзакции.
func TestPaperExecutionRowsER2(t *testing.T) {
	db, s := executionRowsDB(t)
	executionRowsExec(t, db, 2, rowsIntent, 1, 2)
	executionRowsExec(t, db, 2, rowsFills, 1, 1, "1")
	const intent = "UPDATE momentum.order_intents SET "
	const fill = "UPDATE momentum.fills SET "
	const ledger = "UPDATE momentum.ledger_entries SET "
	const spare = " WHERE intent_id='i2'"
	const posting = " WHERE kind='FILL'"
	const fee = " WHERE kind='FEE'"
	// Ослабляем только ограничения M11, мешающие внесению указанных ниже повреждений.
	marketStatement(t, db, `ALTER TABLE momentum.order_intents DISABLE TRIGGER order_intents_identity_immutable;
 ALTER TABLE momentum.order_intents DROP CONSTRAINT order_intents_run_id_plan_revision_plan_hash_fkey, DROP CONSTRAINT order_intents_quantity_check;
 ALTER TABLE momentum.fills DISABLE TRIGGER fills_immutable;
 ALTER TABLE momentum.fills DROP CONSTRAINT fills_environment_account_id_order_link_id_fkey, DROP CONSTRAINT fills_quantity_check,
 DROP CONSTRAINT fills_execution_price_check, DROP CONSTRAINT fills_fee_usdt_check, DROP CONSTRAINT fills_executed_at_check;
 ALTER TABLE momentum.ledger_entries DISABLE TRIGGER ledger_entries_immutable;
 ALTER TABLE momentum.ledger_entries DROP CONSTRAINT ledger_entries_environment_account_id_execution_id_fkey, DROP CONSTRAINT ledger_entries_check1`, true)
	for _, tc := range []struct {
		name, mutation string
		probe          bool
	}{
		{"environment", intent + "environment='other'" + spare, true}, {"account", intent + "account_id='paper/b'" + spare, true},
		{"absent-run", intent + "run_id='missing'" + spare, true}, {"foreign-parent", intent + "run_id='foreign'" + spare, true},
		{"plan", intent + "plan_hash=sha256('wrong'::bytea)" + spare, true}, {"revision", intent + "plan_revision=1.5" + spare, true},
		{"intent-quantity", intent + "quantity=0" + spare, false}, {"missing-intent", fill + "order_link_id='missing'", true},
		{"orphan-fill-posting", ledger + "execution_id='missing',source_id='missing'" + posting, true},
		{"orphan-fee-posting", ledger + "execution_id='missing',source_id='missing'" + fee, true},
		{"funding", ledgerInsert + "('paper','paper/a','FUNDING','unexplained',NULL,'BTC',1,now())", true},
		{"transfer", ledgerInsert + "('paper','paper/a','TRANSFER','unexplained',NULL,NULL,1,now())", true},
		{"fill-symbol", fill + "symbol='ETH'", false}, {"fill-side", fill + "side='SELL'", false},
		{"missing-fill", "DELETE FROM momentum.ledger_entries" + posting, false}, {"missing-fee", "DELETE FROM momentum.ledger_entries" + fee, false},
		{"fill-sign", ledger + "amount_usdt=1" + posting, false}, {"fill-size", ledger + "amount_usdt=-2" + posting, false},
		{"fee-sign", ledger + "amount_usdt=-0.1" + fee, false}, {"fee-size", ledger + "amount_usdt=0.2" + fee, false},
		{"source", ledger + "source_id='wrong'" + fee, false}, {"symbol", ledger + "symbol='ETH'" + posting, false},
		{"time", ledger + "occurred_at=occurred_at+interval '1 microsecond'" + fee, false},
		{"quantity", fill + "quantity='Infinity'", false}, {"price", fill + "execution_price=0", false},
		{"fee", fill + "fee_usdt='NaN'", false}, {"timestamp", fill + "executed_at='infinity'", false},
		{"product-overflow", fill + "quantity=power(10::numeric,10000),execution_price=power(10::numeric,10000)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := candleReadTx(t, db, sql.TxOptions{})
			executionRowsExec(t, tx, 1, tc.mutation)
			for _, run := range []string{"run", ""} {
				r, err := readPaperExecutionRows(t.Context(), tx, s, run)
				if run == "" && !tc.probe {
					migrationResult(t, err, nil)
					continue
				}
				migrationResult(t, err, ErrPaperExecutionRowsCorrupt)
				if !reflect.DeepEqual(r, PaperExecutionRows{}) {
					t.Fatal(r)
				}
			}
		})
	}
	marketStatement(t, db, ledgerInsert+"('paper','paper/b','TRANSFER','other',NULL,NULL,9,now())", true)
	executionRowsCounts(t, executionRowsRead(t, db, s, "next", nil), 0, 0, true)
}

// TestPaperExecutionRowsER3 проверяет независимые лимиты и точную границу Decimal.
func TestPaperExecutionRowsER3(t *testing.T) {
	for _, kind := range []string{"intents", "fills"} {
		t.Run(kind, func(t *testing.T) {
			db, s := executionRowsDB(t)
			executionRowsExec(t, db, 1, rowsIntent, 1, 1)
			executionRowsExec(t, db, 2, rowsFills, 1, 1, "1")
			start := 2
			for _, cap := range []int{256, 257} {
				ni, nf := 1, 1
				if kind == "intents" {
					executionRowsExec(t, db, int64(cap-start+1), rowsIntent, start, cap)
					ni = cap
				} else {
					executionRowsExec(t, db, 2*int64(cap-start+1), rowsFills, start, cap, "1")
					nf = cap
				}
				if cap == 256 {
					executionRowsCounts(t, executionRowsRead(t, db, s, "run", nil), ni, nf, true)
				} else {
					executionRowsRead(t, db, s, "run", paperwire.ErrPaperResourceLimit)
				}
				start = cap + 1
				executionRowsCounts(t, executionRowsRead(t, db, s, "", nil), 0, 0, true)
			}
		})
	}
	db, s := executionRowsDB(t)
	executionRowsExec(t, db, 1, rowsIntent, 1, 1)
	den := new(big.Int).Lsh(big.NewInt(1), 4095)
	tiny := new(big.Rat).SetFrac(big.NewInt(1), den).FloatString(4095)
	executionRowsExec(t, db, 2, rowsFills, 1, 1, tiny)
	r := executionRowsRead(t, db, s, "run", nil)
	f, err := r.ConfirmedFills()
	migrationResult(t, err, nil)
	n, d, err := f[0].Quantity.CanonicalRatio()
	if err != nil || n != "1" || d != den.String() || r.Fills()["e1"].Quantity != tiny {
		t.Fatal("неточная величина")
	}
	huge := new(big.Int).Lsh(big.NewInt(1), 4096).String()
	executionRowsExec(t, db, 2, rowsFills, 2, 2, huge)
	r = executionRowsRead(t, db, s, "run", nil)
	f, err = r.ConfirmedFills()
	if f != nil || !errors.Is(err, ErrPaperExecutionRowsCorrupt) || !errors.Is(err, paperwire.ErrPaperWire) || r.Fills()["e2"].Quantity != huge {
		t.Fatal(err)
	}
}

// TestPaperExecutionRowsER4 проверяет транзакцию владельца и категории недоступности.
func TestPaperExecutionRowsER4(t *testing.T) {
	db, s := executionRowsDB(t)
	pool := completionPool(t, db)
	tx := candleReadTx(t, db, sql.TxOptions{})
	executionRowsExec(t, tx, 1, rowsIntent, 1, 1)
	executionRowsExec(t, tx, 2, rowsFills, 1, 1, "2.000")
	r, err := readPaperExecutionRows(t.Context(), tx, s, "run")
	migrationResult(t, err, nil)
	executionRowsCounts(t, r, 1, 1, true)
	if r.Fills()["e1"].Quantity != "2.000" {
		t.Fatal(r.fills)
	}
	executionRowsCounts(t, executionRowsRead(t, pool, s, "run", nil), 0, 0, false)
	migrationResult(t, tx.Commit(), nil)
	executionRowsCounts(t, executionRowsRead(t, pool, s, "run", nil), 1, 1, true)
	_, err = readPaperExecutionRows(t.Context(), tx, s, "run")
	migrationResult(t, err, ErrPaperExecutionRowsUnavailable)
	for _, run := range []string{"absent", "foreign", "sibling"} {
		executionRowsRead(t, pool, s, run, ErrPaperExecutionRowsAbsent)
	}
	executionRowsRead(t, pool, s, "  ", ErrPaperExecutionRowsArgument)
	for _, invalid := range []ControlScope{{"testnet", "paper/a", "s"}, {"paper", "paper/", "s"}, {"paper", "paper/a", ""}} {
		executionRowsRead(t, pool, invalid, "", ErrPaperExecutionRowsArgument)
	}
	executionRowsRead(t, nil, s, "", ErrPaperExecutionRowsArgument)
	var missing context.Context
	_, err = ReadPaperExecutionRows(missing, pool, s, "")
	migrationResult(t, err, ErrPaperExecutionRowsArgument)
	_, err = readPaperExecutionRows(t.Context(), nil, s, "run")
	migrationResult(t, err, ErrPaperExecutionRowsArgument)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = ReadPaperExecutionRows(cancelled, pool, s, "run")
	migrationResult(t, err, context.Canceled)
	migrationResult(t, err, ErrPaperExecutionRowsUnavailable)
	migrationResult(t, pool.Close(), nil)
	executionRowsRead(t, pool, s, "run", ErrPaperExecutionRowsUnavailable)
	marketStatement(t, db, "ALTER TABLE momentum.fills RENAME TO sensitive_sql_marker", true)
	executionRowsRead(t, db, s, "run", ErrPaperExecutionRowsUnavailable)
}
