package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/lib/pq"
)

const (
	intentInsert = `INSERT INTO momentum.order_intents(intent_id,environment,account_id,order_link_id,run_id,
 plan_revision,plan_hash,symbol,side,reduce_only,quantity) VALUES `
	fillInsert = `INSERT INTO momentum.fills(environment,account_id,execution_id,order_link_id,symbol,side,
 quantity,execution_price,fee_usdt,executed_at) VALUES `
	ledgerInsert = `INSERT INTO momentum.ledger_entries(environment,account_id,kind,source_id,execution_id,
 symbol,amount_usdt,occurred_at) VALUES `
	planRef = `'run',1,sha256('one'::bytea)`
)

// executionLedgerDatabase создаёт схему, run, ревизию плана и одно намерение link-1.
func executionLedgerDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	planSchemaRuns(t, db)
	marketStatement(t, db, `INSERT INTO momentum.plan_revisions(run_id,revision,plan_hash,previous_hash,payload,payload_sha256)
 VALUES ('run',1,sha256('one'::bytea),NULL,'p'::bytea,sha256('p'::bytea))`, true)
	marketStatement(t, db, intentInsert+"('i1','testnet','acc','link-1',"+planRef+",'BTCUSDT','BUY',false,0.01)", true)
	return db
}

// commitState выполняет запросы в одной транзакции и проверяет SQLSTATE на COMMIT ("" — успех).
func commitState(t *testing.T, db *sql.DB, want string, statements ...string) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statements {
		if _, err := tx.ExecContext(context.Background(), s); err != nil {
			_ = tx.Rollback()
			t.Fatalf("statement failed before commit: %v", err)
		}
	}
	err = tx.Commit()
	var pgErr *pq.Error
	if want == "" && err != nil || want != "" && (!errors.As(err, &pgErr) || string(pgErr.Code) != want) {
		t.Fatalf("commit: want %q, got %v", want, err)
	}
}

// fillWithLedger возвращает запросы fill и его проводок FILL/FEE.
func fillWithLedger(execID string) []string {
	return []string{
		fillInsert + "('testnet','acc','" + execID + "','link-1','BTCUSDT','BUY',0.01,90000,0.54,'2026-01-05+00')",
		ledgerInsert + "('testnet','acc','FILL','" + execID + "','" + execID + "','BTCUSDT',-900,'2026-01-05+00')",
		ledgerInsert + "('testnet','acc','FEE','" + execID + "','" + execID + "','BTCUSDT',-0.54,'2026-01-05+00')",
	}
}

// TestOrderIntentSchema проверяет уникальность link ID в account/environment, состояния и неизменность identity.
func TestOrderIntentSchema(t *testing.T) {
	db := executionLedgerDatabase(t)
	marketStatement(t, db, intentInsert+"('i2','testnet','other','link-1',"+planRef+",'BTCUSDT','BUY',false,1)", true)
	marketStatement(t, db, intentInsert+"('i3','mainnet','acc','link-1',"+planRef+",'BTCUSDT','BUY',false,1)", true)
	for _, tc := range []struct{ code, values string }{
		{"23505", "('i4','testnet','acc','link-1'," + planRef + ",'BTCUSDT','BUY',false,1)"},
		{"23503", "('i4','testnet','acc','link-4','run',2,sha256('one'::bytea),'BTCUSDT','BUY',false,1)"},
		{"23514", "('i4','testnet','acc','link-4'," + planRef + ",'BTCUSDT','HOLD',false,1)"},
		{"23514", "('i4','testnet','acc','link-4'," + planRef + ",'BTCUSDT','BUY',false,0)"},
		{"23514", "('i4','testnet','acc','link-4'," + planRef + ",'btc','BUY',false,1)"},
		{"23514", "('i4',' ','acc','link-4'," + planRef + ",'BTCUSDT','BUY',false,1)"},
	} {
		runSchemaSQLState(t, db, tc.code, intentInsert+tc.values)
	}
	for _, state := range []string{"SENT", "UNKNOWN", "PARTIALLY_FILLED"} {
		marketStatement(t, db, "UPDATE momentum.order_intents SET state='"+state+"',updated_at=now() WHERE intent_id='i1'", true)
	}
	runSchemaSQLState(t, db, "23514", "UPDATE momentum.order_intents SET state='LOST' WHERE intent_id='i1'")
	for _, set := range []string{"quantity=2", "side='SELL'", "order_link_id='x'", "account_id='x'", "plan_revision=1"} {
		runSchemaSQLState(t, db, "55000", "UPDATE momentum.order_intents SET "+set+" WHERE intent_id='i1'")
	}
	runSchemaSQLState(t, db, "55000", "DELETE FROM momentum.order_intents WHERE intent_id='i1'")
}

