package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	// ErrMarkPriceRead обозначает безопасный отказ чтения mark prices.
	ErrMarkPriceRead = errors.New("momentum mark price read failed")
	// ErrMarkPriceReadArgument обозначает неверный ключ, диапазон или лимит.
	ErrMarkPriceReadArgument = errors.New("invalid momentum mark price read argument")
	// ErrMarkPriceReadDataset обозначает отсутствующий или несовместимый dataset.
	ErrMarkPriceReadDataset = errors.New("momentum mark price read dataset is unavailable")
	// ErrMarkPriceReadLimit обозначает диапазон с числом строк сверх лимита.
	ErrMarkPriceReadLimit = errors.New("momentum mark price read result exceeds limit")
)

// markReadFailure сохраняет безопасные sentinel и ошибку контекста без SQL-диагностики.
func markReadFailure(ctx context.Context, kind error) error {
	if ctx != nil && ctx.Err() != nil {
		return errors.Join(ErrMarkPriceRead, ctx.Err())
	}
	return errors.Join(ErrMarkPriceRead, kind)
}

// ReadMarkPriceRange читает только mark OHLC в полуоткрытом диапазоне по возрастанию UTC.
func ReadMarkPriceRange(ctx context.Context, db *sql.DB, key CandleStream, start, end time.Time, limit int) ([]model.MarkPriceCandle, error) {
	if ctx == nil {
		return nil, markReadFailure(ctx, ErrMarkPriceReadArgument)
	}
	if ctx.Err() != nil {
		return nil, markReadFailure(ctx, nil)
	}
	step, keyErr := candleKey(key)
	if db == nil || keyErr != nil || limit < 1 || limit > 200 || !candleTime(start, 1) ||
		!candleTime(end, 1) || !start.Before(end) {
		return nil, markReadFailure(ctx, ErrMarkPriceReadArgument)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, markReadFailure(ctx, nil)
	}
	defer tx.Rollback()
	result, err := readMarkPriceRange(ctx, tx, key, start, end, limit, step)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, markReadFailure(ctx, nil)
	}
	return result, nil
}

// ReadMarkPriceRangeTx читает mark OHLC в caller-owned RR READ ONLY без её завершения.
func ReadMarkPriceRangeTx(ctx context.Context, tx *sql.Tx, key CandleStream, start, end time.Time, limit int) ([]model.MarkPriceCandle, error) {
	if ctx == nil {
		return nil, markReadFailure(ctx, ErrMarkPriceReadArgument)
	}
	if ctx.Err() != nil {
		return nil, markReadFailure(ctx, nil)
	}
	step, keyErr := candleKey(key)
	if tx == nil || keyErr != nil || limit < 1 || limit > 200 || !candleTime(start, 1) ||
		!candleTime(end, 1) || !start.Before(end) {
		return nil, markReadFailure(ctx, ErrMarkPriceReadArgument)
	}
	valid, err := readSnapshotMode(ctx, tx)
	if err != nil {
		return nil, markReadFailure(ctx, nil)
	}
	if !valid {
		return nil, markReadFailure(ctx, ErrMarkPriceReadArgument)
	}
	return readMarkPriceRange(ctx, tx, key, start, end, limit, step)
}

// readMarkPriceRange выполняет общую точную проекцию без commit/rollback.
func readMarkPriceRange(ctx context.Context, tx *sql.Tx, key CandleStream, start, end time.Time, limit int, step int64) ([]model.MarkPriceCandle, error) {
	var source, category string
	err := tx.QueryRowContext(ctx, `SELECT source,category FROM momentum.datasets
 WHERE environment=$1 AND dataset_id=$2`, key.Environment, key.DatasetID).Scan(&source, &category)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (source != "bybit_v5" || category != "linear")) {
		return nil, markReadFailure(ctx, ErrMarkPriceReadDataset)
	}
	if err != nil {
		return nil, markReadFailure(ctx, nil)
	}
	rows, err := tx.QueryContext(ctx, `SELECT open_time,pg_catalog.trim_scale(open)::text,
 pg_catalog.trim_scale(high)::text,pg_catalog.trim_scale(low)::text,pg_catalog.trim_scale(close)::text,confirmed
 FROM momentum.mark_prices WHERE environment=$1 AND dataset_id=$2 AND symbol=$3 AND interval_code=$4
 AND open_time >= $5 AND open_time < $6 ORDER BY open_time LIMIT $7`,
		key.Environment, key.DatasetID, key.Symbol, key.IntervalCode, start.UTC(), end.UTC(), limit+1)
	if err != nil {
		return nil, markReadFailure(ctx, nil)
	}
	defer rows.Close()
	result := make([]model.MarkPriceCandle, 0)
	for rows.Next() {
		if len(result) == limit {
			return nil, markReadFailure(ctx, ErrMarkPriceReadLimit)
		}
		var row model.MarkPriceCandle
		var raw [4]string
		if err := rows.Scan(&row.OpenTime, &raw[0], &raw[1], &raw[2], &raw[3], &row.Confirmed); err != nil {
			return nil, markReadFailure(ctx, nil)
		}
		prices, err := markReadPrices(raw)
		if err != nil {
			return nil, markReadFailure(ctx, nil)
		}
		row.Open, row.High, row.Low, row.Close = prices[0], prices[1], prices[2], prices[3]
		row.OpenTime = row.OpenTime.UTC()
		if !candleTime(row.OpenTime, step) || row.OpenTime.Before(start) || !row.OpenTime.Before(end) ||
			(len(result) > 0 && !result[len(result)-1].OpenTime.Before(row.OpenTime)) {
			return nil, markReadFailure(ctx, nil)
		}
		result = append(result, row)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return nil, markReadFailure(ctx, nil)
	}
	return result, nil
}

// markReadPrices проверяет точный NUMERIC projection и согласованность OHLC.
func markReadPrices(raw [4]string) ([4]model.Decimal, error) {
	var prices [4]model.Decimal
	for i, value := range raw {
		parsed, err := model.ParsePrice(value)
		if err != nil {
			return [4]model.Decimal{}, ErrMarkPriceRead
		}
		prices[i] = parsed
	}
	for _, value := range []model.Decimal{prices[0], prices[3]} {
		high, _ := prices[1].Cmp(value)
		low, _ := prices[2].Cmp(value)
		if high < 0 || low > 0 {
			return [4]model.Decimal{}, ErrMarkPriceRead
		}
	}
	return prices, nil
}
