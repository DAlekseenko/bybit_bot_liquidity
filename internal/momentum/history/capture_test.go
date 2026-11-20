package history

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/storage"
)

var captureSymbols = []string{"HYPEUSDT", "BTCUSDT", "ETHUSDT"}
var captureStart = time.UnixMilli(1749168000000).UTC()
var captureEnd = time.UnixMilli(1753056000000).UTC()

// captureOK проверяет инвариант, не скрывая ни одного отказа.
func captureOK(t *testing.T, ok bool) {
	t.Helper()
	if !ok {
		t.Fatal("capture invariant failed")
	}
}

// captureFailure требует nil вместо частичных потоков.
func captureFailure(t *testing.T, got []CapturedSymbol, err, want error) {
	t.Helper()
	captureOK(t, got == nil && errors.Is(err, ErrHistoryCapture) && errors.Is(err, want))
}

// captureTx создаёт только настоящий caller-owned RR RO snapshot.
func captureTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, e := db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	candleImportTestOK(t, e)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// TestCaptureHistoryLoop проверяет45 вызовов, bounds, no partial и точную передачу строк.
func TestCaptureHistoryLoop(t *testing.T) {
	tx := new(sql.Tx)
	failure := errors.New("source failure")
	for failAt := 0; failAt <= 46; failAt++ {
		calls := 0
		wantCtx, cancel := context.WithCancel(t.Context())
		call := func(ctx context.Context, actual *sql.Tx, kind string, key storage.CandleStream, start, end time.Time, limit int) error {
			calls++
			position := (calls - 1) % 15
			wantKind, interval, chunk := "funding", "", 0
			wantStart, wantEnd := captureStart, captureEnd.Add(-time.Millisecond)
			if position < 14 {
				var step time.Duration
				wantKind = []string{"trade", "mark"}[position%2]
				interval = "D"
				step = 24 * time.Hour
				if position >= 2 {
					interval, chunk, step = "60", (position-2)/2, time.Hour
				}
				wantStart = captureStart.Add(time.Duration(chunk) * 200 * step)
				wantEnd = wantStart.Add(200 * step)
				if wantEnd.After(captureEnd) {
					wantEnd = captureEnd
				}
			}
			wantKey := storage.CandleStream{Environment: "mainnet", DatasetID: "exact namespace", Symbol: []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}[(calls-1)/15], IntervalCode: interval}
			captureOK(t, ctx == wantCtx && actual == tx && kind == wantKind && key == wantKey && limit == 200 && start.Equal(wantStart) && end.Equal(wantEnd))
			if failAt == 46 && calls == 45 {
				cancel()
			}
			if calls == failAt {
				return failure
			}
			return nil
		}
		price, e := model.ParsePrice("9007199254740993.00000000000000000001")
		candleImportTestOK(t, e)
		row := storage.StoredCandle{OpenTime: captureStart, Close: price, Confirmed: false}
		readers := captureReaders{
			trade: func(ctx context.Context, actual *sql.Tx, key storage.CandleStream, start, end time.Time, limit int) ([]storage.StoredCandle, error) {
				if e := call(ctx, actual, "trade", key, start, end, limit); e != nil {
					return nil, e
				}
				return []storage.StoredCandle{row}, nil
			},
			mark: func(ctx context.Context, actual *sql.Tx, key storage.CandleStream, start, end time.Time, limit int) ([]model.MarkPriceCandle, error) {
				if e := call(ctx, actual, "mark", key, start, end, limit); e != nil {
					return nil, e
				}
				return make([]model.MarkPriceCandle, 0), nil
			},
			funding: func(ctx context.Context, actual *sql.Tx, key storage.FundingStream, start, end time.Time, limit int) ([]model.FundingEvent, error) {
				captureOK(t, start.Equal(captureStart) && end.Equal(captureEnd.Add(-time.Millisecond)))
				if e := call(ctx, actual, "funding", storage.CandleStream{Environment: key.Environment, DatasetID: key.DatasetID, Symbol: key.Symbol}, start, end, limit); e != nil {
					return nil, e
				}
				return make([]model.FundingEvent, 0), nil
			},
		}
		got, e := captureHistoryTx(wantCtx, tx, "mainnet", "exact namespace", captureSymbols, captureStart, captureEnd, readers)
		cancel()
		if failAt == 46 {
			captureFailure(t, got, e, context.Canceled)
			continue
		}
		if failAt != 0 {
			captureFailure(t, got, e, failure)
			continue
		}
		candleImportTestOK(t, e)
		captureOK(t, calls == 45 && len(got) == 3 && got[0].Symbol == "BTCUSDT" && got[2].Symbol == "HYPEUSDT")
		captureOK(t, len(got[0].TradeDaily) == 1 && len(got[0].TradeHourly) == 6 && reflect.DeepEqual(got[0].TradeDaily[0], row) && got[0].Funding != nil && len(got[0].Funding) == 0 && got[0].MarkDaily != nil)
	}
	for _, tc := range []struct {
		ctx        context.Context
		tx         *sql.Tx
		symbols    []string
		start, end time.Time
	}{
		{nil, tx, captureSymbols, captureStart, captureEnd}, {t.Context(), nil, captureSymbols, captureStart, captureEnd},
		{t.Context(), tx, nil, captureStart, captureEnd}, {t.Context(), tx, captureSymbols, captureEnd, captureStart},
		{t.Context(), tx, captureSymbols, captureStart.Add(time.Millisecond), captureEnd}, {t.Context(), tx, captureSymbols, time.UnixMilli(-86400000), captureEnd},
	} {
		got, e := CaptureHistoryTx(tc.ctx, tc.tx, "mainnet", "id", tc.symbols, tc.start, tc.end)
		captureFailure(t, got, e, ErrHistoryCaptureArgument)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, e := CaptureHistoryTx(ctx, tx, "mainnet", "id", captureSymbols, captureStart, captureEnd)
	captureFailure(t, got, e, context.Canceled)
}

