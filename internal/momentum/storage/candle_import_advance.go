package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	ErrCandleImportMissing = errors.New("momentum candle import checkpoint is missing")
	ErrCandleImportStale   = errors.New("momentum candle import checkpoint changed")
	ErrCandleImportState   = errors.New("momentum candle import checkpoint cannot advance")
)

// CandleImportWindow задаёт namespace и неизменяемые границы задания.
type CandleImportWindow struct {
	Stream               CandleStream
	LowerOpen, UpperOpen time.Time
}

// CandleImportCheckpoint фиксирует состояние задания, ожидаемое после загрузки страницы.
type CandleImportCheckpoint struct {
	Revision int64
	NextEnd  time.Time
	State    string
}

// AdvanceCandleImportPage атомарно готовит строки и новый курсор; Commit выполняет вызывающий код.
func AdvanceCandleImportPage(ctx context.Context, tx *sql.Tx, key CandleImportWindow, expected CandleImportCheckpoint, page model.CandlePage) (next CandleImportCheckpoint, err error) {
	if tx == nil {
		return next, candleFailure(ctx, ErrCandleTx)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	step, keyErr := candleKey(key.Stream)
	if keyErr != nil || !candleTime(key.LowerOpen, step) || !candleTime(key.UpperOpen, step) ||
		!key.LowerOpen.Before(key.UpperOpen) || expected.Revision < 0 ||
		!candleTime(expected.NextEnd.Add(time.Millisecond), step) ||
		expected.NextEnd.Before(key.LowerOpen.Add(-time.Millisecond)) || !expected.NextEnd.Before(key.UpperOpen) {
		return next, candleFailure(ctx, ErrCandleArgument)
	}
	if ctx.Err() != nil {
		return next, candleFailure(ctx, nil)
	}
	var readCommitted bool
	err = tx.QueryRowContext(ctx, `SELECT pg_catalog.current_setting('transaction_isolation') = 'read committed'
	 AND pg_catalog.current_setting('transaction_read_only') = 'off'`).Scan(&readCommitted)
	if err != nil {
		return next, candleFailure(ctx, nil)
	}
	if !readCommitted {
		return next, candleFailure(ctx, ErrCandleTx)
	}
	stream := key.Stream
	args := []any{stream.Environment, stream.DatasetID, stream.Symbol, stream.IntervalCode, key.LowerOpen.UTC(), key.UpperOpen.UTC()}
	var current CandleImportCheckpoint
	err = tx.QueryRowContext(ctx, `SELECT revision,next_end,state FROM momentum.candle_import_progress
	 WHERE environment=$1 AND dataset_id=$2 AND symbol=$3 AND interval_code=$4
	 AND lower_open=$5 AND upper_open=$6 FOR UPDATE`, args...).Scan(&current.Revision, &current.NextEnd, &current.State)
	if errors.Is(err, sql.ErrNoRows) {
		return next, candleFailure(ctx, ErrCandleImportMissing)
	}
	if err != nil {
		return next, candleFailure(ctx, nil)
	}
	if current.Revision != expected.Revision || current.State != expected.State || !current.NextEnd.Equal(expected.NextEnd) {
		return next, candleFailure(ctx, ErrCandleImportStale)
	}
	if (current.State != "pending" && current.State != "progress") || current.Revision == math.MaxInt64 {
		return next, candleFailure(ctx, ErrCandleImportState)
	}
	oldest, valid := candleImportOldest(key, current.NextEnd, time.Duration(step)*time.Millisecond, page)
	if !valid {
		return next, candleFailure(ctx, ErrCandleArgument)
	}
	if err := AppendCandlePage(ctx, tx, stream, page); err != nil {
		return next, err
	}
	candidate := CandleImportCheckpoint{Revision: current.Revision + 1, NextEnd: oldest.Add(-time.Millisecond).UTC(), State: "progress"}
	if oldest.Equal(key.LowerOpen) {
		candidate.State = "complete"
	}
	args = append(args, candidate.Revision, candidate.NextEnd, candidate.State, page.ServerTime.UTC())
	result, err := tx.ExecContext(ctx, `UPDATE momentum.candle_import_progress
	 SET revision=$7,next_end=$8,state=$9,last_server_time=$10
	 WHERE environment=$1 AND dataset_id=$2 AND symbol=$3 AND interval_code=$4
	 AND lower_open=$5 AND upper_open=$6`, args...)
	if err != nil {
		return next, candleFailure(ctx, nil)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 || ctx.Err() != nil {
		return next, candleFailure(ctx, nil)
	}
	return candidate, nil
}

// candleImportOldest проверяет непрерывный закрытый префикс невыполненного диапазона.
func candleImportOldest(key CandleImportWindow, end time.Time, step time.Duration, page model.CandlePage) (time.Time, bool) {
	if len(page.Candles) < 1 || len(page.Candles) > 200 || page.ServerTime.Before(key.UpperOpen) {
		return time.Time{}, false
	}
	open := end.Add(time.Millisecond - step)
	for _, candle := range page.Candles {
		if !candle.Confirmed || !candle.OpenTime.Equal(open) || open.Before(key.LowerOpen) {
			return time.Time{}, false
		}
		open = open.Add(-step)
	}
	return open.Add(step), true
}
