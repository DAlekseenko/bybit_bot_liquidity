package history

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/storage"
)

// commonWarmupRows возвращает окно символа с существующей точной fixture.
func commonWarmupRows(t *testing.T, symbol string, cutoff time.Time) CandleWarmup {
	t.Helper()
	rows := candleWarmupFixture(t, cutoff, 31)
	closes := make([]data.DailyClose, len(rows))
	for i, row := range rows {
		closes[i] = data.DailyClose{Symbol: symbol, OpenTime: row.OpenTime, Close: row.Close, Confirmed: row.Confirmed}
	}
	return CandleWarmup{Status: CandleWarmupReady, Closes: closes}
}

// TestReadCommonCandleWarmup проверяет единый cutoff и все причины неготовности.
func TestReadCommonCandleWarmup(t *testing.T) {
	cutoff := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	symbols := []string{"HYPEUSDT", "BTCUSDT", "ETHUSDT"}
	original := slices.Clone(symbols)
	for _, status := range []CandleWarmupStatus{CandleWarmupReady, CandleWarmupUnavailable, CandleWarmupInsufficient, CandleWarmupGap, CandleWarmupUnconfirmed} {
		t.Run(string(status), func(t *testing.T) {
			var calls []string
			read := func(ctx context.Context, db *sql.DB, key storage.CandleStream, stamp time.Time) (CandleWarmup, error) {
				if key.Environment != "mainnet" || key.DatasetID != "opaque/α" || key.IntervalCode != "D" || !stamp.Equal(cutoff) || stamp.Location() != time.UTC {
					t.Fatal("scope or cutoff changed")
				}
				calls = append(calls, key.Symbol)
				if key.Symbol == "HYPEUSDT" && status != CandleWarmupReady {
					return CandleWarmup{Status: status}, nil
				}
				return commonWarmupRows(t, key.Symbol, stamp), nil
			}
			got, err := readCommonCandleWarmup(t.Context(), new(sql.DB), "mainnet", "opaque/α", symbols, cutoff.In(time.FixedZone("west", -25200)), read)
			candleImportTestOK(t, err)
			if !reflect.DeepEqual(calls, []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}) || !reflect.DeepEqual(symbols, original) || got.Statuses["HYPEUSDT"] != status || len(got.Statuses) != 3 {
				t.Fatalf("wrong statuses or calls: %+v %v", got, calls)
			}
			if status != CandleWarmupReady && got.Windows != nil {
				t.Fatal("partial windows escaped")
			}
			if status == CandleWarmupReady && len(got.Windows) != 3 {
				t.Fatal("ready windows missing")
			}
		})
	}
}