// TestCaptureHistorySnapshot проверяет late imports всех15 потоков, gaps и end exclusion.
func TestCaptureHistorySnapshot(t *testing.T) {
	db, other := candleImportTestDB(t)
	candleImportTestOK(t, storage.RegisterDataset(t.Context(), db, "mainnet", "snapshot"))
	tx := captureTx(t, db)
	before, e := CaptureHistoryTx(t.Context(), tx, "mainnet", "snapshot", captureSymbols, captureStart, captureEnd)
	candleImportTestOK(t, e)
	write, e := other.BeginTx(t.Context(), nil)
	candleImportTestOK(t, e)
	defer write.Rollback()
	for i, symbol := range captureSymbols {
		for _, interval := range []string{"D", "60"} {
			_, e = write.Exec(`INSERT INTO momentum.candles VALUES('mainnet','snapshot',$1,$2,$3,9007199254740993.00000001,9007199254740994.00000002,0.000000000000000001,100.25,3.04,false)`, symbol, interval, captureStart)
			candleImportTestOK(t, e)
			_, e = write.Exec(`INSERT INTO momentum.mark_prices VALUES('mainnet','snapshot',$1,$2,$3,9007199254740993.00000001,9007199254740994.00000002,0.000000000000000001,100.5,false)`, symbol, interval, captureStart)
			candleImportTestOK(t, e)
		}
		rate := "0"
		if i > 0 {
			rate = "-0.0000000000000000001"
		}
		_, e = write.Exec(`INSERT INTO momentum.funding VALUES('mainnet','snapshot',$1,$2,$3),('mainnet','snapshot',$1,$4,1)`, symbol, captureStart.Add(123*time.Millisecond), rate, captureEnd)
		candleImportTestOK(t, e)
	}
	candleImportTestOK(t, write.Commit())
	after, e := CaptureHistoryTx(t.Context(), tx, "mainnet", "snapshot", captureSymbols, captureStart, captureEnd)
	candleImportTestOK(t, e)
	captureOK(t, reflect.DeepEqual(before, after))
	candleImportTestOK(t, tx.Commit())
	fresh := captureTx(t, db)
	got, e := CaptureHistoryTx(t.Context(), fresh, "mainnet", "snapshot", captureSymbols, captureStart, captureEnd)
	candleImportTestOK(t, e)
	for _, s := range got {
		captureOK(t, len(s.TradeDaily) == 1 && len(s.TradeHourly) == 1 && len(s.MarkDaily) == 1 && len(s.MarkHourly) == 1 && len(s.Funding) == 1 && !s.TradeDaily[0].Confirmed && !s.MarkHourly[0].Confirmed)
		v, e := s.TradeDaily[0].Open.WireDecimal()
		candleImportTestOK(t, e)
		captureOK(t, v == "9007199254740993.00000001")
		v, e = s.MarkDaily[0].Close.WireDecimal()
		candleImportTestOK(t, e)
		captureOK(t, v == "100.5" && s.Funding[0].FundingTime.Equal(captureStart.Add(123*time.Millisecond)))
		rate, e := s.Funding[0].Rate.WireDecimal()
		candleImportTestOK(t, e)
		want := "-0.0000000000000000001"
		if s.Symbol == "HYPEUSDT" {
			want = "0"
		}
		captureOK(t, rate == want)
	}
	candleImportTestOK(t, fresh.Commit())
	got, e = CaptureHistoryTx(t.Context(), fresh, "mainnet", "snapshot", captureSymbols, captureStart, captureEnd)
	captureFailure(t, got, e, storage.ErrCandles)
	invalid, e := db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	candleImportTestOK(t, e)
	got, e = CaptureHistoryTx(t.Context(), invalid, "mainnet", "snapshot", captureSymbols, captureStart, captureEnd)
	captureFailure(t, got, e, storage.ErrCandleArgument)
	candleImportTestOK(t, invalid.Rollback())
	writable, e := db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	candleImportTestOK(t, e)
	got, e = CaptureHistoryTx(t.Context(), writable, "mainnet", "snapshot", captureSymbols, captureStart, captureEnd)
	captureFailure(t, got, e, storage.ErrCandleArgument)
	candleImportTestOK(t, writable.Rollback())
	missing := captureTx(t, db)
	got, e = CaptureHistoryTx(t.Context(), missing, "mainnet", "absent", captureSymbols, captureStart, captureEnd)
	captureFailure(t, got, e, storage.ErrCandleDataset)
	candleImportTestOK(t, missing.Rollback())
	invalidKey := captureTx(t, db)
	got, e = CaptureHistoryTx(t.Context(), invalidKey, "Mainnet", "snapshot", captureSymbols, captureStart, captureEnd)
	captureFailure(t, got, e, storage.ErrCandleArgument)
	candleImportTestOK(t, invalidKey.Rollback())
	_, e = other.Exec(`INSERT INTO momentum.funding SELECT 'mainnet','snapshot','BTCUSDT',$1::timestamptz+i*INTERVAL '1 millisecond',0 FROM generate_series(200,400) AS i`, captureStart)
	candleImportTestOK(t, e)
	overflow := captureTx(t, db)
	got, e = CaptureHistoryTx(t.Context(), overflow, "mainnet", "snapshot", captureSymbols, captureStart, captureEnd)
	captureFailure(t, got, e, storage.ErrFundingLimit)
	candleImportTestOK(t, overflow.Rollback())
	_, e = other.Exec("DROP TABLE momentum.mark_prices")
	candleImportTestOK(t, e)
	failed := captureTx(t, db)
	got, e = CaptureHistoryTx(t.Context(), failed, "mainnet", "snapshot", captureSymbols, captureStart, captureEnd)
	captureFailure(t, got, e, storage.ErrMarkPriceRead)
}

