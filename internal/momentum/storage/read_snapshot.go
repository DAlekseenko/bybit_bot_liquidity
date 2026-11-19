package storage

import (
	"context"
	"database/sql"
)

// readSnapshotMode проверяет реальный режим caller-owned транзакции.
func readSnapshotMode(ctx context.Context, tx *sql.Tx) (bool, error) {
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT
pg_catalog.current_setting('transaction_isolation') = 'repeatable read'
AND pg_catalog.current_setting('transaction_read_only') = 'on'`).Scan(&valid)
	return valid, err
}
