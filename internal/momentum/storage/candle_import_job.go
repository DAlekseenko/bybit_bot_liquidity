package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	// ErrCandleImportNotInserted означает, что точный запрос не добавил строку.
	ErrCandleImportNotInserted = errors.New("momentum candle import job not inserted")
	// ErrCandleImportInitUnknown означает, что итог попытки вставки не подтверждён.
	ErrCandleImportInitUnknown = errors.New("momentum candle import job insert outcome unknown")
	// ErrCandleImportCorrupt обозначает несогласованные метаданные сохранённого задания.
	ErrCandleImportCorrupt = errors.New("invalid stored momentum candle import job")
)

// StoredCandleImport хранит наблюдавшиеся метаданные точного задания.
type StoredCandleImport struct {
	Window         CandleImportWindow
	Checkpoint     CandleImportCheckpoint
	Reason         string
	LastServerTime *time.Time
}

// candleImportJobArgs проверяет тот же полный ключ, который использует продвижение.
func candleImportJobArgs(key CandleImportWindow) (step int64, args []any, err error) {
	step, err = candleKey(key.Stream)
	if err != nil || !candleTime(key.LowerOpen, step) || !candleTime(key.UpperOpen, step) ||
		!key.LowerOpen.Before(key.UpperOpen) {
		return 0, nil, ErrCandleArgument
	}
	stream := key.Stream
	return step, []any{stream.Environment, stream.DatasetID, stream.Symbol, stream.IntervalCode,
		key.LowerOpen.UTC(), key.UpperOpen.UTC()}, nil
}

// InitCandleImport пытается добавить pending задание без изменения существующей строки.
func InitCandleImport(ctx context.Context, db *sql.DB, key CandleImportWindow) error {
	if ctx == nil {
		return errors.Join(ErrCandles, ErrCandleArgument)
	}
	_, args, err := candleImportJobArgs(key)
	if err != nil || db == nil || ctx.Err() != nil {
		return candleFailure(ctx, ErrCandleArgument)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return candleFailure(ctx, nil)
	}
	defer conn.Close()

	args = append(args, key.UpperOpen.Add(-time.Millisecond).UTC())
	result, err := conn.ExecContext(ctx, `INSERT INTO momentum.candle_import_progress
	 (environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end)
	 SELECT $1,$2,$3,$4,$5::timestamptz,$6::timestamptz,$7::timestamptz FROM momentum.datasets
	 WHERE environment=$1 AND dataset_id=$2 AND source='bybit_v5' AND category='linear'
	 FOR SHARE
	 ON CONFLICT (environment,dataset_id,symbol,interval_code,lower_open,upper_open) DO NOTHING`, args...)
	if err != nil {
		return errors.Join(ErrCandles, ErrCandleImportInitUnknown, ctx.Err())
	}
	count, err := result.RowsAffected()
	if err != nil || (count != 0 && count != 1) {
		return errors.Join(ErrCandles, ErrCandleImportInitUnknown, ctx.Err())
	}
	if count == 0 {
		return errors.Join(ErrCandles, ErrCandleImportNotInserted)
	}
	return nil
}

// ReadCandleImport читает и проверяет только durable метаданные точного задания.
func ReadCandleImport(ctx context.Context, db *sql.DB, key CandleImportWindow) (StoredCandleImport, error) {
	var empty StoredCandleImport
	if ctx == nil {
		return empty, errors.Join(ErrCandles, ErrCandleArgument)
	}
	step, args, err := candleImportJobArgs(key)
	if err != nil || db == nil || ctx.Err() != nil {
		return empty, candleFailure(ctx, ErrCandleArgument)
	}
	var row StoredCandleImport
	var revision sql.NullInt64
	var state, reason sql.NullString
	var cursor, observed sql.NullTime
	var hasObservation, validDataset bool
	err = db.QueryRowContext(ctx, `SELECT p.revision,p.state,p.reason,
	 CASE WHEN pg_catalog.isfinite(p.next_end) THEN p.next_end END,
	 CASE WHEN pg_catalog.isfinite(p.last_server_time) THEN p.last_server_time END,
	 p.last_server_time IS NOT NULL, (d.source='bybit_v5' AND d.category='linear') IS TRUE
	 FROM momentum.candle_import_progress p
	 LEFT JOIN momentum.datasets d USING (environment,dataset_id)
	 WHERE p.environment=$1 AND p.dataset_id=$2 AND p.symbol=$3 AND p.interval_code=$4
	 AND p.lower_open=$5 AND p.upper_open=$6`, args...).Scan(&revision, &state, &reason, &cursor, &observed, &hasObservation, &validDataset)
	if ctx.Err() != nil {
		return empty, candleFailure(ctx, nil)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return empty, candleFailure(ctx, ErrCandleImportMissing)
	}
	if err != nil {
		return empty, candleFailure(ctx, nil)
	}
	if !revision.Valid || !state.Valid || !reason.Valid || !cursor.Valid || hasObservation != observed.Valid || !validDataset {
		return empty, candleFailure(ctx, ErrCandleImportCorrupt)
	}
	row.Window = key
	row.Window.LowerOpen, row.Window.UpperOpen = key.LowerOpen.UTC(), key.UpperOpen.UTC()
	row.Checkpoint = CandleImportCheckpoint{Revision: revision.Int64, NextEnd: cursor.Time.UTC(), State: state.String}
	row.Reason = reason.String
	if observed.Valid {
		stamp := observed.Time.UTC()
		row.LastServerTime = &stamp
	}
	if !validStoredCandleImport(row, step) {
		return empty, candleFailure(ctx, ErrCandleImportCorrupt)
	}
	if ctx.Err() != nil {
		return empty, candleFailure(ctx, nil)
	}
	return row, nil
}

// validStoredCandleImport проверяет локальные инварианты v4 без вывода об истории строк.
func validStoredCandleImport(row StoredCandleImport, step int64) bool {
	lower, upper := row.Window.LowerOpen, row.Window.UpperOpen
	firstEnd, lastEnd := upper.Add(-time.Millisecond), lower.Add(-time.Millisecond)
	checkpoint := row.Checkpoint
	end, observed := checkpoint.NextEnd, row.LastServerTime
	if checkpoint.Revision < 0 || end.Before(lastEnd) || !end.Before(upper) ||
		!candleTime(end.Add(time.Millisecond), step) || (observed != nil && !candleTime(*observed, 1)) {
		return false
	}
	closedCutoff := observed != nil && !observed.Before(upper)
	switch checkpoint.State {
	case "pending":
		return checkpoint.Revision == 0 && row.Reason == "" && end.Equal(firstEnd) && observed == nil
	case "progress":
		return checkpoint.Revision > 0 && row.Reason == "" && closedCutoff && !end.Before(lower) && end.Before(firstEnd)
	case "complete":
		return checkpoint.Revision > 0 && row.Reason == "" && closedCutoff && end.Equal(lastEnd)
	case "blocked":
		if checkpoint.Revision == 0 {
			return false
		}
		switch row.Reason {
		case "source_boundary":
			return lower.UnixMilli() == 0 && end.Equal(firstEnd) && observed == nil
		case "empty", "gap":
			return closedCutoff && !end.Before(lower)
		case "unconfirmed":
			return observed != nil && !end.Before(lower)
		}
	}
	return false
}
