// Package history выполняет ограниченные шаги импорта исторических данных.

package history

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/storage"
)

var (
	// ErrCandleImportFetch означает, что источник не подтвердил запрос страницы.
	ErrCandleImportFetch = errors.New("momentum candle import page fetch failed")
	// ErrCandleImportCommitUnknown означает, что результат записи требует сверки.
	ErrCandleImportCommitUnknown = errors.New("momentum candle import commit outcome unknown")
)

// CandleImportPageSource получает одну страницу по логическим аргументам public API.
type CandleImportPageSource func(context.Context, string, string, int64, int64) (model.CandlePage, error)

// StepCandleImport читает точное задание и фиксирует не более одного перехода.
func StepCandleImport(ctx context.Context, db *sql.DB, source CandleImportPageSource, key storage.CandleImportWindow) (storage.StoredCandleImport, error) {
	var empty storage.StoredCandleImport
	if ctx == nil || source == nil {
		return empty, errors.Join(storage.ErrCandles, storage.ErrCandleArgument)
	}
	job, err := storage.ReadCandleImport(ctx, db, key)
	if err != nil {
		return empty, err
	}
	switch job.Checkpoint.State {
	case "complete", "blocked":
		return job, nil
	}
	if err := ctx.Err(); err != nil {
		return empty, errors.Join(storage.ErrCandles, err)
	}
	if job.Checkpoint.Revision == math.MaxInt64 {
		return empty, errors.Join(storage.ErrCandles, storage.ErrCandleImportState)
	}
	var observation *storage.CandleImportPageObservation
	key = job.Window
	if key.LowerOpen.UnixMilli() > 0 {
		stream := key.Stream
		end := job.Checkpoint.NextEnd
		page, err := source(ctx, stream.Symbol, stream.IntervalCode, key.LowerOpen.UnixMilli(), end.UnixMilli())
		if err != nil {
			return empty, candleImportStepFailure(ctx, ErrCandleImportFetch, err)
		}
		observation = &storage.CandleImportPageObservation{
			Symbol: stream.Symbol, IntervalCode: stream.IntervalCode, Start: key.LowerOpen, End: end, Page: page,
		}
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return empty, candleImportStepFailure(ctx, storage.ErrCandles, err)
	}
	defer tx.Rollback()

	checkpoint, reason, err := storage.ApplyCandleImportObservation(ctx, tx, key, job.Checkpoint, observation)
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, candleImportStepFailure(ctx, ErrCandleImportCommitUnknown, err)
	}
	job.Checkpoint, job.Reason, job.LastServerTime = checkpoint, string(reason), nil
	if observation != nil {
		actual := observation.Page.ServerTime.UTC()
		job.LastServerTime = &actual
	}
	return job, nil
}

// candleImportStepFailure сохраняет только безопасную категорию и контекстный sentinel.
func candleImportStepFailure(ctx context.Context, kind, cause error) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(kind, err)
	}
	for _, contextError := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(cause, contextError) {
			return errors.Join(kind, contextError)
		}
	}
	return kind
}
