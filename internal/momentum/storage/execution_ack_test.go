package storage

import (
	"context"
	"testing"
)

// TestAcknowledgeOrderIntent проверяет переход в ACKNOWLEDGED без понижения продвинутых состояний.
func TestAcknowledgeOrderIntent(t *testing.T) {
	db := executionLedgerDatabase(t)
	ctx := context.Background()
	const state = "SELECT state FROM momentum.order_intents WHERE order_link_id='link-1'"
	if err := SetOrderIntentState(ctx, db, "testnet", "acc", "link-1", "UNKNOWN"); err != nil {
		t.Fatal(err)
	}
	if applied, err := AcknowledgeOrderIntent(ctx, db, "testnet", "acc", "link-1"); err != nil || !applied {
		t.Fatalf("unknown -> ack: %v %v", applied, err)
	}
	migrationValue(t, db, state, "ACKNOWLEDGED")
	if err := SetOrderIntentState(ctx, db, "testnet", "acc", "link-1", "CANCELLED"); err != nil {
		t.Fatal(err)
	}
	if applied, err := AcknowledgeOrderIntent(ctx, db, "testnet", "acc", "link-1"); err != nil || applied {
		t.Fatalf("cancelled must not be downgraded: %v %v", applied, err)
	}
	migrationValue(t, db, state, "CANCELLED")
	_, err := AcknowledgeOrderIntent(ctx, db, "testnet", "acc", "missing")
	requireExecutionErr(t, err, ErrExecutionAbsent)
}

// TestMarkOrderIntentUnknown проверяет, что таймаут отправки не затирает fills и отмену.
func TestMarkOrderIntentUnknown(t *testing.T) {
	db := executionLedgerDatabase(t)
	ctx := context.Background()
	const state = "SELECT state FROM momentum.order_intents WHERE order_link_id='link-1'"
	if err := SetOrderIntentState(ctx, db, "testnet", "acc", "link-1", "SENT"); err != nil {
		t.Fatal(err)
	}
	if applied, err := MarkOrderIntentUnknown(ctx, db, "testnet", "acc", "link-1"); err != nil || !applied {
		t.Fatalf("sent -> unknown: %v %v", applied, err)
	}
	migrationValue(t, db, state, "UNKNOWN")
	for _, advanced := range []string{"PARTIALLY_FILLED", "CANCELLED", "ACKNOWLEDGED"} {
		if _, err := db.ExecContext(ctx, "UPDATE momentum.order_intents SET state=$1 WHERE order_link_id='link-1'", advanced); err != nil {
			t.Fatal(err)
		}
		if applied, err := MarkOrderIntentUnknown(ctx, db, "testnet", "acc", "link-1"); err != nil || applied {
			t.Fatalf("%s must not be downgraded: %v %v", advanced, applied, err)
		}
		migrationValue(t, db, state, advanced)
	}
	_, err := MarkOrderIntentUnknown(ctx, db, "testnet", "acc", "missing")
	requireExecutionErr(t, err, ErrExecutionAbsent)
}
