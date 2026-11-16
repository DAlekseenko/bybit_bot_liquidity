package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	ErrCandleConflict = errors.New("momentum candle conflicts with an immutable row")
	ErrCandleTx       = errors.New("momentum candle write requires a read-write READ COMMITTED transaction")
)

// AppendCandlePage добавляет одну страницу; nil оставляет изменения в транзакции caller.
func AppendCandlePage(ctx context.Context, tx *sql.Tx, key CandleStream, page model.CandlePage) (err error) {
	if tx == nil {
		return candleFailure(ctx, ErrCandleTx)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err := ctx.Err(); err != nil {
		return candleFailure(ctx, nil)
	}
	step, err := candleKey(key)
	if err != nil || len(page.Candles) < 1 || len(page.Candles) > 200 || page.Environment != key.Environment ||
		page.Source != "bybit_v5" || page.Category != "linear" || !candlePageOrigin(page) ||
		!candleTime(page.ServerTime, 1) || page.ServerTime.UnixMilli() == 0 {
		return candleFailure(ctx, ErrCandleArgument)
	}
	var readCommitted bool
	err = tx.QueryRowContext(ctx, `SELECT pg_catalog.current_setting('transaction_isolation') = 'read committed'
	 AND pg_catalog.current_setting('transaction_read_only') = 'off'`).Scan(&readCommitted)
	if err != nil {
		return candleFailure(ctx, nil)
	}
	if !readCommitted {
		return candleFailure(ctx, ErrCandleTx)
	}
	var source, category string
	err = tx.QueryRowContext(ctx, `SELECT source,category FROM momentum.datasets
	 WHERE environment=$1 AND dataset_id=$2 FOR SHARE`, key.Environment, key.DatasetID).Scan(&source, &category)
	if errors.Is(err, sql.ErrNoRows) {
		return candleFailure(ctx, ErrCandleDataset)
	}
	if err != nil || source != page.Source || category != page.Category {
		return candleFailure(ctx, nil)
	}
	for i, row := range page.Candles {
		if row.Symbol != key.Symbol || row.IntervalCode != key.IntervalCode || !candleTime(row.OpenTime, step) ||
			(i > 0 && !row.OpenTime.Before(page.Candles[i-1].OpenTime)) ||
			row.Confirmed != !page.ServerTime.Before(row.OpenTime.Add(time.Duration(step)*time.Millisecond)) {
			return candleFailure(ctx, ErrCandleArgument)
		}
		values, err := candleWriteValues(row)
		if err != nil {
			return candleFailure(ctx, err)
		}
		if err := appendCandleRow(ctx, tx, key, row, values); err != nil {
			return candleFailure(ctx, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return candleFailure(ctx, nil)
	}
	return nil
}

// candlePageOrigin проверяет канонический endpoint происхождения входной страницы.
func candlePageOrigin(page model.CandlePage) bool {
	if page.Environment == "testnet" {
		return page.Endpoint == "https://api-testnet.bybit.com/v5/market/kline"
	}
	return page.Endpoint == "https://api.bybit.com/v5/market/kline"
}

// candleWriteValues превращает OHLCV в точные decimal параметры и проверяет границы.
func candleWriteValues(row model.Candle) (values [5]string, err error) {
	for i, d := range [...]model.Decimal{row.Open, row.High, row.Low, row.Close, row.Volume} {
		s, err := d.WireDecimal()
		if err != nil {
			return values, ErrCandleArgument
		}
		parse := model.ParsePrice
		if i == 4 {
			parse = model.ParseQty
		}
		if _, err := parse(s); err != nil {
			return values, ErrCandleArgument
		}
		values[i] = s
	}
	for _, bound := range [...]model.Decimal{row.Open, row.Close} {
		max, _ := row.High.Cmp(bound)
		min, _ := row.Low.Cmp(bound)
		if max < 0 || min > 0 {
			return values, ErrCandleArgument
		}
	}
	return values, nil
}

// appendCandleRow различает вставку, точный повтор и конфликт без изменения строки.
func appendCandleRow(ctx context.Context, tx *sql.Tx, key CandleStream, row model.Candle, values [5]string) error {
	args := []any{key.Environment, key.DatasetID, key.Symbol, key.IntervalCode, row.OpenTime.UTC(),
		values[0], values[1], values[2], values[3], values[4], row.Confirmed}
	result, err := tx.ExecContext(ctx, `INSERT INTO momentum.candles
	 (environment,dataset_id,symbol,interval_code,open_time,open,high,low,close,volume,confirmed)
	 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, args...)
	if err != nil {
		return ErrCandles
	}
	count, err := result.RowsAffected()
	if err != nil || (count != 0 && count != 1) {
		return ErrCandles
	}
	if count == 1 {
		return nil
	}
	// Новый statement после INSERT видит подтверждённого победителя при READ COMMITTED.
	var same bool
	err = tx.QueryRowContext(ctx, `SELECT (open,high,low,close,volume,confirmed) =
	 ($6::numeric,$7::numeric,$8::numeric,$9::numeric,$10::numeric,$11::boolean)
	 FROM momentum.candles WHERE environment=$1 AND dataset_id=$2 AND symbol=$3 AND interval_code=$4
	 AND open_time=$5 FOR SHARE`, args...).Scan(&same)
	if err != nil {
		return ErrCandles
	}
	if !same {
		return ErrCandleConflict
	}
	return nil
}
