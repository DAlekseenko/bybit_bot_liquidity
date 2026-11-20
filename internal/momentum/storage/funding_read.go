package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// ErrFundingLimit обозначает диапазон с большим числом сохранённых событий, чем limit.
var ErrFundingLimit = errors.New("momentum funding result exceeds limit")

// ReadFundingRange возвращает все сохранённые события включительного диапазона по возрастанию.
func ReadFundingRange(ctx context.Context, db *sql.DB, key FundingStream, start, end time.Time, limit int) ([]model.FundingEvent, error) {
	if ctx == nil {
		return nil, fundingFailure(ctx, ErrFundingArgument)
	}
	if ctx.Err() != nil {
		return nil, fundingFailure(ctx, nil)
	}
	if db == nil || !validFundingStream(key) || limit < 1 || limit > 200 ||
		!candleTime(start, 1) || !candleTime(end, 1) || end.Before(start) {
		return nil, fundingFailure(ctx, ErrFundingArgument)
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fundingFailure(ctx, nil)
	}
	defer tx.Rollback()

	result, err := readFundingRange(ctx, tx, key, start, end, limit)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fundingFailure(ctx, nil)
	}
	return result, nil
}

// ReadFundingRangeTx читает включительный диапазон в caller-owned RR READ ONLY без commit/rollback.
func ReadFundingRangeTx(ctx context.Context, tx *sql.Tx, key FundingStream, start, end time.Time, limit int) ([]model.FundingEvent, error) {
	if ctx == nil {
		return nil, fundingFailure(ctx, ErrFundingArgument)
	}
	if ctx.Err() != nil {
		return nil, fundingFailure(ctx, nil)
	}
	if tx == nil || !validFundingStream(key) || limit < 1 || limit > 200 || !candleTime(start, 1) || !candleTime(end, 1) || end.Before(start) {
		return nil, fundingFailure(ctx, ErrFundingArgument)
	}
	valid, err := readSnapshotMode(ctx, tx)
	if err != nil {
		return nil, fundingFailure(ctx, nil)
	}
	if !valid {
		return nil, fundingFailure(ctx, ErrFundingArgument)
	}
	return readFundingRange(ctx, tx, key, start, end, limit)
}

// readFundingRange выполняет прежнюю provenance/row проекцию без управления транзакцией.
func readFundingRange(ctx context.Context, tx *sql.Tx, key FundingStream, start, end time.Time, limit int) ([]model.FundingEvent, error) {
	var source, category string
	err := tx.QueryRowContext(ctx, `SELECT source,category FROM momentum.datasets
WHERE environment=$1 AND dataset_id=$2`, key.Environment, key.DatasetID).Scan(&source, &category)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fundingFailure(ctx, ErrFundingDataset)
	}
	if err != nil {
		return nil, fundingFailure(ctx, nil)
	}
	if source != "bybit_v5" || category != "linear" {
		return nil, fundingFailure(ctx, ErrFundingDataset)
	}

	rows, err := tx.QueryContext(ctx, `SELECT funding_time,pg_catalog.trim_scale(rate)::text
FROM momentum.funding WHERE environment=$1 AND dataset_id=$2 AND symbol=$3
AND funding_time >= $4 AND funding_time <= $5
ORDER BY funding_time LIMIT $6`, key.Environment, key.DatasetID, key.Symbol, start.UTC(), end.UTC(), limit+1)
	if err != nil {
		return nil, fundingFailure(ctx, nil)
	}
	defer rows.Close()

	result := make([]model.FundingEvent, 0)
	for rows.Next() {
		if len(result) == limit {
			return nil, fundingFailure(ctx, ErrFundingLimit)
		}
		var event model.FundingEvent
		var rate string
		if err := rows.Scan(&event.FundingTime, &rate); err != nil {
			return nil, fundingFailure(ctx, nil)
		}
		event.FundingTime = event.FundingTime.UTC()
		if !candleTime(event.FundingTime, 1) || event.FundingTime.Before(start) || event.FundingTime.After(end) ||
			(len(result) > 0 && !result[len(result)-1].FundingTime.Before(event.FundingTime)) {
			return nil, fundingFailure(ctx, nil)
		}
		event.Rate, err = model.ParseRate(rate)
		if err != nil {
			return nil, fundingFailure(ctx, nil)
		}
		result = append(result, event)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return nil, fundingFailure(ctx, nil)
	}
	return result, nil
}
