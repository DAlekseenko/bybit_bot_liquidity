package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// StoredDatasetArtifact содержит проверенные исходные байты и время их первой записи.
type StoredDatasetArtifact struct {
	SHA256    string
	Payload   []byte
	CreatedAt time.Time
}

var (
	// ErrDatasetArtifactArgument обозначает некорректные аргументы репозитория.
	ErrDatasetArtifactArgument = errors.New("invalid momentum dataset artifact argument")
	// ErrDatasetArtifactAbsent обозначает отсутствие ключа в снимке транзакции.
	ErrDatasetArtifactAbsent = errors.New("momentum dataset artifact absent")
	// ErrDatasetArtifactCorrupt обозначает несоответствие сохранённых байтов ключу или метаданным.
	ErrDatasetArtifactCorrupt = errors.New("invalid stored momentum dataset artifact")
	// ErrDatasetArtifactUnavailable обозначает неподтверждённый результат операции.
	ErrDatasetArtifactUnavailable = errors.New("momentum dataset artifact unavailable")
)

// datasetArtifactKey принимает только канонический SHA-256 в lowercase hex.
func datasetArtifactKey(hash string) ([]byte, error) {
	if len(hash) != sha256.Size*2 {
		return nil, ErrDatasetArtifactArgument
	}
	key, err := hex.DecodeString(hash)
	if err != nil || len(key) != sha256.Size || hex.EncodeToString(key) != hash {
		return nil, ErrDatasetArtifactArgument
	}
	return key, nil
}

// checkedDatasetArtifact проверяет SQL-проекцию, не интерпретируя содержимое.
func checkedDatasetArtifact(key, stored, payload []byte, stamp sql.NullTime) (StoredDatasetArtifact, error) {
	sum := sha256.Sum256(payload)
	if len(key) != sha256.Size || !bytes.Equal(key, stored) || !bytes.Equal(key, sum[:]) || len(payload) == 0 ||
		!stamp.Valid || stamp.Time.Before(time.Unix(0, 0)) {
		return StoredDatasetArtifact{}, ErrDatasetArtifactCorrupt
	}
	return StoredDatasetArtifact{SHA256: hex.EncodeToString(key), Payload: bytes.Clone(payload), CreatedAt: stamp.Time.UTC()}, nil
}

// ReadDatasetArtifactTx читает exact-key в caller-owned транзакции без Commit/Rollback.
func ReadDatasetArtifactTx(ctx context.Context, tx *sql.Tx, hash string) (StoredDatasetArtifact, error) {
	key, err := datasetArtifactKey(hash)
	if ctx == nil || tx == nil || err != nil {
		return StoredDatasetArtifact{}, ErrDatasetArtifactArgument
	}
	if err := ctx.Err(); err != nil {
		return StoredDatasetArtifact{}, errors.Join(ErrDatasetArtifactUnavailable, err)
	}
	var stored, payload []byte
	var stamp sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT artifact_hash, payload,
 CASE WHEN pg_catalog.isfinite(created_at) AND created_at >= TIMESTAMPTZ '1970-01-01 00:00+00'
 THEN created_at END FROM momentum.dataset_artifacts WHERE artifact_hash = $1`, key).Scan(&stored, &payload, &stamp)
	if canceled := ctx.Err(); canceled != nil {
		return StoredDatasetArtifact{}, errors.Join(ErrDatasetArtifactUnavailable, canceled)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return StoredDatasetArtifact{}, ErrDatasetArtifactAbsent
	}
	if err != nil {
		return StoredDatasetArtifact{}, ErrDatasetArtifactUnavailable
	}
	got, err := checkedDatasetArtifact(key, stored, payload, stamp)
	if canceled := ctx.Err(); canceled != nil {
		return StoredDatasetArtifact{}, errors.Join(ErrDatasetArtifactUnavailable, canceled)
	}
	return got, err
}

// WriteDatasetArtifactTx добавляет или проверяет exact bytes; фиксацией целой операции владеет caller.
func WriteDatasetArtifactTx(ctx context.Context, tx *sql.Tx, hash string, payload []byte) (StoredDatasetArtifact, error) {
	key, err := datasetArtifactKey(hash)
	if ctx == nil || tx == nil || err != nil || len(payload) == 0 {
		return StoredDatasetArtifact{}, ErrDatasetArtifactArgument
	}
	payload = bytes.Clone(payload)
	sum := sha256.Sum256(payload)
	if !bytes.Equal(key, sum[:]) {
		return StoredDatasetArtifact{}, ErrDatasetArtifactArgument
	}
	if err := ctx.Err(); err != nil {
		return StoredDatasetArtifact{}, errors.Join(ErrDatasetArtifactUnavailable, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO momentum.dataset_artifacts (artifact_hash, payload)
 VALUES ($1, $2) ON CONFLICT (artifact_hash) DO NOTHING`, key, payload)
	if err != nil {
		return StoredDatasetArtifact{}, errors.Join(ErrDatasetArtifactUnavailable, ctx.Err())
	}
	got, err := ReadDatasetArtifactTx(ctx, tx, hash)
	if errors.Is(err, ErrDatasetArtifactAbsent) {
		return StoredDatasetArtifact{}, ErrDatasetArtifactUnavailable
	}
	if err != nil {
		return StoredDatasetArtifact{}, err
	}
	if !bytes.Equal(got.Payload, payload) {
		return StoredDatasetArtifact{}, ErrDatasetArtifactCorrupt
	}
	return got, nil
}