// TestBrokerOrderSchema проверяет FK на намерение и уникальность exchange order ID.
func TestBrokerOrderSchema(t *testing.T) {
	db := executionLedgerDatabase(t)
	marketStatement(t, db, intentInsert+"('i2','testnet','acc','link-2',"+planRef+",'BTCUSDT','SELL',true,1)", true)
	const insert = "INSERT INTO momentum.broker_orders(environment,account_id,order_link_id,exchange_order_id,exchange_status,observed_at) VALUES "
	marketStatement(t, db, insert+"('testnet','acc','link-1','ex-1','New','2026-01-05+00')", true)
	runSchemaSQLState(t, db, "23505", insert+"('testnet','acc','link-2','ex-1','New','2026-01-05+00')")
	runSchemaSQLState(t, db, "23503", insert+"('testnet','acc','missing','ex-2','New','2026-01-05+00')")
	marketStatement(t, db, "UPDATE momentum.broker_orders SET exchange_status='Filled' WHERE exchange_order_id='ex-1'", true)
	runSchemaSQLState(t, db, "55000", "UPDATE momentum.broker_orders SET exchange_order_id='ex-9' WHERE exchange_order_id='ex-1'")
}

// TestFillLedgerAtomicity проверяет, что fill без проводок не коммитится, а дубль execution и fee невозможен.
func TestFillLedgerAtomicity(t *testing.T) {
	db := executionLedgerDatabase(t)
	commitState(t, db, "23514", fillWithLedger("e1")[0])
	commitState(t, db, "23514", fillWithLedger("e1")[:2]...)
	migrationValue(t, db, "SELECT count(*) FROM momentum.fills", "0")
	commitState(t, db, "", fillWithLedger("e1")...)
	migrationValue(t, db, "SELECT count(*)||'/'||sum(amount_usdt) FROM momentum.ledger_entries", "2/-900.54")
	runSchemaSQLState(t, db, "23505", fillWithLedger("e1")[0])
	runSchemaSQLState(t, db, "23505", fillWithLedger("e1")[2])
	runSchemaSQLState(t, db, "23503", fillWithLedger("missing")[1])
	runSchemaSQLState(t, db, "23503", fillInsert+"('testnet','acc','e2','no-link','BTCUSDT','BUY',1,1,0,'2026-01-05+00')")
	for _, s := range []string{
		"UPDATE momentum.fills SET fee_usdt=0 WHERE execution_id='e1'",
		"DELETE FROM momentum.fills WHERE execution_id='e1'",
		"UPDATE momentum.ledger_entries SET amount_usdt=0 WHERE source_id='e1'",
		"DELETE FROM momentum.ledger_entries WHERE source_id='e1'",
	} {
		runSchemaSQLState(t, db, "55000", s)
	}
}

// TestLedgerEntryKinds проверяет связь вида проводки с execution и symbol.
func TestLedgerEntryKinds(t *testing.T) {
	db := executionLedgerDatabase(t)
	marketStatement(t, db, ledgerInsert+"('testnet','acc','FUNDING','f1',NULL,'BTCUSDT',-0.1,'2026-01-05+00')", true)
	marketStatement(t, db, ledgerInsert+"('testnet','acc','TRANSFER','t1',NULL,NULL,100,'2026-01-05+00')", true)
	for _, values := range []string{
		"('testnet','acc','FUNDING','f2',NULL,NULL,1,'2026-01-05+00')",
		"('testnet','acc','TRANSFER','t2',NULL,'BTCUSDT',1,'2026-01-05+00')",
		"('testnet','acc','FEE','e9',NULL,'BTCUSDT',1,'2026-01-05+00')",
		"('testnet','acc','REBATE','r1',NULL,'BTCUSDT',1,'2026-01-05+00')",
	} {
		runSchemaSQLState(t, db, "23514", ledgerInsert+values)
	}
	runSchemaSQLState(t, db, "23505", ledgerInsert+"('testnet','acc','FUNDING','f1',NULL,'BTCUSDT',-0.1,'2026-01-05+00')")
}
