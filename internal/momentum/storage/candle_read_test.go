package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// candleReadDB создаёт реальные schema/namespace и различимые OHLCV fixtures.
func candleReadDB(t *testing.T) (*sql.DB, CandleStream) {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	_, err := db.Exec(`INSERT INTO momentum.datasets(environment,dataset_id,source,category)
	 VALUES ('testnet','fixture','bybit_v5','linear'),('mainnet','fixture','bybit_v5','linear');
 INSERT INTO momentum.candles SELECT 'testnet','fixture','BTCUSDT','D',stamp,
 9007199254740993.00000001,9007199254740994.00000002,0.000000000000000001,123.4567890123456789,3.0400,true
 FROM unnest(ARRAY[TIMESTAMPTZ '1970-01-03 00:00+00',TIMESTAMPTZ '1970-01-02 00:00+00']) stamp`)
	migrationResult(t, err, nil)
	return db, CandleStream{"testnet", "fixture", "BTCUSDT", "D"}
}

// requireCandleReadError проверяет nil, sentinel/context и отсутствие SQL-утечки.
func requireCandleReadError(t *testing.T, ctx context.Context, db *sql.DB, key CandleStream, start, end time.Time, limit int, want error) {
	t.Helper()
	got, err := ReadCandleRange(ctx, db, key, start, end, limit)
	if err != nil && (strings.Contains(err.Error(), "secret-marker") || strings.Contains(err.Error(), "sensitive_sql_marker") || strings.Contains(err.Error(), "postgres://") || strings.Contains(err.Error(), "pq:")) {
		t.Fatalf("raw SQL diagnostic leaked: %v", err)
	}
	migrationResult(t, err, want)
	if got != nil || !errors.Is(err, ErrCandles) || (want == ErrCandles && errors.Is(err, ErrCandleDataset)) {
		t.Fatalf("unexpected partial/absence rows=%+v err=%v", got, err)
	}
}

// TestReadCandleRange проверяет сортировку, точность, scope, empty и row cap.
func TestReadCandleRange(t *testing.T) {
	db, key := candleReadDB(t)
	db.SetMaxOpenConns(1)
	_, err := db.Exec("SET TimeZone='Pacific/Honolulu'")
	migrationResult(t, err, nil)
	start, end := time.Unix(0, 0), time.Unix(4*86400, 0)
	zone := time.FixedZone("plus3", 3*3600)
	rows, err := ReadCandleRange(context.Background(), db, key, start.In(zone), end.In(zone), 2)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for i, row := range rows {
		if row.OpenTime != time.Unix(int64(i+1)*86400, 0).UTC() || row.OpenTime.Location() != time.UTC || !row.Confirmed {
			t.Fatalf("timestamp/order/confirmed row=%+v", row)
		}
		for j, value := range []model.Decimal{row.Open, row.High, row.Low, row.Close, row.Volume} {
			want, parseErr := model.ParseAmount([]string{"9007199254740993.00000001", "9007199254740994.00000002", "0.000000000000000001", "123.4567890123456789", "3.04"}[j])
			if cmp, err := value.Cmp(want); parseErr != nil || err != nil || cmp != 0 {
				t.Fatalf("field %d lost precision: %v", j, err)
			}
		}
	}
	for _, tc := range []struct {
		key   CandleStream
		limit int
		want  error
	}{{key, 1, ErrCandleLimit}, {CandleStream{"testnet", "missing", "BTCUSDT", "D"}, 2, ErrCandleDataset}} {
		requireCandleReadError(t, context.Background(), db, tc.key, start, end, tc.limit, tc.want)
	}
	for _, empty := range []CandleStream{{"mainnet", "fixture", "BTCUSDT", "D"}, {"testnet", "fixture", "ETHUSDT", "D"}, {"testnet", "fixture", "BTCUSDT", "60"}} {
		if got, err := ReadCandleRange(context.Background(), db, empty, start, end, 2); err != nil || got == nil || len(got) != 0 {
			t.Fatalf("known empty scope=%+v rows=%+v err=%v", empty, got, err)
		}
	}
	got, err := ReadCandleRange(context.Background(), db, key, time.Unix(86400, 0), time.Unix(2*86400, 0), 2)
	if err != nil || len(got) != 1 || !got[0].OpenTime.Equal(time.Unix(86400, 0)) {
		t.Fatalf("half-open exact bounds=%+v err=%v", got, err)
	}
	for _, id := range []string{strings.Repeat("я", 128), "quoted' id", "\tid\t"} {
		_, err := db.Exec("INSERT INTO momentum.datasets(environment,dataset_id,source,category) VALUES ('testnet',$1,'bybit_v5','linear')", id)
		migrationResult(t, err, nil)
		_, err = db.Exec(`INSERT INTO momentum.candles SELECT 'testnet',$1,symbol,'60',TIMESTAMPTZ '1970-01-01 01:00+00',
 open,high,low,close,0,false FROM momentum.candles WHERE interval_code='D' LIMIT 1`, id)
		migrationResult(t, err, nil)
		got, err := ReadCandleRange(context.Background(), db, CandleStream{"testnet", id, "BTCUSDT", "60"}, start, end, 1)
		if err != nil || len(got) != 1 {
			t.Fatalf("hourly opaque key rows=%+v err=%v", got, err)
		}
		if other, err := ReadCandleRange(context.Background(), db, CandleStream{"testnet", id, "BTCUSDT", "D"}, start, end, 2); err != nil || other == nil || len(other) != 0 {
			t.Fatalf("dataset/interval isolation rows=%+v err=%v", other, err)
		}
		cmp, cmpErr := got[0].Volume.Cmp(model.Zero())
		if cmpErr != nil || cmp != 0 || got[0].Confirmed || got[0].OpenTime != time.Unix(3600, 0).UTC() {
			t.Fatalf("hourly values=%+v err=%v", got[0], cmpErr)
		}
	}
}

