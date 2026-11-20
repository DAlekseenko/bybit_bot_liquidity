package history

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/storage"
)

var (
	// ErrHistoryCapture обозначает неполученный полный read-only capture.
	ErrHistoryCapture = errors.New("momentum history capture failed")
	// ErrHistoryCaptureArgument обозначает отсутствующий snapshot или неверное окно/universe.
	ErrHistoryCaptureArgument = errors.New("invalid momentum history capture argument")
)

// CapturedSymbol содержит пять исходных потоков без заполнения gaps и оценки готовности.
type CapturedSymbol struct {
	Symbol                  string
	TradeDaily, TradeHourly []storage.StoredCandle
	MarkDaily, MarkHourly   []model.MarkPriceCandle
	Funding                 []model.FundingEvent
}

type captureReaders struct {
	trade   func(context.Context, *sql.Tx, storage.CandleStream, time.Time, time.Time, int) ([]storage.StoredCandle, error)
	mark    func(context.Context, *sql.Tx, storage.CandleStream, time.Time, time.Time, int) ([]model.MarkPriceCandle, error)
	funding func(context.Context, *sql.Tx, storage.FundingStream, time.Time, time.Time, int) ([]model.FundingEvent, error)
}

// CaptureHistoryTx материализует configured streams в caller-owned RR READ ONLY, не завершая её.
func CaptureHistoryTx(ctx context.Context, tx *sql.Tx, environment, datasetID string, symbols []string, start, end time.Time) ([]CapturedSymbol, error) {
	return captureHistoryTx(ctx, tx, environment, datasetID, symbols, start, end, captureReaders{storage.ReadCandleRangeTx, storage.ReadMarkPriceRangeTx, storage.ReadFundingRangeTx})
}

// captureHistoryTx использует прежний bounded D/60 window loop и один funding range на символ.
func captureHistoryTx(ctx context.Context, tx *sql.Tx, environment, datasetID string, symbols []string, start, end time.Time, read captureReaders) ([]CapturedSymbol, error) {
	universe, err := config.CanonicalSymbols(symbols)
	if ctx == nil || tx == nil || err != nil || !start.Before(end) || !captureDay(start) || !captureDay(end) {
		return nil, errors.Join(ErrHistoryCapture, ErrHistoryCaptureArgument)
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(ErrHistoryCapture, err)
	}
	result := make([]CapturedSymbol, 0, len(universe))
	for _, symbol := range universe {
		current := CapturedSymbol{Symbol: symbol, TradeDaily: make([]storage.StoredCandle, 0), TradeHourly: make([]storage.StoredCandle, 0), MarkDaily: make([]model.MarkPriceCandle, 0), MarkHourly: make([]model.MarkPriceCandle, 0)}
		for _, interval := range []string{"D", "60"} {
			step := time.Hour
			if interval == "D" {
				step = 24 * time.Hour
			}
			key := storage.CandleStream{Environment: environment, DatasetID: datasetID, Symbol: symbol, IntervalCode: interval}
			for at := start.UTC(); at.Before(end); {
				next := at.Add(200 * step)
				if next.After(end) {
					next = end.UTC()
				}
				rows, err := read.trade(ctx, tx, key, at, next, 200)
				if err != nil {
					return nil, errors.Join(ErrHistoryCapture, err)
				}
				marks, err := read.mark(ctx, tx, key, at, next, 200)
				if err != nil {
					return nil, errors.Join(ErrHistoryCapture, err)
				}
				if interval == "D" {
					current.TradeDaily = append(current.TradeDaily, rows...)
					current.MarkDaily = append(current.MarkDaily, marks...)
				} else {
					current.TradeHourly = append(current.TradeHourly, rows...)
					current.MarkHourly = append(current.MarkHourly, marks...)
				}
				at = next
			}
		}
		events, err := read.funding(ctx, tx, storage.FundingStream{Environment: environment, DatasetID: datasetID, Symbol: symbol}, start.UTC(), end.UTC().Add(-time.Millisecond), 200)
		if err != nil {
			return nil, errors.Join(ErrHistoryCapture, err)
		}
		current.Funding = append(make([]model.FundingEvent, 0, len(events)), events...)
		result = append(result, current)
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(ErrHistoryCapture, err)
	}
	return result, nil
}

// captureDay наследует exact UTC daily bounds существующего trade reader.
func captureDay(t time.Time) bool {
	ms := t.UnixMilli()
	return ms >= 0 && t.UTC().Year() <= 294276 && time.UnixMilli(ms).Equal(t) && ms%86400000 == 0
}
