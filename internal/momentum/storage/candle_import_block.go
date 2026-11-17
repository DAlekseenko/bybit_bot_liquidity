package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// CandleImportBlockReason описывает причину остановки без продвижения окна.
type CandleImportBlockReason string

const (
	CandleImportBlockEmpty          CandleImportBlockReason = "empty"
	CandleImportBlockGap            CandleImportBlockReason = "gap"
	CandleImportBlockUnconfirmed    CandleImportBlockReason = "unconfirmed"
	CandleImportBlockSourceBoundary CandleImportBlockReason = "source_boundary"
)

// CandleImportPageObservation связывает результат читающей операции с её фактическими аргументами.
type CandleImportPageObservation struct {
	Symbol, IntervalCode string
	Start, End           time.Time
	Page                 model.CandlePage
}

// BlockCandleImport сохраняет доказанное blocked состояние; Commit выполняет вызывающий код.
func BlockCandleImport(ctx context.Context, tx *sql.Tx, key CandleImportWindow, expected CandleImportCheckpoint, reason CandleImportBlockReason, observation *CandleImportPageObservation) (next CandleImportCheckpoint, err error) {
	if tx == nil {
		return next, candleFailure(ctx, ErrCandleTx)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	current, err := lockCandleImportCheckpoint(ctx, tx, key, expected)
	if err != nil {
		return next, err
	}
	sourceTime, ok := candleImportBlockObservation(key, current, reason, observation)
	if !ok {
		return next, candleFailure(ctx, ErrCandleArgument)
	}
	var source, category string
	err = tx.QueryRowContext(ctx, `SELECT source,category FROM momentum.datasets
	 WHERE environment=$1 AND dataset_id=$2 FOR SHARE`, key.Stream.Environment, key.Stream.DatasetID).Scan(&source, &category)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (source != "bybit_v5" || category != "linear")) {
		return next, candleFailure(ctx, ErrCandleDataset)
	}
	if err != nil {
		return next, candleFailure(ctx, nil)
	}
	var observed any
	if !sourceTime.IsZero() {
		observed = sourceTime.UTC()
	}
	candidate := CandleImportCheckpoint{Revision: current.Revision + 1, NextEnd: current.NextEnd.UTC(), State: "blocked"}
	args := append(current.args, candidate.Revision, string(reason), observed)
	result, err := tx.ExecContext(ctx, `UPDATE momentum.candle_import_progress
	 SET revision=$7,state='blocked',reason=$8,last_server_time=$9
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

// candleImportBlockObservation сверяет заявленную причину с источником или точной границей эпохи.
func candleImportBlockObservation(key CandleImportWindow, current lockedCandleImport, reason CandleImportBlockReason, observation *CandleImportPageObservation) (time.Time, bool) {
	observed, known, valid := classifyCandleImportObservation(key, current, observation)
	return observed, valid && known != "" && reason == known
}

// ApplyCandleImportObservation выбирает и применяет один переход по проверенному наблюдению.
func ApplyCandleImportObservation(ctx context.Context, tx *sql.Tx, key CandleImportWindow, expected CandleImportCheckpoint, observation *CandleImportPageObservation) (next CandleImportCheckpoint, reason CandleImportBlockReason, err error) {
	if tx == nil {
		if ctx == nil {
			ctx = context.Background()
		}
		return next, reason, candleFailure(ctx, ErrCandleTx)
	}
	if ctx == nil {
		_ = tx.Rollback()
		return next, reason, candleFailure(context.Background(), ErrCandleArgument)
	}
	step, keyErr := candleKey(key.Stream)
	if keyErr != nil {
		_, err = BlockCandleImport(ctx, tx, key, expected, "", observation)
		return next, reason, err
	}
	current := lockedCandleImport{CandleImportCheckpoint: expected, width: time.Duration(step) * time.Millisecond}
	_, reason, valid := classifyCandleImportObservation(key, current, observation)
	if !valid {
		_, err = BlockCandleImport(ctx, tx, key, expected, "", observation)
		return next, "", err
	}
	if reason == "" {
		advanced, err := AdvanceCandleImportPage(ctx, tx, key, expected, observation.Page)
		if err != nil {
			return CandleImportCheckpoint{}, "", err
		}
		return advanced, "", nil
	}
	blocked, err := BlockCandleImport(ctx, tx, key, expected, reason, observation)
	if err != nil {
		return CandleImportCheckpoint{}, "", err
	}
	return blocked, reason, nil
}

// classifyCandleImportObservation проверяет evidence и выводит единственный переход.
func classifyCandleImportObservation(key CandleImportWindow, current lockedCandleImport, observation *CandleImportPageObservation) (time.Time, CandleImportBlockReason, bool) {
	if observation == nil {
		validEpoch := key.LowerOpen.UnixMilli() == 0 && current.State == "pending" &&
			current.NextEnd.Equal(key.UpperOpen.Add(-time.Millisecond))
		if validEpoch {
			return time.Time{}, CandleImportBlockSourceBoundary, true
		}
		return time.Time{}, "", false
	}
	if observation.Symbol != key.Stream.Symbol || observation.IntervalCode != key.Stream.IntervalCode ||
		!observation.Start.Equal(key.LowerOpen) || !observation.End.Equal(current.NextEnd) || key.LowerOpen.UnixMilli() == 0 {
		return time.Time{}, "", false
	}
	page := observation.Page
	if page.Candles == nil || len(page.Candles) > 200 || page.Environment != key.Stream.Environment ||
		page.Source != "bybit_v5" || page.Category != "linear" || !candlePageOrigin(page) ||
		!candleTime(page.ServerTime, 1) || page.ServerTime.UnixMilli() == 0 {
		return time.Time{}, "", false
	}
	expected := current.NextEnd.Add(time.Millisecond - current.width)
	var gap bool
	for i, row := range page.Candles {
		open := row.OpenTime
		if row.Symbol != key.Stream.Symbol || row.IntervalCode != key.Stream.IntervalCode ||
			!candleTime(open, current.width.Milliseconds()) || open.Before(key.LowerOpen) ||
			open.After(current.NextEnd) || open.After(page.ServerTime) ||
			(i > 0 && !open.Before(page.Candles[i-1].OpenTime)) ||
			row.Confirmed != !page.ServerTime.Before(open.Add(current.width)) {
			return time.Time{}, "", false
		}
		if _, err := candleWriteValues(row); err != nil {
			return time.Time{}, "", false
		}
		gap = gap || !open.Equal(expected)
		expected = open.Add(-current.width)
	}
	switch {
	case page.ServerTime.Before(key.UpperOpen):
		return page.ServerTime, CandleImportBlockUnconfirmed, true
	case len(page.Candles) == 0:
		return page.ServerTime, CandleImportBlockEmpty, true
	case gap:
		return page.ServerTime, CandleImportBlockGap, true
	default:
		return page.ServerTime, "", true
	}
}