// TestReadCommonCandleWarmupFailures сохраняет отказ вместо частичного результата.
func TestReadCommonCandleWarmupFailures(t *testing.T) {
	cutoff := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	symbols := []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}
	for _, nilDB := range []bool{true, false} {
		ctx, db := context.Context(nil), new(sql.DB)
		if nilDB {
			ctx, db = t.Context(), nil
		}
		got, err := ReadCommonCandleWarmup(ctx, db, "testnet", "id", symbols, cutoff)
		if !errors.Is(err, storage.ErrCandleArgument) || !reflect.DeepEqual(got, CommonCandleWarmup{}) {
			t.Fatalf("nil argument: %+v %v", got, err)
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := ReadCommonCandleWarmup(canceled, new(sql.DB), "testnet", "id", symbols, cutoff)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, CommonCandleWarmup{}) {
		t.Fatalf("canceled argument: %+v %v", got, err)
	}
	for _, tc := range []struct {
		name                 string
		environment, dataset string
		symbols              []string
		cutoff               time.Time
	}{
		{"environment", "paper", "id", symbols, cutoff},
		{"id", "testnet", "", symbols, cutoff},
		{"id-space", "testnet", " id", symbols, cutoff},
		{"id-nul", "testnet", "x\x00y", symbols, cutoff},
		{"id-utf8", "testnet", string([]byte{0xff}), symbols, cutoff},
		{"symbols", "testnet", "id", []string{"BTCUSDT", "BTCUSDT", "HYPEUSDT"}, cutoff},
		{"short", "testnet", "id", symbols[:2], cutoff},
		{"time", "testnet", "id", symbols, cutoff.Add(time.Nanosecond)},
		{"epoch", "testnet", "id", symbols, time.UnixMilli(0)},
		{"upper", "testnet", "id", symbols, time.Date(294277, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(context.Context, *sql.DB, storage.CandleStream, time.Time) (CandleWarmup, error) {
				t.Fatal("invalid input reached reader")
				return CandleWarmup{}, nil
			}
			got, err := readCommonCandleWarmup(t.Context(), new(sql.DB), tc.environment, tc.dataset, tc.symbols, tc.cutoff, read)
			if !errors.Is(err, storage.ErrCandleArgument) || !reflect.DeepEqual(got, CommonCandleWarmup{}) {
				t.Fatalf("got %+v %v", got, err)
			}
		})
	}
	for _, mode := range []string{"sql", "context", "last-cutoff", "different-symbol", "unknown-status"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			read := func(_ context.Context, _ *sql.DB, key storage.CandleStream, stamp time.Time) (CandleWarmup, error) {
				window := commonWarmupRows(t, key.Symbol, stamp)
				if key.Symbol == "ETHUSDT" {
					switch mode {
					case "sql":
						return CandleWarmup{}, storage.ErrCandleDataset
					case "context":
						cancel()
					case "last-cutoff":
						window = commonWarmupRows(t, key.Symbol, stamp.Add(-candleWarmupDay))
					case "different-symbol":
						window.Closes[0].Symbol = "FOREIGNUSDT"
					case "unknown-status":
						window.Status = "unknown"
					}
				}
				return window, nil
			}
			got, err := readCommonCandleWarmup(ctx, new(sql.DB), "testnet", "id", symbols, cutoff, read)
			if err == nil || !reflect.DeepEqual(got, CommonCandleWarmup{}) {
				t.Fatalf("partial result %+v %v", got, err)
			}
			if mode == "sql" && !errors.Is(err, storage.ErrCandleDataset) {
				t.Fatal(err)
			}
			if mode == "context" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

// TestReadCommonCandleWarmupPostgres проверяет реальные отдельные снимки трёх рядов.
func TestReadCommonCandleWarmupPostgres(t *testing.T) {
	db, _ := candleImportTestDB(t)
	ctx := t.Context()
	cutoff := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	symbols := []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}
	for _, env := range []string{"testnet", "mainnet"} {
		candleImportTestOK(t, storage.RegisterDataset(ctx, db, env, "common"))
	}
	for _, symbol := range symbols {
		_, err := db.ExecContext(ctx, `INSERT INTO momentum.candles
 (environment,dataset_id,symbol,interval_code,open_time,open,high,low,close,volume,confirmed)
 SELECT 'testnet','common',$1,'D',$2::timestamptz-d*interval '24 hours',$3::numeric,$3::numeric,$3::numeric,$3::numeric,1,true
 FROM generate_series(-1,32) AS d`, symbol, cutoff, candleWarmupPrice)
		candleImportTestOK(t, err)
	}
	got, err := ReadCommonCandleWarmup(ctx, db, "testnet", "common", symbols, cutoff)
	candleImportTestOK(t, err)
	if len(got.Windows) != 3 {
		t.Fatalf("missing windows %+v", got)
	}
	for symbol, rows := range got.Windows {
		if len(rows) != 31 || got.Statuses[symbol] != CandleWarmupReady {
			t.Fatalf("bad rows %+v", got)
		}
		for i, row := range rows {
			value, err := row.Close.WireDecimal()
			if err != nil || value != candleWarmupPrice || !row.OpenTime.Equal(cutoff.Add(-time.Duration(31-i)*candleWarmupDay)) {
				t.Fatal("wrong scope/value")
			}
		}
	}
	_, err = db.ExecContext(ctx, `DELETE FROM momentum.candles WHERE environment='testnet' AND dataset_id='common' AND symbol='HYPEUSDT' AND open_time=$1`, cutoff.Add(-candleWarmupDay))
	candleImportTestOK(t, err)
	got, err = ReadCommonCandleWarmup(ctx, db, "testnet", "common", symbols, cutoff)
	candleImportTestOK(t, err)
	if got.Windows != nil || got.Statuses["HYPEUSDT"] != CandleWarmupGap {
		t.Fatal("gap allowed full windows")
	}
	got, err = ReadCommonCandleWarmup(ctx, db, "mainnet", "common", symbols, cutoff)
	candleImportTestOK(t, err)
	if got.Windows != nil || got.Statuses["BTCUSDT"] != CandleWarmupUnavailable {
		t.Fatal("environment mixed")
	}
	got, err = ReadCommonCandleWarmup(ctx, db, "testnet", "missing", symbols, cutoff)
	if !errors.Is(err, storage.ErrCandleDataset) || !reflect.DeepEqual(got, CommonCandleWarmup{}) {
		t.Fatal("dataset failure became unavailable")
	}
}
