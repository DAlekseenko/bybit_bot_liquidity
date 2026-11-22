package storage

import (
	"context"
	"database/sql"
)

// AcknowledgeOrderIntent переводит намерение в ACKNOWLEDGED только из PENDING,
// SENT или UNKNOWN. Уже продвинутое состояние (PARTIALLY_FILLED, FILLED,
// CANCELLED, REJECTED) не понижается: applied=false без ошибки.
func AcknowledgeOrderIntent(ctx context.Context, db *sql.DB, environment, accountID, orderLinkID string) (applied bool, err error) {
	return advanceOrderIntent(ctx, db, environment, accountID, orderLinkID, `state='ACKNOWLEDGED'
 WHERE environment=$1 AND account_id=$2 AND order_link_id=$3 AND state IN ('PENDING','SENT','UNKNOWN')`)
}

// MarkOrderIntentUnknown переводит намерение в UNKNOWN (неизвестный исход отправки)
// только из PENDING или SENT. Состояние, уже продвинутое fills или отменой во время
// ожидания ответа, сохраняется: applied=false без ошибки.
func MarkOrderIntentUnknown(ctx context.Context, db *sql.DB, environment, accountID, orderLinkID string) (applied bool, err error) {
	return advanceOrderIntent(ctx, db, environment, accountID, orderLinkID, `state='UNKNOWN'
 WHERE environment=$1 AND account_id=$2 AND order_link_id=$3 AND state IN ('PENDING','SENT')`)
}

// advanceOrderIntent выполняет условный переход (SET ... WHERE из константы вызывающего);
// отсутствие намерения — ErrExecutionAbsent, неподходящее состояние — applied=false.
func advanceOrderIntent(ctx context.Context, db *sql.DB, environment, accountID, orderLinkID, transition string) (bool, error) {
	if ctx == nil || db == nil || !nonBlank(environment, accountID, orderLinkID) {
		return false, executionFailure(ctx, ErrExecutionArgument)
	}
	res, err := db.ExecContext(ctx, `UPDATE momentum.order_intents SET updated_at=CURRENT_TIMESTAMP, `+transition,
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
