package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// ErrDataset обозначает отказ операции с provenance без сырых SQL-деталей.
	ErrDataset = errors.New("momentum dataset storage failed")
	// ErrDatasetArgument обозначает невалидный ключ либо отсутствующий pool.
	ErrDatasetArgument = errors.New("invalid momentum dataset argument")
	// ErrDatasetNotFound отличает отсутствующий ключ от отказа хранилища.
	ErrDatasetNotFound = errors.New("momentum dataset not found")
)

// Dataset хранит неизменяемое происхождение одного исторического набора.
type Dataset struct {
	Environment string
	ID          string
	Source      string
	Category    string
	CreatedAt   time.Time
}

// datasetInput проверяет context и точный ключ без SQL и нормализации ввода.
func datasetInput(ctx context.Context, db *sql.DB, environment, id string) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrDataset, err)
	}
	if db == nil || (environment != "mainnet" && environment != "testnet") || !utf8.ValidString(id) ||
		utf8.RuneCountInString(id) < 1 || utf8.RuneCountInString(id) > 128 || strings.Trim(id, " ") != id || strings.ContainsRune(id, 0) {
		return errors.Join(ErrDataset, ErrDatasetArgument)
	}
	return nil
}

// datasetFailure сохраняет категорию и context без сырых SQL-диагностик.
func datasetFailure(ctx context.Context, err error) error {
	if cause := ctx.Err(); cause != nil {
		return errors.Join(ErrDataset, cause)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errors.Join(ErrDataset, ErrDatasetNotFound)
	}
	return ErrDataset
}

// RegisterDataset добавляет provenance либо сохраняет существующую строку целиком.
func RegisterDataset(ctx context.Context, db *sql.DB, environment, id string) error {
	if err := datasetInput(ctx, db, environment, id); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO momentum.datasets(environment,dataset_id,source,category)
 VALUES ($1,$2,'bybit_v5','linear') ON CONFLICT (environment,dataset_id) DO NOTHING`, environment, id)
	if err != nil {
		return datasetFailure(ctx, err)
	}
	return nil
}

// ReadDataset возвращает происхождение и время UTC либо явный NotFound.
func ReadDataset(ctx context.Context, db *sql.DB, environment, id string) (Dataset, error) {
	if err := datasetInput(ctx, db, environment, id); err != nil {
		return Dataset{}, err
	}
	var row Dataset
	var finite bool
	err := db.QueryRowContext(ctx, `SELECT source,category,created_at,isfinite(created_at) FROM momentum.datasets
 WHERE environment=$1 AND dataset_id=$2`, environment, id).Scan(&row.Source, &row.Category, &row.CreatedAt, &finite)
	if err != nil {
		return Dataset{}, datasetFailure(ctx, err)
	}
	if !finite || row.Source != "bybit_v5" || row.Category != "linear" || row.CreatedAt.Before(time.Unix(0, 0)) {
		return Dataset{}, ErrDataset
	}
	row.Environment, row.ID, row.CreatedAt = environment, id, row.CreatedAt.UTC()
	return row, nil
}