// TestCaptureHistoryScale проверяет portable7155 rows без внешних файлов и funding cadence.
func TestCaptureHistoryScale(t *testing.T) {
	db, _ := candleImportTestDB(t)
	candleImportTestOK(t, storage.RegisterDataset(t.Context(), db, "mainnet", "scale"))
	write, e := db.BeginTx(t.Context(), nil)
	candleImportTestOK(t, e)
	defer write.Rollback()
	for _, symbol := range captureSymbols {
		for _, interval := range []string{"D", "60"} {
			count, step := 45, "1 day"
			if interval == "60" {
				count, step = 1080, "1 hour"
			}
			args := []any{symbol, interval, captureStart, step, count - 1}
			_, e = write.Exec(`INSERT INTO momentum.candles SELECT 'mainnet','scale',$1,$2,$3::timestamptz+i*$4::interval,9007199254740993.00000001,9007199254740994.00000002,0.000000000000000001,100.25,3.04,i%2=0 FROM generate_series(0,$5::integer) i`, args...)
			candleImportTestOK(t, e)
			_, e = write.Exec(`INSERT INTO momentum.mark_prices SELECT 'mainnet','scale',$1,$2,$3::timestamptz+i*$4::interval,9007199254740993.00000001,9007199254740994.00000002,0.000000000000000001,100.5,i%2=0 FROM generate_series(0,$5::integer) i`, args...)
			candleImportTestOK(t, e)
		}
		_, e = write.Exec(`INSERT INTO momentum.funding SELECT 'mainnet','scale',$1,$2::timestamptz+i*i*INTERVAL '1 second'+INTERVAL '123 milliseconds',CASE WHEN i%2=0 THEN 0 ELSE -0.0000000000000000001 END FROM generate_series(0,134) i`, symbol, captureStart)
		candleImportTestOK(t, e)
	}
	candleImportTestOK(t, write.Commit())
	tx := captureTx(t, db)
	calls := 0
	readers := captureReaders{
		func(ctx context.Context, actual *sql.Tx, key storage.CandleStream, start, end time.Time, limit int) ([]storage.StoredCandle, error) {
			calls++
			return storage.ReadCandleRangeTx(ctx, actual, key, start, end, limit)
		},
		func(ctx context.Context, actual *sql.Tx, key storage.CandleStream, start, end time.Time, limit int) ([]model.MarkPriceCandle, error) {
			calls++
			return storage.ReadMarkPriceRangeTx(ctx, actual, key, start, end, limit)
		},
		func(ctx context.Context, actual *sql.Tx, key storage.FundingStream, start, end time.Time, limit int) ([]model.FundingEvent, error) {
			calls++
			return storage.ReadFundingRangeTx(ctx, actual, key, start, end, limit)
		},
	}
	got, e := captureHistoryTx(t.Context(), tx, "mainnet", "scale", captureSymbols, captureStart, captureEnd, readers)
	candleImportTestOK(t, e)
	total := 0
	for _, stream := range got {
		for group, trades := range [][]storage.StoredCandle{stream.TradeDaily, stream.TradeHourly} {
			marks, count, step := stream.MarkDaily, 45, 24*time.Hour
			if group == 1 {
				marks, count, step = stream.MarkHourly, 1080, time.Hour
			}
			captureOK(t, len(trades) == count && len(marks) == count)
			for i, trade := range trades {
				mark := marks[i]
				captureOK(t, trade.OpenTime.Equal(captureStart.Add(time.Duration(i)*step)) && mark.OpenTime.Equal(trade.OpenTime) && trade.OpenTime.Location() == time.UTC && mark.OpenTime.Location() == time.UTC && trade.Confirmed == (i%2 == 0) && mark.Confirmed == trade.Confirmed)
				values := []model.Decimal{trade.Open, trade.High, trade.Low, trade.Close, trade.Volume, mark.Open, mark.High, mark.Low, mark.Close}
				expected := []string{"9007199254740993.00000001", "9007199254740994.00000002", "0.000000000000000001", "100.25", "3.04", "9007199254740993.00000001", "9007199254740994.00000002", "0.000000000000000001", "100.5"}
				for j, value := range values {
					wire, e := value.WireDecimal()
					candleImportTestOK(t, e)
					captureOK(t, wire == expected[j])
				}
			}
			total += len(trades) + len(marks)
		}
		captureOK(t, len(stream.Funding) == 135)
		for i, event := range stream.Funding {
			rate, e := event.Rate.WireDecimal()
			candleImportTestOK(t, e)
			expected := "0"
			if i%2 != 0 {
				expected = "-0.0000000000000000001"
			}
			captureOK(t, rate == expected && event.FundingTime.Equal(captureStart.Add(time.Duration(i*i)*time.Second+123*time.Millisecond)) && event.FundingTime.Location() == time.UTC)
		}
		total += len(stream.Funding)
	}
	captureOK(t, len(got)*5 == 15 && calls == 45 && total == 7155)
	candleImportTestOK(t, tx.Commit())
}
