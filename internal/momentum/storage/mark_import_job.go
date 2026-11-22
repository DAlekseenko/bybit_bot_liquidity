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
	// ErrMarkImportNotInserted означает, что задание с этим ключом уже существует.
	ErrMarkImportNotInserted = errors.New("momentum mark import job not inserted")
	// ErrMarkImportInitUnknown означает, что итог попытки вставки не подтверждён.
	ErrMarkImportInitUnknown = errors.New("momentum mark import job insert outcome unknown")
	// ErrMarkImportMissing означает отсутствие задания с точным ключом.
	ErrMarkImportMissing = errors.New("momentum mark import job is missing")
	// ErrMarkImportStale означает, что сохранённый курсор отличается от ожидаемого.
	ErrMarkImportStale = errors.New("momentum mark import checkpoint changed")
	// ErrMarkImportState означает терминальное состояние, которое нельзя продвинуть.
	ErrMarkImportState = errors.New("momentum mark import checkpoint cannot advance")
	// ErrMarkImportCorrupt обозначает несогласованные метаданные сохранённого задания.
	ErrMarkImportCorrupt = errors.New("invalid stored momentum mark import job")
)

// MarkImportWindow задаёт ряд mark и неизменяемые границы задания [LowerOpen, UpperOpen).
type MarkImportWindow struct {
	Stream               MarkPriceStream
	LowerOpen, UpperOpen time.Time
}

// StoredMarkImport хранит наблюдавшиеся метаданные точного задания импорта mark.
type StoredMarkImport struct {
	Window         MarkImportWindow
	Checkpoint     CandleImportCheckpoint
	Reason         string
	LastServerTime *time.Time
}

// candle приводит окно mark к общему ключу проверки границ и курсора.
func (key MarkImportWindow) candle() CandleImportWindow {
	return CandleImportWindow{CandleStream(key.Stream), key.LowerOpen, key.UpperOpen}
}

// InitMarkImport добавляет pending задание; существующую строку не меняет.
func InitMarkImport(ctx context.Context, db *sql.DB, key MarkImportWindow) error {
	if ctx == nil {
		return errors.Join(ErrMarkPrices, ErrMarkPriceArgument)
	}
	_, args, err := candleImportJobArgs(key.candle())
	if err != nil || db == nil || ctx.Err() != nil {
		return markPriceFailure(ctx, ErrMarkPriceArgument)
	}
	args = append(args, key.UpperOpen.Add(-time.Millisecond).UTC())
	result, err := db.ExecContext(ctx, `INSERT INTO momentum.mark_import_progress
	 (environment,dataset_id,symbol,interval_code,lower_open,upper_open,next_end)
	 SELECT $1,$2,$3,$4,$5::timestamptz,$6::timestamptz,$7::timestamptz FROM momentum.datasets
	 WHERE environment=$1 AND dataset_id=$2 AND source='bybit_v5' AND category='linear'
	 FOR SHARE
	 ON CONFLICT (environment,dataset_id,symbol,interval_code,lower_open,upper_open) DO NOTHING`, args...)
	if err != nil {
		return errors.Join(ErrMarkPrices, ErrMarkImportInitUnknown, ctx.Err())
	}
	count, err := result.RowsAffected()
	if err != nil || (count != 0 && count != 1) {
		return errors.Join(ErrMarkPrices, ErrMarkImportInitUnknown, ctx.Err())
	}
	if count == 0 {
		return errors.Join(ErrMarkPrices, ErrMarkImportNotInserted)
	}
	return nil
}

