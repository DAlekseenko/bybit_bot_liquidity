package history

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/storage"
)

const candleWarmupPrice = "9007199254740993.00000000000000000001"

// candleWarmupFixture задаёт точные непрерывные дневные строки.
func candleWarmupFixture(t *testing.T, cutoff time.Time, n int) []storage.StoredCandle {
	t.Helper()
	price, err := model.ParsePrice(candleWarmupPrice)
	candleImportTestOK(t, err)
	rows := make([]storage.StoredCandle, n)
	for i := range rows {
		rows[i] = storage.StoredCandle{OpenTime: cutoff.Add(-time.Duration(n-i) * candleWarmupDay), Close: price, Confirmed: true}
	}
	return rows
}

// TestReadCandleWarmupClassifies проверяет readiness и сохраняет точный ряд.
func TestReadCandleWarmupClassifies(t *testing.T) {
	cutoff := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	db := new(sql.DB)
	key := storage.CandleStream{Environment: "testnet", DatasetID: "id", Symbol: "HYPEUSDT", IntervalCode: "D"}
	for _, tc := range []struct {
		name        string
		status      CandleWarmupStatus
		n           int
		omit        int
		unconfirmed bool
	}{
		{"ready", CandleWarmupReady, 31, -1, false},
		{"empty", CandleWarmupUnavailable, 0, -1, false},
		{"short", CandleWarmupInsufficient, 30, -1, false},
		{"internal", CandleWarmupGap, 31, 18, false},
		{"tail", CandleWarmupGap, 31, 30, false},
		{"unconfirmed", CandleWarmupUnconfirmed, 31, -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := candleWarmupFixture(t, cutoff, tc.n)
			if tc.omit >= 0 {
				rows = append(rows[:tc.omit], rows[tc.omit+1:]...)
			}
			if tc.unconfirmed {
				rows[17].Confirmed = false
			}
			calls := 0
			read := func(ctx context.Context, pool *sql.DB, actual storage.CandleStream, start, end time.Time, limit int) ([]storage.StoredCandle, error) {
				calls++
				if ctx != t.Context() || pool != db || actual != key || start != cutoff.Add(-31*candleWarmupDay) || end != cutoff || limit != 31 {
					t.Fatal("wrong range")
				}
				return rows, nil
			}
			result, err := readCandleWarmup(t.Context(), db, key, cutoff.In(time.FixedZone("offset", -18000)), read)
			if err != nil || calls != 1 || result.Status != tc.status {
				t.Fatalf("calls=%d status=%q error=%v", calls, result.Status, err)
			}
			if tc.status != CandleWarmupReady {
				if result.Closes != nil {
					t.Fatal("partial result")
				}
				return
			}
			if len(result.Closes) != 31 {
				t.Fatal("wrong length")
			}
			for i, row := range result.Closes {
				value, err := row.Close.WireDecimal()
				if row.Symbol != key.Symbol || row.OpenTime != rows[i].OpenTime || !row.Confirmed || value != candleWarmupPrice || err != nil {
					t.Fatalf("wrong row %d: %+v", i, row)
				}
			}
			result.Closes[0].OpenTime = time.Time{}
			if rows[0].OpenTime.IsZero() {
				t.Fatal("input changed")
			}
		})
	}
}

