package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	// ErrFundingImportNotInserted означает подтверждённое отсутствие вставки точным вызовом.
	ErrFundingImportNotInserted = errors.New("momentum funding import job not inserted")
	// ErrFundingImportInitUnknown означает неподтверждённый исход попытки вставки.
	ErrFundingImportInitUnknown = errors.New("momentum funding import job insert outcome unknown")
	// ErrFundingImportMissing отличает отсутствие точного durable задания.
	ErrFundingImportMissing = errors.New("momentum funding import job absent")
	// ErrFundingImportCorrupt обозначает несогласованные сохранённые метаданные.
	ErrFundingImportCorrupt = errors.New("invalid stored momentum funding import job")
)

// FundingImportWindow задаёт точный namespace и включительные неподвижные границы задания.
type FundingImportWindow struct {
	Stream               FundingStream
	LowerTime, UpperTime time.Time
}

// StoredFundingImport хранит durable metadata; pending не является решением по странице.
type StoredFundingImport struct {
	Window                                                  FundingImportWindow
	Revision                                                int64
	Action                                                  model.FundingPageAction
	NextEnd, LastRequestEnd, LastServerTime, LastPageOldest *time.Time
	LastPageCount                                           *int
}

// fundingImportJobArgs проверяет полный ключ #304 без cadence или нормализации opaque ID.
func fundingImportJobArgs(key FundingImportWindow) ([]any, error) {
	if !validFundingStream(key.Stream) || !candleTime(key.LowerTime, 1) || !candleTime(key.UpperTime, 1) ||
		key.UpperTime.UnixMilli() <= 0 || key.UpperTime.Before(key.LowerTime) {
		return nil, ErrFundingArgument
	}
	return []any{key.Stream.Environment, key.Stream.DatasetID, key.Stream.Symbol, key.LowerTime.UTC(), key.UpperTime.UTC()}, nil
}

// InitFundingImport пытается добавить pending, никогда не сбрасывая существующий progress.
func InitFundingImport(ctx context.Context, db *sql.DB, key FundingImportWindow) error {
	if ctx == nil {
		return fundingFailure(ctx, ErrFundingArgument)
	}
	if ctx.Err() != nil {
		return fundingFailure(ctx, nil)
	}
	args, err := fundingImportJobArgs(key)
	if err != nil || db == nil {
		return fundingFailure(ctx, ErrFundingArgument)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fundingFailure(ctx, nil)
	}
	defer conn.Close()
	result, err := conn.ExecContext(ctx, `INSERT INTO momentum.funding_import_progress
 (environment,dataset_id,symbol,lower_time,upper_time,next_end)
 SELECT $1,$2,$3,$4::timestamptz,$5::timestamptz,$5::timestamptz FROM momentum.datasets
 WHERE environment=$1 AND dataset_id=$2 AND source='bybit_v5' AND category='linear'
 FOR SHARE ON CONFLICT (environment,dataset_id,symbol,lower_time,upper_time) DO NOTHING`, args...)
	if err != nil {
		return errors.Join(ErrFunding, ErrFundingImportInitUnknown, ctx.Err())
	}
	count, err := result.RowsAffected()
	if err != nil || count < 0 || count > 1 {
		return errors.Join(ErrFunding, ErrFundingImportInitUnknown, ctx.Err())
	}
	if count == 0 {
		return fundingFailure(ctx, ErrFundingImportNotInserted)
	}
	return nil
}

