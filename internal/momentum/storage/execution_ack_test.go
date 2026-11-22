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
