package history

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/storage"
)

// CommonCandleWarmup сохраняет причины готовности и полные окна всего портфеля.
type CommonCandleWarmup struct {
	Statuses map[string]CandleWarmupStatus
	Windows  map[string][]data.DailyClose
}

type commonCandleWarmupReader func(context.Context, *sql.DB, storage.CandleStream, time.Time) (CandleWarmup, error)

// ReadCommonCandleWarmup читает каждый ряд отдельным снимком на одном cutoff.
func ReadCommonCandleWarmup(ctx context.Context, db *sql.DB, environment, datasetID string, symbols []string, cutoff time.Time) (CommonCandleWarmup, error) {
	return readCommonCandleWarmup(ctx, db, environment, datasetID, symbols, cutoff, ReadCandleWarmup)
}

// readCommonCandleWarmup согласует заданный universe без частичных торговых окон.
func readCommonCandleWarmup(ctx context.Context, db *sql.DB, environment, datasetID string, symbols []string, cutoff time.Time, read commonCandleWarmupReader) (CommonCandleWarmup, error) {
	var empty CommonCandleWarmup
	canonical, err := config.CanonicalSymbols(symbols)
	cutoff = cutoff.UTC()
	lower := cutoff.Add(-candleWarmupDays * candleWarmupDay)
	if err != nil || ctx == nil || db == nil ||
		(environment != "testnet" && environment != "mainnet") || !utf8.ValidString(datasetID) ||
		utf8.RuneCountInString(datasetID) < 1 || utf8.RuneCountInString(datasetID) > 128 ||
		strings.Trim(datasetID, " ") != datasetID || strings.ContainsRune(datasetID, 0) ||
		cutoff.Year() > 294276 || lower.Year() < 1970 || cutoff != cutoff.Truncate(candleWarmupDay) {
		return empty, errors.Join(storage.ErrCandles, storage.ErrCandleArgument)
	}
	if err := ctx.Err(); err != nil {
		return empty, errors.Join(storage.ErrCandles, err)
	}
	result := CommonCandleWarmup{Statuses: make(map[string]CandleWarmupStatus, len(canonical))}
	series := make(map[string][]data.DailyClose, len(canonical))
	ready := true
	for _, symbol := range canonical {
		key := storage.CandleStream{Environment: environment, DatasetID: datasetID, Symbol: symbol, IntervalCode: "D"}
		window, err := read(ctx, db, key, cutoff)
		if err != nil {
			return empty, err
		}
		switch window.Status {
		case CandleWarmupReady:
			if len(window.Closes) != candleWarmupDays || !window.Closes[candleWarmupDays-1].OpenTime.Equal(cutoff.Add(-candleWarmupDay)) {
				return empty, errors.Join(storage.ErrCandles, storage.ErrCandleArgument)
			}
			series[symbol] = window.Closes
		case CandleWarmupUnavailable, CandleWarmupInsufficient, CandleWarmupGap, CandleWarmupUnconfirmed:
			ready = false
		default:
			return empty, errors.Join(storage.ErrCandles, storage.ErrCandleArgument)
		}
		result.Statuses[symbol] = window.Status
	}
	if err := ctx.Err(); err != nil {
		return empty, errors.Join(storage.ErrCandles, err)
	}
	if ready {
		result.Windows, err = data.CommonDailyWindow(canonical, series, cutoff)
		if err != nil {
			return empty, errors.Join(storage.ErrCandles, err)
		}
	}
	return result, nil
}
