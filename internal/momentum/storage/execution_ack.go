package storage

import (
	"context"
	"database/sql"
)

// AcknowledgeOrderIntent переводит намерение в ACKNOWLEDGED только из PENDING,
// SENT или UNKNOWN. Уже продвинутое состояние (PARTIALLY_FILLED, FILLED,
// CANCELLED, REJECTED) не понижается: applied=false без ошибки.
func AcknowledgeOrderIntent(ctx context.Context, db *sql.DB, environment, accountID, orderLinkID string) (applied bool, err error) {
	if ctx == nil || db == nil || !nonBlank(environment, accountID, orderLinkID) {
		return false, executionFailure(ctx, ErrExecutionArgument)
	}
	res, err := db.ExecContext(ctx, `UPDATE momentum.order_intents SET state='ACKNOWLEDGED', updated_at=CURRENT_TIMESTAMP
 WHERE environment=$1 AND account_id=$2 AND order_link_id=$3 AND state IN ('PENDING','SENT','UNKNOWN')`,
		environment, accountID, orderLinkID)
	if err != nil {
		return false, executionFailure(ctx, nil)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM momentum.order_intents
 WHERE environment=$1 AND account_id=$2 AND order_link_id=$3)`, environment, accountID, orderLinkID).Scan(&exists); err != nil {
		return false, executionFailure(ctx, nil)
	}
	if !exists {
		return false, executionFailure(ctx, ErrExecutionAbsent)
	}
	return false, nil
}
