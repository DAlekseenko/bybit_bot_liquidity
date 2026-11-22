package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// testQty разбирает количество для тестов.
func testQty(t *testing.T, s string) model.Decimal {
	t.Helper()
	d, err := model.ParseQty(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// testFill строит BUY fill BTCUSDT по цене 90000 с комиссией 0.1.
func testFill(t *testing.T, id, qty string) model.ConfirmedFill {
	return model.ConfirmedFill{ExecutionID: id, Symbol: "BTCUSDT", Side: model.FillBuy, ExecutedAt: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
		Quantity: testQty(t, qty), ExecutionPrice: testQty(t, "90000"), FeeUSDT: testQty(t, "0.1")}
}

// requireExecutionErr проверяет общую и конкретную категорию ошибки.
func requireExecutionErr(t *testing.T, err, kind error) {
	t.Helper()
	if !errors.Is(err, ErrExecution) || !errors.Is(err, kind) {
		t.Fatalf("want %v, got %v", kind, err)
	}
}

// TestRecordOrderIntent проверяет идемпотентный повтор, конфликт и ручные состояния.
func TestRecordOrderIntent(t *testing.T) {
	db := executionLedgerDatabase(t)
	ctx := context.Background()
	sum := sha256.Sum256([]byte("one"))
	in := OrderIntent{IntentID: "i2", Environment: "testnet", AccountID: "acc", OrderLinkID: "link-2", RunID: "run",
		PlanRevision: 1, PlanHash: hex.EncodeToString(sum[:]), Symbol: "ETHUSDT", Side: model.FillSell, ReduceOnly: true,
		Quantity: testQty(t, "0.50")}
	for i := 0; i < 2; i++ {
		if err := RecordOrderIntent(ctx, db, in); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	changed := in
	changed.Quantity = testQty(t, "0.6")
	requireExecutionErr(t, RecordOrderIntent(ctx, db, changed), ErrExecutionConflict)
	bad := in
	bad.PlanHash = "zz"
	requireExecutionErr(t, RecordOrderIntent(ctx, db, bad), ErrExecutionArgument)
	if err := SetOrderIntentState(ctx, db, "testnet", "acc", "link-2", "UNKNOWN"); err != nil {
		t.Fatal(err)
	}
	migrationValue(t, db, "SELECT state FROM momentum.order_intents WHERE order_link_id='link-2'", "UNKNOWN")
	requireExecutionErr(t, SetOrderIntentState(ctx, db, "testnet", "acc", "link-2", "FILLED"), ErrExecutionArgument)
	requireExecutionErr(t, SetOrderIntentState(ctx, db, "testnet", "acc", "missing", "SENT"), ErrExecutionAbsent)
}

// TestRecordFillPartialAndDuplicate проверяет накопление, no-op повтор, конфликт и запрет переполнения.
func TestRecordFillPartialAndDuplicate(t *testing.T) {
	db := executionLedgerDatabase(t)
	ctx := context.Background()
	steps := []struct {
		fill     model.ConfirmedFill
		inserted bool
		filled   string
		state    string
	}{
		{testFill(t, "e1", "0.004"), true, "0.004", "PARTIALLY_FILLED"},
		{testFill(t, "e1", "0.004"), false, "0.004", "PARTIALLY_FILLED"},
		{testFill(t, "e2", "0.006"), true, "0.01", "FILLED"},
	}
	for i, s := range steps {
		got, err := RecordFill(ctx, db, "testnet", "acc", "link-1", s.fill)
		if err != nil || got.Inserted != s.inserted || got.State != s.state {
			t.Fatalf("step %d: %+v %v", i, got, err)
		}
		if c, _ := got.FilledQuantity.Cmp(testQty(t, s.filled)); c != 0 {
			t.Fatalf("step %d filled mismatch", i)
		}
	}
	changed := testFill(t, "e1", "0.004")
	changed.ExecutionPrice = testQty(t, "1")
	_, err := RecordFill(ctx, db, "testnet", "acc", "link-1", changed)
	requireExecutionErr(t, err, ErrExecutionConflict)
	_, err = RecordFill(ctx, db, "testnet", "acc", "link-1", testFill(t, "e3", "0.001"))
	requireExecutionErr(t, err, ErrExecutionConflict)
	wrongSide := testFill(t, "e4", "0.001")
	wrongSide.Side = model.FillSell
	_, err = RecordFill(ctx, db, "testnet", "acc", "link-1", wrongSide)
	requireExecutionErr(t, err, ErrExecutionConflict)
	_, err = RecordFill(ctx, db, "testnet", "acc", "missing", testFill(t, "e5", "0.001"))
	requireExecutionErr(t, err, ErrExecutionAbsent)
	migrationValue(t, db, "SELECT count(*)||'/'||sum(amount_usdt) FROM momentum.ledger_entries", "4/-900.2")
	migrationValue(t, db, "SELECT state FROM momentum.order_intents WHERE order_link_id='link-1'", "FILLED")
}

// TestRecordFillPreservesCancellation проверяет, что повтор и поздний частичный fill не снимают CANCELLED.
func TestRecordFillPreservesCancellation(t *testing.T) {
	db := executionLedgerDatabase(t)
	ctx := context.Background()
	sum := sha256.Sum256([]byte("one"))
	in := OrderIntent{IntentID: "i3", Environment: "testnet", AccountID: "acc", OrderLinkID: "link-3", RunID: "run",
		PlanRevision: 1, PlanHash: hex.EncodeToString(sum[:]), Symbol: "BTCUSDT", Side: model.FillBuy,
		Quantity: testQty(t, "0.01")}
	if err := RecordOrderIntent(ctx, db, in); err != nil {
		t.Fatal(err)
	}
	const state = "SELECT state FROM momentum.order_intents WHERE order_link_id='link-3'"
	if _, err := RecordFill(ctx, db, "testnet", "acc", "link-3", testFill(t, "c1", "0.004")); err != nil {
		t.Fatal(err)
	}
	if err := SetOrderIntentState(ctx, db, "testnet", "acc", "link-3", "CANCELLED"); err != nil {
		t.Fatal(err)
	}
	for i, s := range []struct {
		id, qty, state string
		inserted       bool
	}{
		{"c1", "0.004", "CANCELLED", false},
		{"c2", "0.002", "CANCELLED", true},
		{"c3", "0.004", "FILLED", true},
	} {
		got, err := RecordFill(ctx, db, "testnet", "acc", "link-3", testFill(t, s.id, s.qty))
		if err != nil || got.Inserted != s.inserted || got.State != s.state {
			t.Fatalf("step %d: %+v %v", i, got, err)
		}
		migrationValue(t, db, state, s.state)
	}
}

// TestRecordFillInjectedFailure проверяет откат fill, проводок и состояния при сбое перед COMMIT.
func TestRecordFillInjectedFailure(t *testing.T) {
	db := executionLedgerDatabase(t)
	_, err := recordFill(context.Background(), db, "testnet", "acc", "link-1", testFill(t, "e1", "0.004"),
		func(*sql.Tx) error { return errors.New("injected") })
	requireExecutionErr(t, err, ErrExecution)
	migrationValue(t, db, `SELECT (SELECT count(*) FROM momentum.fills)||'/'||(SELECT count(*) FROM momentum.ledger_entries)||'/'||
 (SELECT state FROM momentum.order_intents WHERE order_link_id='link-1')`, "0/0/PENDING")
}

// TestRecordFillConcurrent проверяет, что параллельные повторы и разные fills не удваивают P&L.
func TestRecordFillConcurrent(t *testing.T) {
	db := executionLedgerDatabase(t)
	var wg sync.WaitGroup
	errs := make(chan error, 19)
	// 10 повторов одного execution ID и 9 разных fills по 0.001 — ровно объём намерения 0.01.
	for i := 0; i < 19; i++ {
		id := "same"
		if i%2 == 1 {
			id = "e" + strconv.Itoa(i)
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := RecordFill(context.Background(), db, "testnet", "acc", "link-1", testFill(t, id, "0.001"))
			errs <- err
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	migrationValue(t, db, `SELECT (SELECT count(*)||'/'||sum(quantity) FROM momentum.fills)||'/'||
 (SELECT count(*) FROM momentum.ledger_entries)||'/'||(SELECT state FROM momentum.order_intents WHERE order_link_id='link-1')`, "10/0.010/20/FILLED")
}