// TestReadCandleArguments проверяет ключи, UTC/precision bounds и nil/context.
func TestReadCandleArguments(t *testing.T) {
	db, key := candleReadDB(t)
	start, end := time.Unix(0, 0), time.Unix(3*86400, 0)
	for _, invalid := range []CandleStream{{"paper", "fixture", "BTCUSDT", "D"}, {"testnet", "", "BTCUSDT", "D"},
		{"testnet", " fixture", "BTCUSDT", "D"}, {"testnet", strings.Repeat("я", 129), "BTCUSDT", "D"},
		{"testnet", "bad\x00id", "BTCUSDT", "D"}, {"testnet", "bad\xffid", "BTCUSDT", "D"},
		{"testnet", "fixture", "btcusdt", "D"}, {"testnet", "fixture", "BTCUSDC", "D"}, {"testnet", "fixture", "BTCUSDT", "1"}} {
		requireCandleReadError(t, context.Background(), db, invalid, start, end, 2, ErrCandleArgument)
	}
	for _, tc := range []struct {
		start, end time.Time
		limit      int
	}{{start, end, 0}, {start, end, 201}, {end, start, 2}, {start, start, 2}, {time.Unix(0, 1), end, 2},
		{time.Unix(-1, 0), end, 2}, {time.Unix(3600, 0), end, 2}, {start, time.Unix(3*86400, 1), 2},
		{time.Unix(1<<63-1, 0), end, 2}} {
		requireCandleReadError(t, context.Background(), db, key, tc.start, tc.end, tc.limit, ErrCandleArgument)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	requireCandleReadError(t, ctx, nil, key, start, end, 2, context.Canceled)
	requireCandleReadError(t, context.Background(), nil, key, start, end, 2, ErrCandleArgument)
	requireCandleReadError(t, context.Background(), openTestPostgres(t), CandleStream{"testnet", "secret-marker", "BTCUSDT", "D"}, start, end, 2, ErrCandles)
	migrationResult(t, db.Close(), nil)
	requireCandleReadError(t, context.Background(), db, key, start, end, 2, ErrCandles)
}

// TestReadCandleUnrepresentable проверяет nil после уже прочитанной valid row.
func TestReadCandleUnrepresentable(t *testing.T) {
	for _, column := range []string{"open", "volume"} {
		db, key := candleReadDB(t)
		_, err := db.Exec("UPDATE momentum.candles SET " + column + "=1." + strings.Repeat("0", 64) + "1 WHERE open_time=TIMESTAMPTZ '1970-01-03 00:00+00'")
		migrationResult(t, err, nil)
		requireCandleReadError(t, context.Background(), db, key, time.Unix(0, 0), time.Unix(3*86400, 0), 2, ErrCandles)
	}
}

// TestReadCandleDeadline проверяет реальную SQL-блокировку без fake DB.
func TestReadCandleDeadline(t *testing.T) {
	db, key := candleReadDB(t)
	holder, err := db.BeginTx(context.Background(), nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.Exec("LOCK TABLE momentum.candles IN ACCESS EXCLUSIVE MODE")
	migrationResult(t, err, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	requireCandleReadError(t, ctx, db, key, time.Unix(0, 0), time.Unix(3*86400, 0), 2, context.DeadlineExceeded)
}