// ReadMarkImport читает и проверяет durable метаданные точного задания для рестарта.
func ReadMarkImport(ctx context.Context, db *sql.DB, key MarkImportWindow) (StoredMarkImport, error) {
	if ctx == nil {
		return StoredMarkImport{}, errors.Join(ErrMarkPrices, ErrMarkPriceArgument)
	}
	step, args, err := candleImportJobArgs(key.candle())
	if err != nil || db == nil || ctx.Err() != nil {
		return StoredMarkImport{}, markPriceFailure(ctx, ErrMarkPriceArgument)
	}
	var row StoredCandleImport
	var observed sql.NullTime
	err = db.QueryRowContext(ctx, `SELECT p.revision,p.state,p.reason,p.next_end,p.last_server_time
	 FROM momentum.mark_import_progress p JOIN momentum.datasets d USING (environment,dataset_id)
	 WHERE p.environment=$1 AND p.dataset_id=$2 AND p.symbol=$3 AND p.interval_code=$4
	 AND p.lower_open=$5 AND p.upper_open=$6 AND d.source='bybit_v5' AND d.category='linear'`, args...).Scan(
		&row.Checkpoint.Revision, &row.Checkpoint.State, &row.Reason, &row.Checkpoint.NextEnd, &observed)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredMarkImport{}, markPriceFailure(ctx, ErrMarkImportMissing)
	}
	if err != nil {
		return StoredMarkImport{}, markPriceFailure(ctx, nil)
	}
	row.Window = key.candle()
	row.Window.LowerOpen, row.Window.UpperOpen = key.LowerOpen.UTC(), key.UpperOpen.UTC()
	row.Checkpoint.NextEnd = row.Checkpoint.NextEnd.UTC()
	if observed.Valid {
		stamp := observed.Time.UTC()
		row.LastServerTime = &stamp
	}
	if !validStoredCandleImport(row, step) {
		return StoredMarkImport{}, markPriceFailure(ctx, ErrMarkImportCorrupt)
	}
	return StoredMarkImport{MarkImportWindow{key.Stream, row.Window.LowerOpen, row.Window.UpperOpen},
		row.Checkpoint, row.Reason, row.LastServerTime}, nil
}