// ReadFundingImport читает и проверяет только metadata одного durable задания.
func ReadFundingImport(ctx context.Context, db *sql.DB, key FundingImportWindow) (StoredFundingImport, error) {
	var empty StoredFundingImport
	if ctx == nil {
		return empty, fundingFailure(ctx, ErrFundingArgument)
	}
	if ctx.Err() != nil {
		return empty, fundingFailure(ctx, nil)
	}
	args, err := fundingImportJobArgs(key)
	if err != nil || db == nil {
		return empty, fundingFailure(ctx, ErrFundingArgument)
	}
	var revision, count sql.NullInt64
	var action sql.NullString
	var times [4]sql.NullTime
	var present [4]bool
	var validDataset bool
	err = db.QueryRowContext(ctx, `SELECT p.revision,p.action,
 CASE WHEN isfinite(p.next_end) THEN p.next_end END,p.next_end IS NOT NULL,
 CASE WHEN isfinite(p.last_request_end) THEN p.last_request_end END,p.last_request_end IS NOT NULL,
 CASE WHEN isfinite(p.last_server_time) THEN p.last_server_time END,p.last_server_time IS NOT NULL,
 CASE WHEN isfinite(p.last_page_oldest) THEN p.last_page_oldest END,p.last_page_oldest IS NOT NULL,
 p.last_page_count,(d.source='bybit_v5' AND d.category='linear') IS TRUE
 FROM momentum.funding_import_progress p LEFT JOIN momentum.datasets d USING(environment,dataset_id)
 WHERE p.environment=$1 AND p.dataset_id=$2 AND p.symbol=$3 AND p.lower_time=$4 AND p.upper_time=$5`, args...).Scan(
		&revision, &action, &times[0], &present[0], &times[1], &present[1], &times[2], &present[2], &times[3], &present[3], &count, &validDataset)
	if ctx.Err() != nil {
		return empty, fundingFailure(ctx, nil)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return empty, fundingFailure(ctx, ErrFundingImportMissing)
	}
	if err != nil {
		return empty, fundingFailure(ctx, nil)
	}
	if !revision.Valid || !action.Valid || !validDataset || count.Valid && (count.Int64 < 0 || count.Int64 > 200) {
		return empty, fundingFailure(ctx, ErrFundingImportCorrupt)
	}
	row := StoredFundingImport{Window: key, Revision: revision.Int64, Action: model.FundingPageAction(action.String)}
	row.Window.LowerTime, row.Window.UpperTime = key.LowerTime.UTC(), key.UpperTime.UTC()
	destinations := []**time.Time{&row.NextEnd, &row.LastRequestEnd, &row.LastServerTime, &row.LastPageOldest}
	for i, stamp := range times {
		if present[i] != stamp.Valid {
			return empty, fundingFailure(ctx, ErrFundingImportCorrupt)
		}
		if stamp.Valid {
			value := stamp.Time.UTC()
			*destinations[i] = &value
		}
	}
	if count.Valid {
		value := int(count.Int64)
		row.LastPageCount = &value
	}
	if !validStoredFundingImport(row) {
		return empty, fundingFailure(ctx, ErrFundingImportCorrupt)
	}
	return row, nil
}

// validStoredFundingImport проверяет форму #304, не выводя полноту funding history.
func validStoredFundingImport(row StoredFundingImport) bool {
	if _, err := fundingImportJobArgs(row.Window); err != nil || row.Revision < 0 {
		return false
	}
	lower, upper := row.Window.LowerTime, row.Window.UpperTime
	for _, stamp := range []*time.Time{row.NextEnd, row.LastRequestEnd, row.LastServerTime, row.LastPageOldest} {
		if stamp != nil && !candleTime(*stamp, 1) {
			return false
		}
	}
	epoch := time.UnixMilli(0)
	for _, stamp := range []*time.Time{row.NextEnd, row.LastRequestEnd} {
		if stamp != nil && (!stamp.After(epoch) || stamp.Before(lower) || stamp.After(upper)) {
			return false
		}
	}
	if row.Action == "pending" {
		return row.Revision == 0 && row.NextEnd != nil && row.NextEnd.Equal(upper) &&
			row.LastRequestEnd == nil && row.LastServerTime == nil && row.LastPageOldest == nil && row.LastPageCount == nil
	}
	if row.Revision == 0 || row.LastRequestEnd == nil || row.LastServerTime == nil || !row.LastServerTime.After(epoch) ||
		row.LastPageCount == nil || *row.LastPageCount < 0 || *row.LastPageCount > 200 {
		return false
	}
	if row.LastPageOldest != nil && (row.LastPageOldest.Before(lower) || row.LastPageOldest.After(*row.LastRequestEnd) || row.LastPageOldest.After(*row.LastServerTime)) {
		return false
	}
	switch row.Action {
	case model.FundingPageContinue:
		return *row.LastPageCount > 0 && row.LastPageOldest != nil && row.NextEnd != nil &&
			row.NextEnd.Equal(row.LastPageOldest.Add(-time.Millisecond)) && row.NextEnd.Before(*row.LastRequestEnd)
	case model.FundingPageLowerReached:
		return *row.LastPageCount > 0 && row.LastPageOldest != nil && row.LastPageOldest.Equal(lower) && row.NextEnd == nil
	case model.FundingPageEmpty:
		return *row.LastPageCount == 0 && row.LastPageOldest == nil && row.NextEnd == nil
	case model.FundingPageSourceBoundary:
		return lower.Equal(epoch) && *row.LastPageCount > 0 && row.LastPageOldest != nil && row.LastPageOldest.Equal(epoch.Add(time.Millisecond)) && row.NextEnd == nil
	}
	return false
}
