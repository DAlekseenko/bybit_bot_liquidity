package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	ErrMarkPrices        = errors.New("momentum mark price storage failed")
	ErrMarkPriceArgument = errors.New("invalid momentum mark price page")
	ErrMarkPriceDataset  = errors.New("momentum mark price dataset is unavailable")
	ErrMarkPriceConflict = errors.New("momentum mark price conflicts with an immutable row")
	ErrMarkPriceTx       = errors.New("momentum mark price write requires a read-write READ COMMITTED transaction")
)

// MarkPriceStream задаёт namespace одного ряда mark OHLC.
type MarkPriceStream CandleStream

// AppendMarkPricePage добавляет подтверждённую страницу в переданную транзакцию.
func AppendMarkPricePage(ctx context.Context, tx *sql.Tx, key MarkPriceStream, page model.MarkPricePage) (err error) {
	if tx == nil {
		return markPriceFailure(ctx, ErrMarkPriceTx)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if ctx.Err() != nil {
		return markPriceFailure(ctx, nil)
	}
	step, err := markPricePageStep(key, page)
	if err != nil {
		return markPriceFailure(ctx, err)
	}
	var readCommitted bool
	err = tx.QueryRowContext(ctx, `SELECT pg_catalog.current_setting('transaction_isolation') = 'read committed'
	 AND pg_catalog.current_setting('transaction_read_only') = 'off'`).Scan(&readCommitted)
	if err != nil {
		return markPriceFailure(ctx, nil)
	}
	if !readCommitted {
		return markPriceFailure(ctx, ErrMarkPriceTx)
	}
	var source, category string
	err = tx.QueryRowContext(ctx, `SELECT source,category FROM momentum.datasets
	 WHERE environment=$1 AND dataset_id=$2 FOR SHARE`, key.Environment, key.DatasetID).Scan(&source, &category)
	if errors.Is(err, sql.ErrNoRows) {
		return markPriceFailure(ctx, ErrMarkPriceDataset)
	}
	if err != nil {
		return markPriceFailure(ctx, nil)
	}
	if source != page.Source || category != page.Category {
		return markPriceFailure(ctx, ErrMarkPriceDataset)
	}
	for i, row := range page.Candles {
		if !markPriceMillis(row.OpenTime) || !candleTime(row.OpenTime, step) || row.OpenTime.Before(page.StartTime) || row.OpenTime.After(page.EndTime) ||
			page.ServerTime.UnixMilli()-row.OpenTime.UnixMilli() < step || !row.Confirmed ||
			(i > 0 && !row.OpenTime.Before(page.Candles[i-1].OpenTime)) {
			return markPriceFailure(ctx, ErrMarkPriceArgument)
		}
		prices, err := markPriceWriteValues(row)
		if err != nil {
			return markPriceFailure(ctx, err)
		}
		if err := appendMarkPriceRow(ctx, tx, key, row.OpenTime, prices); err != nil {
			return markPriceFailure(ctx, err)
		}
	}
	if ctx.Err() != nil {
		return markPriceFailure(ctx, nil)
	}
	return nil
}

// markPricePageStep проверяет ключ, происхождение, границы и размер страницы.
func markPricePageStep(key MarkPriceStream, page model.MarkPricePage) (int64, error) {
	step, err := candleKey(CandleStream(key))
	if err != nil || len(page.Candles) < 1 || len(page.Candles) > 200 || page.Environment != key.Environment ||
		page.Symbol != key.Symbol || page.IntervalCode != key.IntervalCode || page.Source != "bybit_v5" ||
		page.Category != "linear" {
		return 0, ErrMarkPriceArgument
	}
	const path = "/v5/market/mark-price-kline"
	endpoint := "https://api.bybit.com" + path
	if key.Environment == "testnet" {
		endpoint = "https://api-testnet.bybit.com" + path
	}
	if page.Endpoint != endpoint || !markPriceMillis(page.StartTime) || page.StartTime.UnixMilli()%step != 0 ||
		!markPriceMillis(page.EndTime) || page.EndTime.Before(page.StartTime) || !markPriceMillis(page.ServerTime) {
		return 0, ErrMarkPriceArgument
	}
	return step, nil
}

// markPriceMillis проверяет положительный точный Unix-ms без переполнения.
func markPriceMillis(stamp time.Time) bool {
	ms := stamp.UnixMilli()
	return ms > 0 && time.UnixMilli(ms).Equal(stamp)
}

// markPriceWriteValues преобразует валидный OHLC в точные SQL-параметры.
func markPriceWriteValues(row model.MarkPriceCandle) (prices [4]string, err error) {
	for i, price := range [...]model.Decimal{row.Open, row.High, row.Low, row.Close} {
		value, err := price.WireDecimal()
		if err != nil {
			return prices, ErrMarkPriceArgument
		}
		if _, err := model.ParsePrice(value); err != nil {
			return prices, ErrMarkPriceArgument
		}
		prices[i] = value
	}
	for _, value := range [...]model.Decimal{row.Open, row.Close} {
		high, _ := row.High.Cmp(value)
		low, _ := row.Low.Cmp(value)
		if high < 0 || low > 0 {
			return prices, ErrMarkPriceArgument
		}
	}
	return prices, nil
}

// appendMarkPriceRow вставляет новую строку или сверяет неизменяемый повтор.
func appendMarkPriceRow(ctx context.Context, tx *sql.Tx, key MarkPriceStream, open time.Time, prices [4]string) error {
	args := []any{key.Environment, key.DatasetID, key.Symbol, key.IntervalCode, open.UTC(), prices[0], prices[1], prices[2], prices[3]}
	result, err := tx.ExecContext(ctx, `INSERT INTO momentum.mark_prices
	 (environment,dataset_id,symbol,interval_code,open_time,open,high,low,close,confirmed)
	 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,TRUE)
	 ON CONFLICT (environment,dataset_id,symbol,interval_code,open_time) DO NOTHING`, args...)
	if err != nil {
		return ErrMarkPrices
	}
	count, err := result.RowsAffected()
	if err != nil || (count != 0 && count != 1) {
		return ErrMarkPrices
	}
	if count == 1 {
		return nil
	}
	// Отдельный statement видит победителя после ожидания INSERT при READ COMMITTED.
	var same bool
	err = tx.QueryRowContext(ctx, `SELECT (open,high,low,close,confirmed) =
	 ($6::numeric,$7::numeric,$8::numeric,$9::numeric,TRUE)
	 FROM momentum.mark_prices WHERE environment=$1 AND dataset_id=$2 AND symbol=$3 AND interval_code=$4
	 AND open_time=$5 FOR SHARE`, args...).Scan(&same)
	if err != nil {
		return ErrMarkPrices
	}
	if !same {
		return ErrMarkPriceConflict
	}
	return nil
}

// markPriceFailure сохраняет категорию и ошибку контекста без исходной диагностики SQL.
func markPriceFailure(ctx context.Context, kind error) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrMarkPrices, err)
	}
	if kind == nil || kind == ErrMarkPrices {
		return ErrMarkPrices
	}
	return errors.Join(ErrMarkPrices, kind)
}
