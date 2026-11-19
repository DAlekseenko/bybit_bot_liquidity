package history

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/storage"
)

const (
	candleWarmupDays = 31
	candleWarmupDay  = 24 * time.Hour
)

// CandleWarmupStatus описывает готовность дневного окна из локального хранилища.
type CandleWarmupStatus string

const (
	// CandleWarmupReady означает полное непрерывное подтверждённое окно.
	CandleWarmupReady CandleWarmupStatus = "ready"
	// CandleWarmupUnavailable означает зарегистрированный пустой ряд.
	CandleWarmupUnavailable CandleWarmupStatus = "unavailable"
	// CandleWarmupInsufficient означает непрерывный ряд короче 31 дня.
	CandleWarmupInsufficient CandleWarmupStatus = "insufficient"
	// CandleWarmupGap означает пропуск или отсутствие последнего дня.
	CandleWarmupGap CandleWarmupStatus = "gap"
	// CandleWarmupUnconfirmed означает неподтверждённую закрытую свечу.
	CandleWarmupUnconfirmed CandleWarmupStatus = "unconfirmed"
)

// CandleWarmup возвращает точные цены закрытия только для готового окна.
type CandleWarmup struct {
	Status CandleWarmupStatus
	Closes []data.DailyClose
}

type candleWarmupReader func(context.Context, *sql.DB, storage.CandleStream, time.Time, time.Time, int) ([]storage.StoredCandle, error)

// ReadCandleWarmup проверяет дневное окно из одного локального SQL-снимка.
func ReadCandleWarmup(ctx context.Context, db *sql.DB, key storage.CandleStream, cutoff time.Time) (CandleWarmup, error) {
	return readCandleWarmup(ctx, db, key, cutoff, storage.ReadCandleRange)
}

// readCandleWarmup проверяет точные границы и читает один ограниченный диапазон.
func readCandleWarmup(ctx context.Context, db *sql.DB, key storage.CandleStream, cutoff time.Time, read candleWarmupReader) (CandleWarmup, error) {
	var empty CandleWarmup
	cutoff = cutoff.UTC()
	lower := cutoff.Add(-candleWarmupDays * candleWarmupDay)
	if ctx == nil || db == nil || key.IntervalCode != "D" || cutoff.Year() > 294276 ||
		lower.Year() < 1970 || cutoff != cutoff.Truncate(candleWarmupDay) {
		return empty, errors.Join(storage.ErrCandles, storage.ErrCandleArgument)
	}
	if err := ctx.Err(); err != nil {
		return empty, errors.Join(storage.ErrCandles, err)
	}
	rows, err := read(ctx, db, key, lower, cutoff, candleWarmupDays)
	if err != nil {
		return empty, err
	}
	return classifyCandleWarmup(key.Symbol, cutoff, rows)
}

// classifyCandleWarmup различает отсутствие истории, нехватку дней и пропуски.
func classifyCandleWarmup(symbol string, cutoff time.Time, rows []storage.StoredCandle) (CandleWarmup, error) {
	if len(rows) == 0 {
		return CandleWarmup{Status: CandleWarmupUnavailable}, nil
	}
	closes := make([]data.DailyClose, 0, len(rows))
	for _, row := range rows {
		closes = append(closes, data.DailyClose{Symbol: symbol, OpenTime: row.OpenTime, Close: row.Close, Confirmed: row.Confirmed})
	}
	window, err := data.SelectDailyWindow(symbol, closes, cutoff)
	switch err {
	case data.ReasonUnconfirmed:
		return CandleWarmup{Status: CandleWarmupUnconfirmed}, nil
	case data.ReasonGap:
		return CandleWarmup{Status: CandleWarmupGap}, nil
	case data.ReasonInsufficient, nil:
		last := cutoff.Add(-candleWarmupDay)
		for i := len(closes) - 1; i >= 0; i-- {
			if !closes[i].OpenTime.Equal(last) {
				return CandleWarmup{Status: CandleWarmupGap}, nil
			}
			last = last.Add(-candleWarmupDay)
		}
		if err != nil {
			return CandleWarmup{Status: CandleWarmupInsufficient}, nil
		}
		return CandleWarmup{Status: CandleWarmupReady, Closes: window}, nil
	default:
		return CandleWarmup{}, errors.Join(storage.ErrCandles, err)
	}
}
