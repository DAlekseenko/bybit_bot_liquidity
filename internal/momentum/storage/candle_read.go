package storage

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	ErrCandles        = errors.New("momentum candle storage failed")
	ErrCandleArgument = errors.New("invalid momentum candle argument")
	ErrCandleDataset  = errors.New("momentum candle dataset is unavailable")
	ErrCandleLimit    = errors.New("momentum candle result exceeds limit")
)

// CandleStream задаёт точный namespace одного ряда OHLCV.
type CandleStream struct {
	Environment  string
	DatasetID    string
	Symbol       string
	IntervalCode string
}

// StoredCandle хранит только реальный SQL payload, без отсутствующего turnover.
type StoredCandle struct {
	OpenTime                       time.Time
	Open, High, Low, Close, Volume model.Decimal
	Confirmed                      bool
}

// candleKey проверяет существующие schema key rules без нормализации opaque ID.
func candleKey(key CandleStream) (int64, error) {
	if (key.Environment != "testnet" && key.Environment != "mainnet") || !utf8.ValidString(key.DatasetID) ||
		utf8.RuneCountInString(key.DatasetID) < 1 || utf8.RuneCountInString(key.DatasetID) > 128 ||
		strings.Trim(key.DatasetID, " ") != key.DatasetID || strings.ContainsRune(key.DatasetID, 0) ||
		!regexp.MustCompile(`^[A-Z0-9]+USDT$`).MatchString(key.Symbol) {
		return 0, ErrCandleArgument
	}
	switch key.IntervalCode {
	case "D":
		return 86400000, nil
	case "60":
		return 3600000, nil
	}
	return 0, ErrCandleArgument
}

// candleTime проверяет точный UTC-миллисекундный instant без overflow/truncation.
func candleTime(stamp time.Time, step int64) bool {
	ms := stamp.UnixMilli()
	return ms >= 0 && stamp.UTC().Year() <= 294276 && time.UnixMilli(ms).Equal(stamp) && ms%step == 0
}

// candleFailure сохраняет sentinel/context и не раскрывает SQL, DSN или значения.
func candleFailure(ctx context.Context, kind error) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrCandles, err)
	}
	return errors.Join(ErrCandles, kind)
}

// ReadCandleRange читает полный ограниченный полуоткрытый range по возрастанию UTC.
func ReadCandleRange(ctx context.Context, db *sql.DB, key CandleStream, start, end time.Time, limit int) ([]StoredCandle, error) {
	step, err := candleKey(key)
	if err != nil || db == nil || limit < 1 || limit > 200 || !candleTime(start, step) ||
		!candleTime(end, step) || !start.Before(end) {
		return nil, candleFailure(ctx, ErrCandleArgument)
	}
	if err := ctx.Err(); err != nil {
		return nil, candleFailure(ctx, nil)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, candleFailure(ctx, nil)
	}
	defer tx.Rollback()
	result, err := readCandleRange(ctx, tx, key, start, end, limit, step)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, candleFailure(ctx, nil)
	}
	return result, nil
}

// ReadCandleRangeTx читает диапазон в общей RR READ ONLY транзакции без commit/rollback.
func ReadCandleRangeTx(ctx context.Context, tx *sql.Tx, key CandleStream, start, end time.Time, limit int) ([]StoredCandle, error) {
	if ctx == nil {
		return nil, errors.Join(ErrCandles, ErrCandleArgument)
	}
	step, err := candleKey(key)
	if err != nil || tx == nil || limit < 1 || limit > 200 || !candleTime(start, step) ||
		!candleTime(end, step) || !start.Before(end) {
		return nil, candleFailure(ctx, ErrCandleArgument)
	}
	if ctx.Err() != nil {
		return nil, candleFailure(ctx, nil)
	}
	valid, err := readSnapshotMode(ctx, tx)
	if err != nil {
		return nil, candleFailure(ctx, nil)
	}
	if !valid {
		return nil, candleFailure(ctx, ErrCandleArgument)
	}
	return readCandleRange(ctx, tx, key, start, end, limit, step)
}

// readCandleRange выполняет общую SQL-проекцию без управления транзакцией.
func readCandleRange(ctx context.Context, tx *sql.Tx, key CandleStream, start, end time.Time, limit int, step int64) ([]StoredCandle, error) {
	var source, category string
	err := tx.QueryRowContext(ctx, `SELECT source,category FROM momentum.datasets
 WHERE environment=$1 AND dataset_id=$2`, key.Environment, key.DatasetID).Scan(&source, &category)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, candleFailure(ctx, ErrCandleDataset)
	}
	if err != nil || source != "bybit_v5" || category != "linear" {
		return nil, candleFailure(ctx, nil)
	}
	rows, err := tx.QueryContext(ctx, `SELECT open_time,open::text,high::text,low::text,close::text,volume::text,confirmed
 FROM momentum.candles WHERE environment=$1 AND dataset_id=$2 AND symbol=$3 AND interval_code=$4
 AND open_time >= $5 AND open_time < $6 ORDER BY open_time LIMIT $7`,
		key.Environment, key.DatasetID, key.Symbol, key.IntervalCode, start.UTC(), end.UTC(), limit+1)
	if err != nil {
		return nil, candleFailure(ctx, nil)
	}
	defer rows.Close()
	result := make([]StoredCandle, 0)
	for rows.Next() {
		var row StoredCandle
		var values [5]string
		if err := rows.Scan(&row.OpenTime, &values[0], &values[1], &values[2], &values[3], &values[4], &row.Confirmed); err != nil {
			return nil, candleFailure(ctx, nil)
		}
		fields := []*model.Decimal{&row.Open, &row.High, &row.Low, &row.Close, &row.Volume}
		for i := range values {
			parse := model.ParsePrice
			if i == 4 {
				parse = model.ParseQty
			}
			*fields[i], err = parse(values[i])
			if err != nil {
				return nil, candleFailure(ctx, nil)
			}
		}
		row.OpenTime = row.OpenTime.UTC()
		if !candleTime(row.OpenTime, step) {
			return nil, candleFailure(ctx, nil)
		}
		result = append(result, row)
		if len(result) > limit {
			return nil, candleFailure(ctx, ErrCandleLimit)
		}
	}
	if rows.Err() != nil || rows.Close() != nil {
		return nil, candleFailure(ctx, nil)
	}
	return result, nil
}
