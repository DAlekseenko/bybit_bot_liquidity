package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"bybit_bot_liquidity/internal/momentum/signals"
)

var (
	// ErrSignalBatchNotInserted обозначает подтверждённое отсутствие вставки этим вызовом.
	ErrSignalBatchNotInserted = errors.New("momentum signal batch not inserted")
	// ErrSignalBatchInsertUnknown обозначает неподтверждённый итог попытки записи.
	ErrSignalBatchInsertUnknown = errors.New("momentum signal batch insert outcome unknown")
	// ErrSignalBatchInsertUnavailable обозначает недоступное соединение до попытки записи.
	ErrSignalBatchInsertUnavailable = errors.New("momentum signal batch connection unavailable")
	// ErrSignalBatchInsertArgument обозначает отсутствующий context, pool или пакет.
	ErrSignalBatchInsertArgument = errors.New("invalid momentum signal batch insert argument")
)

const insertSignalBatchSQL = `INSERT INTO momentum.signal_batches (batch_hash, payload)
 VALUES ($1, $2) ON CONFLICT (batch_hash) DO NOTHING`

// InsertSignalBatch пытается вставить канонический пакет одним autocommit statement.
func InsertSignalBatch(ctx context.Context, db *sql.DB, batch signals.Batch) error {
	if ctx == nil || db == nil || batch.Hash() == "" {
		return ErrSignalBatchInsertArgument
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := hex.DecodeString(batch.Hash())
	if err != nil || len(key) != sha256.Size {
		return ErrSignalBatchInsertArgument
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return errors.Join(ErrSignalBatchInsertUnavailable, ctx.Err())
	}
	defer conn.Close()

	// Выделенное соединение исключает повтор INSERT при driver.ErrBadConn.
	result, err := conn.ExecContext(ctx, insertSignalBatchSQL, key, batch.Payload())
	if err != nil {
		return errors.Join(ErrSignalBatchInsertUnknown, ctx.Err())
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ErrSignalBatchInsertUnknown
	}
	switch count {
	case 0:
		return ErrSignalBatchNotInserted
	case 1:
		return nil
	default:
		return ErrSignalBatchInsertUnknown
	}
}