// TestReadCandleWarmupFailures отделяет отказ хранилища, отмену и неверное окно.
func TestReadCandleWarmupFailures(t *testing.T) {
	cutoff := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	db := new(sql.DB)
	key := storage.CandleStream{Environment: "testnet", DatasetID: "id", Symbol: "HYPEUSDT", IntervalCode: "D"}
	noRead := func(context.Context, *sql.DB, storage.CandleStream, time.Time, time.Time, int) ([]storage.StoredCandle, error) {
		t.Fatal("read unexpectedly called")
		return nil, nil
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		ctx      context.Context
		db       *sql.DB
		interval string
		stamp    time.Time
		want     error
	}{
		{nil, db, "D", cutoff, storage.ErrCandleArgument},
		{t.Context(), nil, "D", cutoff, storage.ErrCandleArgument},
		{t.Context(), db, "60", cutoff, storage.ErrCandleArgument},
		{t.Context(), db, "D", cutoff.Add(time.Millisecond), storage.ErrCandleArgument},
		{t.Context(), db, "D", time.Unix(0, 0), storage.ErrCandleArgument},
		{t.Context(), db, "D", time.Date(294277, 1, 1, 0, 0, 0, 0, time.UTC), storage.ErrCandleArgument},
		{canceled, db, "D", cutoff, context.Canceled},
	} {
		key.IntervalCode = tc.interval
		got, err := readCandleWarmup(tc.ctx, tc.db, key, tc.stamp, noRead)
		if got.Status != "" || got.Closes != nil || !errors.Is(err, storage.ErrCandles) || !errors.Is(err, tc.want) {
			t.Fatalf("got=%+v err=%v want=%v", got, err, tc.want)
		}
	}
	key.IntervalCode = "D"
	got, err := readCandleWarmup(t.Context(), db, key, cutoff, func(context.Context, *sql.DB, storage.CandleStream, time.Time, time.Time, int) ([]storage.StoredCandle, error) {
		return nil, storage.ErrCandleDataset
	})
	if got.Status != "" || got.Closes != nil || err != storage.ErrCandleDataset {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	rows := candleWarmupFixture(t, cutoff, 31)
	rows[7].Close = model.Zero()
	got, err = classifyCandleWarmup(key.Symbol, cutoff, rows)
	if got.Status != "" || got.Closes != nil || !errors.Is(err, data.ReasonInvalidPrice) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

// TestReadCandleWarmupPostgres проверяет фактический SQL scope и границы.
func TestReadCandleWarmupPostgres(t *testing.T) {
	db, _ := candleImportTestDB(t)
	ctx := t.Context()
	cutoff := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	key := storage.CandleStream{Environment: "testnet", DatasetID: "warmup", Symbol: "HYPEUSDT", IntervalCode: "D"}
	for _, env := range []string{"testnet", "mainnet"} {
		candleImportTestOK(t, storage.RegisterDataset(ctx, db, env, key.DatasetID))
	}
	candleImportTestOK(t, storage.RegisterDataset(ctx, db, "testnet", "hype-empty"))
	for _, row := range []struct {
		env, symbol, interval string
		oldest, newest        int
	}{
		{"testnet", "HYPEUSDT", "D", 32, -1},
		{"mainnet", "HYPEUSDT", "D", 1, 1},
		{"testnet", "BTCUSDT", "D", 1, 1},
		{"testnet", "ETHUSDT", "60", 1, 1},
	} {
		_, err := db.ExecContext(ctx, `INSERT INTO momentum.candles
   (environment,dataset_id,symbol,interval_code,open_time,open,high,low,close,volume,confirmed)
   SELECT $1,$2,$3,$4,$5::timestamptz - d*interval '24 hours',$6::numeric,$6::numeric,$6::numeric,$6::numeric,1,true
   FROM generate_series($7::int,$8::int) AS d ORDER BY d
  `, row.env, key.DatasetID, row.symbol, row.interval, cutoff, candleWarmupPrice, row.newest, row.oldest)
		candleImportTestOK(t, err)
	}
	got, err := ReadCandleWarmup(ctx, db, key, cutoff)
	candleImportTestOK(t, err)
	if got.Status != CandleWarmupReady || len(got.Closes) != 31 {
		t.Fatalf("status=%q count=%d", got.Status, len(got.Closes))
	}
	for i, row := range got.Closes {
		value, err := row.Close.WireDecimal()
		if value != candleWarmupPrice || err != nil || !row.OpenTime.Equal(cutoff.Add(-time.Duration(31-i)*candleWarmupDay)) || row.OpenTime.Location() != time.UTC {
			t.Fatalf("wrong row %d: %+v %v", i, row, err)
		}
	}
	for _, tc := range []struct {
		env, symbol, dataset string
		want                 CandleWarmupStatus
		wantError            error
	}{
		{"mainnet", "HYPEUSDT", "warmup", CandleWarmupInsufficient, nil},
		{"testnet", "BTCUSDT", "warmup", CandleWarmupInsufficient, nil},
		{"testnet", "HYPEUSDT", "hype-empty", CandleWarmupUnavailable, nil},
		{"mainnet", "HYPEUSDT", "unknown", "", storage.ErrCandleDataset},
		{"paper", "HYPEUSDT", "warmup", "", storage.ErrCandleArgument},
	} {
		checked := storage.CandleStream{Environment: tc.env, DatasetID: tc.dataset, Symbol: tc.symbol, IntervalCode: "D"}
		got, err := ReadCandleWarmup(ctx, db, checked, cutoff)
		if got.Status != tc.want || got.Closes != nil || !errors.Is(err, tc.wantError) {
			t.Fatalf("key=%+v got=%+v err=%v", checked, got, err)
		}
	}
}
