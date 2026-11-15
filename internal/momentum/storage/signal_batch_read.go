package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/signals"
)

// StoredSignalBatch содержит проверенный пакет и UTC timestamp его записи.
type StoredSignalBatch struct {
	Batch     signals.Batch
	CreatedAt time.Time
}

var (
	// ErrSignalBatchUnavailable обозначает невозможность завершить проверку строки.
	ErrSignalBatchUnavailable = errors.New("momentum signal batch unavailable")
	// ErrSignalBatchReadArgument обозначает отсутствующие или некорректные аргументы.
	ErrSignalBatchReadArgument = errors.New("invalid momentum signal batch read argument")
	// ErrSignalBatchAbsent обозначает отсутствие точного ключа в снимке запроса.
	ErrSignalBatchAbsent = errors.New("momentum signal batch absent")
	// ErrSignalBatchCorrupt обозначает строку с непроверяемыми данными или метаданными.
	ErrSignalBatchCorrupt = errors.New("invalid stored momentum signal batch")
)

// ReadSignalBatch проверяет и восстанавливает пакет по каноническому SHA256 ключу.
func ReadSignalBatch(ctx context.Context, db *sql.DB, hash string) (StoredSignalBatch, error) {
	if ctx == nil || db == nil || len(hash) != sha256.Size*2 {
		return StoredSignalBatch{}, ErrSignalBatchReadArgument
	}
	key, err := hex.DecodeString(hash)
	if err != nil || hex.EncodeToString(key) != hash {
		return StoredSignalBatch{}, ErrSignalBatchReadArgument
	}
	if err := ctx.Err(); err != nil {
		return StoredSignalBatch{}, errors.Join(ErrSignalBatchUnavailable, err)
	}
	var storedKey, payload []byte
	var stamp sql.NullTime
	err = db.QueryRowContext(ctx, `SELECT batch_hash, payload,
 CASE WHEN pg_catalog.isfinite(created_at) AND created_at >= TIMESTAMPTZ '1970-01-01 00:00:00+00'
 THEN created_at END
 FROM momentum.signal_batches WHERE batch_hash = $1`, key).Scan(&storedKey, &payload, &stamp)
	if canceled := ctx.Err(); canceled != nil {
		return StoredSignalBatch{}, errors.Join(ErrSignalBatchUnavailable, canceled)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return StoredSignalBatch{}, ErrSignalBatchAbsent
	}
	if err != nil {
		return StoredSignalBatch{}, ErrSignalBatchUnavailable
	}
	if !bytes.Equal(storedKey, key) || !stamp.Valid {
		return StoredSignalBatch{}, ErrSignalBatchCorrupt
	}
	batch, err := signals.DecodeBatch(payload)
	if canceled := ctx.Err(); canceled != nil {
		return StoredSignalBatch{}, errors.Join(ErrSignalBatchUnavailable, canceled)
	}
	if err != nil || batch.Hash() != hash {
		return StoredSignalBatch{}, ErrSignalBatchCorrupt
	}
	if err := ctx.Err(); err != nil {
		return StoredSignalBatch{}, errors.Join(ErrSignalBatchUnavailable, err)
	}
	return StoredSignalBatch{Batch: batch, CreatedAt: stamp.Time.UTC()}, nil
}