// ApplyMarkImportObservation по проверенной странице выполняет один переход:
// продвижение курсора с записью mark или остановку с причиной. page == nil
// допустим только для эпохальной границы источника. Commit выполняет вызывающий код.
func ApplyMarkImportObservation(ctx context.Context, tx *sql.Tx, key MarkImportWindow, expected CandleImportCheckpoint,
	page *model.MarkPricePage) (next CandleImportCheckpoint, reason CandleImportBlockReason, err error) {
	if tx == nil || ctx == nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		return next, "", errors.Join(ErrMarkPrices, ErrMarkPriceTx)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	step, args, err := candleImportJobArgs(key.candle())
	if err != nil {
		return next, "", markPriceFailure(ctx, ErrMarkPriceArgument)
	}
	if err := lockMarkImport(ctx, tx, args, expected); err != nil {
		return next, "", err
	}
	oldest, reason, ok := classifyMarkImportPage(key, expected, time.Duration(step)*time.Millisecond, page)
	if !ok {
		return next, "", markPriceFailure(ctx, ErrMarkPriceArgument)
	}
	next = CandleImportCheckpoint{Revision: expected.Revision + 1, NextEnd: expected.NextEnd.UTC(), State: "blocked"}
	var observed any
	if page != nil {
		observed = page.ServerTime.UTC()
	}
	if reason == "" {
		if err := AppendMarkPricePage(ctx, tx, key.Stream, *page); err != nil {
			return CandleImportCheckpoint{}, "", err
		}
		next.NextEnd, next.State = oldest.Add(-time.Millisecond).UTC(), "progress"
		if oldest.Equal(key.LowerOpen) {
			next.State = "complete"
		}
	}
	args = append(args, next.Revision, next.NextEnd, next.State, string(reason), observed)
	result, err := tx.ExecContext(ctx, `UPDATE momentum.mark_import_progress
	 SET revision=$7,next_end=$8,state=$9,reason=$10,last_server_time=$11
	 WHERE environment=$1 AND dataset_id=$2 AND symbol=$3 AND interval_code=$4
	 AND lower_open=$5 AND upper_open=$6`, args...)
	if err != nil {
		return CandleImportCheckpoint{}, "", markPriceFailure(ctx, nil)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 || ctx.Err() != nil {
		return CandleImportCheckpoint{}, "", markPriceFailure(ctx, nil)
	}
	return next, reason, nil
}

// lockMarkImport блокирует точное задание и сверяет ожидаемый курсор и набор данных.
func lockMarkImport(ctx context.Context, tx *sql.Tx, args []any, expected CandleImportCheckpoint) error {
	var readCommitted, validDataset bool
	err := tx.QueryRowContext(ctx, `SELECT pg_catalog.current_setting('transaction_isolation') = 'read committed'
	 AND pg_catalog.current_setting('transaction_read_only') = 'off'`).Scan(&readCommitted)
	if err != nil {
		return markPriceFailure(ctx, nil)
	}
	if !readCommitted {
		return markPriceFailure(ctx, ErrMarkPriceTx)
	}
	var current CandleImportCheckpoint
	err = tx.QueryRowContext(ctx, `SELECT p.revision,p.next_end,p.state,(d.source='bybit_v5' AND d.category='linear')
	 FROM momentum.mark_import_progress p JOIN momentum.datasets d USING (environment,dataset_id)
	 WHERE p.environment=$1 AND p.dataset_id=$2 AND p.symbol=$3 AND p.interval_code=$4
	 AND p.lower_open=$5 AND p.upper_open=$6 FOR UPDATE OF p FOR SHARE OF d`, args...).Scan(
		&current.Revision, &current.NextEnd, &current.State, &validDataset)
	if errors.Is(err, sql.ErrNoRows) {
		return markPriceFailure(ctx, ErrMarkImportMissing)
	}
	if err != nil {
		return markPriceFailure(ctx, nil)
	}
	if !validDataset {
		return markPriceFailure(ctx, ErrMarkPriceDataset)
	}
	if current.Revision != expected.Revision || current.State != expected.State || !current.NextEnd.Equal(expected.NextEnd) {
		return markPriceFailure(ctx, ErrMarkImportStale)
	}
	if (current.State != "pending" && current.State != "progress") || current.Revision == math.MaxInt64 {
		return markPriceFailure(ctx, ErrMarkImportState)
	}
	return nil
}

// classifyMarkImportPage проверяет происхождение страницы и выводит единственный
// переход: "" — непрерывный закрытый префикс с самым старым open, иначе причина.
func classifyMarkImportPage(key MarkImportWindow, cursor CandleImportCheckpoint, width time.Duration,
	page *model.MarkPricePage) (time.Time, CandleImportBlockReason, bool) {
	if page == nil {
		epoch := key.LowerOpen.UnixMilli() == 0 && cursor.State == "pending" &&
			cursor.NextEnd.Equal(key.UpperOpen.Add(-time.Millisecond))
		return time.Time{}, CandleImportBlockSourceBoundary, epoch
	}
	endpoint := "https://api.bybit.com/v5/market/mark-price-kline"
	if key.Stream.Environment == "testnet" {
		endpoint = "https://api-testnet.bybit.com/v5/market/mark-price-kline"
	}
	if page.Candles == nil || len(page.Candles) > 200 || page.Environment != key.Stream.Environment ||
		page.Symbol != key.Stream.Symbol || page.IntervalCode != key.Stream.IntervalCode || page.Source != "bybit_v5" ||
		page.Category != "linear" || page.Endpoint != endpoint || key.LowerOpen.UnixMilli() == 0 ||
		!page.StartTime.Equal(key.LowerOpen) || !page.EndTime.Equal(cursor.NextEnd) || !markPriceMillis(page.ServerTime) {
		return time.Time{}, "", false
	}
	want := cursor.NextEnd.Add(time.Millisecond - width)
	gap := false
	for i, row := range page.Candles {
		open := row.OpenTime
		if !candleTime(open, width.Milliseconds()) || open.Before(key.LowerOpen) || open.After(cursor.NextEnd) ||
			open.After(page.ServerTime) || (i > 0 && !open.Before(page.Candles[i-1].OpenTime)) ||
			row.Confirmed != !page.ServerTime.Before(open.Add(width)) {
			return time.Time{}, "", false
		}
		if _, err := markPriceWriteValues(row); err != nil {
			return time.Time{}, "", false
		}
		gap = gap || !open.Equal(want)
		want = open.Add(-width)
	}
	switch {
	case page.ServerTime.Before(key.UpperOpen):
		return time.Time{}, CandleImportBlockUnconfirmed, true
	case len(page.Candles) == 0:
		return time.Time{}, CandleImportBlockEmpty, true
	case gap:
		return time.Time{}, CandleImportBlockGap, true
	}
	return want.Add(width), "", true
}
