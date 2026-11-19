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

// markReadFixture создаёт независимые mark/trade ряды, включая реальный hourly gap.
func markReadFixture(t *testing.T) (*sql.DB, CandleStream) {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(t.Context(), db), nil)
	marketStatement(t, db, `INSERT INTO momentum.datasets(environment,dataset_id,source,category) VALUES
 ('testnet','fixture','bybit_v5','linear'),('mainnet','fixture','bybit_v5','linear'),
 ('testnet','other','bybit_v5','linear'),('testnet','empty','bybit_v5','linear');
 INSERT INTO momentum.mark_prices VALUES
 ('testnet','fixture','BTCUSDT','D','epoch',9007199254740993.00000000000000000001,9007199254740995.00000000000000000003,9007199254740992.00000000000000000009,9007199254740994.00000000000000000002,true),
 ('testnet','fixture','BTCUSDT','D','1970-01-03+00',7,9,6,8,false),
 ('testnet','fixture','BTCUSDT','D','1970-01-04+00',7,9,6,8,true),
 ('mainnet','fixture','BTCUSDT','D','epoch',1,1,1,1,true),
 ('mainnet','fixture','BTCUSDT','60','epoch',5,5,5,5,true),
 ('testnet','other','BTCUSDT','D','epoch',2,2,2,2,true),
 ('testnet','fixture','ETHUSDT','D','epoch',3,3,3,3,true),
 ('testnet','fixture','BTCUSDT','60','epoch',4,4,4,4,true),
 ('testnet','fixture','BTCUSDT','60','1970-01-01 01:00+00',10,10,10,10,true),
 ('testnet','fixture','BTCUSDT','60','1970-01-01 03:00+00',11,11,11,11,true);
 INSERT INTO momentum.candles VALUES
 ('testnet','fixture','BTCUSDT','D','epoch',100,110,90,105,1,true),
 ('testnet','empty','BTCUSDT','D','epoch',100,110,90,105,1,true)`, true)
	return db, CandleStream{Environment: "testnet", DatasetID: "fixture", Symbol: "BTCUSDT", IntervalCode: "D"}
}

// markReadFailureTest проверяет nil результат, sentinel и безопасную диагностику.
func markReadFailureTest(t *testing.T, ctx context.Context, db *sql.DB, key CandleStream, start, end time.Time, limit int, want error) {
	t.Helper()
	got, err := ReadMarkPriceRange(ctx, db, key, start, end, limit)
	if got != nil || !errors.Is(err, ErrMarkPriceRead) || !errors.Is(err, want) {
		t.Fatalf("rows=%v error=%v want=%v", got, err, want)
	}
	for _, forbidden := range []string{"mark-read-secret", "pq:", "postgres://"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("unsafe read error=%v", err)
		}
	}
}

// TestMarkReadPrices проверяет точные OHLC и отказ без округления или частичных цен.
func TestMarkReadPrices(t *testing.T) {
	valid := [4]string{"9007199254740993.00000000000000000001", "9007199254740995.00000000000000000003",
		"9007199254740992.00000000000000000009", "9007199254740994.00000000000000000002"}
	prices, err := markReadPrices(valid)
	if err != nil {
		t.Fatal(err)
	}
	for i, price := range prices {
		raw, err := price.WireDecimal()
		if err != nil || raw != valid[i] {
			t.Fatalf("price[%d]=%s %v", i, raw, err)
		}
	}
	for _, bad := range []string{"", "0", "-1", "NaN", "1/3", "0." + strings.Repeat("0", 64) + "1"} {
		input := valid
		input[3] = bad
		got, err := markReadPrices(input)
		if got != ([4]model.Decimal{}) || !errors.Is(err, ErrMarkPriceRead) {
			t.Fatalf("partial prices=%v error=%v", got, err)
		}
	}
	for _, field := range []int{0, 3} {
		for _, value := range []string{"1", "9007199254740999"} {
			input := valid
			input[field] = value
			got, err := markReadPrices(input)
			if got != ([4]model.Decimal{}) || !errors.Is(err, ErrMarkPriceRead) {
				t.Fatalf("invalid bounds=%v error=%v", got, err)
			}
		}
	}
}

// TestReadMarkPriceArguments проверяет чистые отказы и приоритет отмены до обращения к SQL.
func TestReadMarkPriceArguments(t *testing.T) {
	start, end := time.Unix(0, 0), time.Unix(86400, 0)
	valid := CandleStream{Environment: "testnet", DatasetID: "fixture", Symbol: "BTCUSDT", IntervalCode: "D"}
	pool := new(sql.DB)
	invalidKeys := []CandleStream{
		{Environment: "paper", DatasetID: valid.DatasetID, Symbol: valid.Symbol, IntervalCode: valid.IntervalCode},
		{Environment: valid.Environment, DatasetID: "", Symbol: valid.Symbol, IntervalCode: valid.IntervalCode},
		{Environment: valid.Environment, DatasetID: " mark-read-secret", Symbol: valid.Symbol, IntervalCode: valid.IntervalCode},
		{Environment: valid.Environment, DatasetID: "x\x00", Symbol: valid.Symbol, IntervalCode: valid.IntervalCode},
		{Environment: valid.Environment, DatasetID: "\xff", Symbol: valid.Symbol, IntervalCode: valid.IntervalCode},
		{Environment: valid.Environment, DatasetID: strings.Repeat("я", 129), Symbol: valid.Symbol, IntervalCode: valid.IntervalCode},
		{Environment: valid.Environment, DatasetID: valid.DatasetID, Symbol: "btcusdt", IntervalCode: valid.IntervalCode},
		{Environment: valid.Environment, DatasetID: valid.DatasetID, Symbol: valid.Symbol, IntervalCode: "1"},
	}
	for _, key := range invalidKeys {
		markReadFailureTest(t, t.Context(), pool, key, start, end, 1, ErrMarkPriceReadArgument)
	}
	for _, tc := range []struct {
		start, end time.Time
		limit      int
	}{{start, end, 0}, {start, end, 201}, {end, start, 1}, {start, start, 1},
		{time.UnixMilli(-1), end, 1}, {start.Add(time.Nanosecond), end, 1}, {start, end.Add(time.Nanosecond), 1},
		{start, time.Unix(1<<63-1, 0), 1}} {
		markReadFailureTest(t, t.Context(), pool, valid, tc.start, tc.end, tc.limit, ErrMarkPriceReadArgument)
	}
	markReadFailureTest(t, nil, pool, valid, start, end, 1, ErrMarkPriceReadArgument)
	markReadFailureTest(t, t.Context(), nil, valid, start, end, 1, ErrMarkPriceReadArgument)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	markReadFailureTest(t, ctx, nil, CandleStream{}, start, end, 0, context.Canceled)
}

// TestReadMarkPriceProjection проверяет SQL scope, UTC, half-open границы и сохранённый confirmed.
func TestReadMarkPriceProjection(t *testing.T) {
	db, key := markReadFixture(t)
	db.SetMaxOpenConns(1)
	marketStatement(t, db, "SET TIME ZONE 'Pacific/Honolulu'", true)
	marketStatement(t, db, `UPDATE momentum.mark_prices SET open=(open::text||repeat('0',75))::numeric
 WHERE environment='testnet' AND dataset_id='fixture' AND symbol='BTCUSDT' AND interval_code='D' AND open_time='epoch'`, true)
	zone := time.FixedZone("east", 3*3600)
	start, end := time.Unix(0, 0).In(zone), time.Unix(3*86400, 0).In(zone)
	got, err := ReadMarkPriceRange(t.Context(), db, key, start, end, 2)
	if err != nil || len(got) != 2 || got[0].OpenTime != start.UTC() || got[1].OpenTime != time.Unix(2*86400, 0).UTC() ||
		got[1].Confirmed || !got[0].Confirmed {
		t.Fatalf("projection=%v error=%v", got, err)
	}
	for i, value := range []model.Decimal{got[0].Open, got[0].High, got[0].Low, got[0].Close} {
		raw, err := value.WireDecimal()
		want := []string{"9007199254740993.00000000000000000001", "9007199254740995.00000000000000000003", "9007199254740992.00000000000000000009", "9007199254740994.00000000000000000002"}[i]
		if err != nil || raw != want {
			t.Fatalf("field[%d]=%s error=%v", i, raw, err)
		}
	}
	single, err := ReadMarkPriceRange(t.Context(), db, key, start, time.UnixMilli(1), 1)
	if err != nil || len(single) != 1 || single[0].OpenTime != start.UTC() {
		t.Fatalf("millisecond upper bound=%v error=%v", single, err)
	}
	after, err := ReadMarkPriceRange(t.Context(), db, key, time.UnixMilli(1), end, 1)
	if err != nil || len(after) != 1 || after[0].OpenTime != time.Unix(2*86400, 0).UTC() {
		t.Fatalf("millisecond lower bound=%v error=%v", after, err)
	}

	for _, tc := range []struct{ environment, dataset, symbol, code, close string }{
		{"mainnet", "fixture", "BTCUSDT", "D", "1"}, {"mainnet", "fixture", "BTCUSDT", "60", "5"},
		{"testnet", "other", "BTCUSDT", "D", "2"},
		{"testnet", "fixture", "ETHUSDT", "D", "3"}, {"testnet", "fixture", "BTCUSDT", "60", "4"},
	} {
		k := CandleStream{tc.environment, tc.dataset, tc.symbol, tc.code}
		rangeEnd := time.Unix(3600, 0)
		if tc.code == "D" {
			rangeEnd = time.Unix(86400, 0)
		}
		rows, err := ReadMarkPriceRange(t.Context(), db, k, start, rangeEnd, 1)
		if err != nil || len(rows) != 1 {
			t.Fatalf("namespace=%+v rows=%v error=%v", k, rows, err)
		}
		value, err := rows[0].Close.WireDecimal()
		if value != tc.close || err != nil {
			t.Fatalf("wrong namespace close=%s error=%v", value, err)
		}
	}
	key.IntervalCode = "60"
	got, err = ReadMarkPriceRange(t.Context(), db, key, start, time.Unix(4*3600, 0), 3)
	if err != nil || len(got) != 3 || got[2].OpenTime != time.Unix(3*3600, 0).UTC() {
		t.Fatalf("hourly gap=%v error=%v", got, err)
	}
	for _, id := range []string{"fixture", "empty"} {
		key.DatasetID = id
		got, err = ReadMarkPriceRange(t.Context(), db, key, time.Unix(2*3600, 0), time.Unix(3*3600, 0), 1)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("empty gap=%v error=%v", got, err)
		}
	}
	key.IntervalCode = "D"
	got, err = ReadMarkPriceRange(t.Context(), db, key, start, time.Unix(86400, 0), 1)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty mark fell back to trade rows: %v %v", got, err)
	}
}

// TestReadMarkPriceCapAndFailures проверяет limit+1, opaque IDs, испорченные данные и provenance.
func TestReadMarkPriceCapAndFailures(t *testing.T) {
	db, key := markReadFixture(t)
	start := time.Unix(0, 0).UTC()
	for _, id := range []string{strings.Repeat("я", 128), "x'--", "\tid\t"} {
		migrationResult(t, RegisterDataset(t.Context(), db, "testnet", id), nil)
		_, err := db.Exec(`INSERT INTO momentum.mark_prices SELECT 'testnet',$1,'BTCUSDT','60',
 TIMESTAMPTZ 'epoch'+n*INTERVAL '1 hour',n+1,n+1,n+1,n+1,true FROM generate_series(0,200) n`, id)
		migrationResult(t, err, nil)
		key.DatasetID, key.IntervalCode = id, "60"
		rows, err := ReadMarkPriceRange(t.Context(), db, key, start, time.Unix(200*3600, 0), 200)
		if err != nil || len(rows) != 200 || rows[199].OpenTime != time.Unix(199*3600, 0).UTC() {
			t.Fatalf("200 rows=%d error=%v", len(rows), err)
		}
		markReadFailureTest(t, t.Context(), db, key, start, time.Unix(201*3600, 0), 200, ErrMarkPriceReadLimit)
	}
	key = CandleStream{"mainnet", "other", "BTCUSDT", "D"}
	markReadFailureTest(t, t.Context(), db, key, start, time.Unix(86400, 0), 1, ErrMarkPriceReadDataset)
	key.Environment, key.DatasetID = "testnet", "fixture"
	marketStatement(t, db, `UPDATE momentum.mark_prices SET close=('7.'||repeat('0',64)||'1')::numeric
 WHERE environment='testnet' AND dataset_id='fixture' AND symbol='BTCUSDT' AND interval_code='D' AND open_time='1970-01-03+00'`, true)
	markReadFailureTest(t, t.Context(), db, key, start, time.Unix(3*86400, 0), 3, ErrMarkPriceRead)
	marketStatement(t, db, "ALTER TABLE momentum.datasets DROP CONSTRAINT datasets_source_check; UPDATE momentum.datasets SET source='mark-read-secret' WHERE environment='testnet' AND dataset_id='fixture'", true)
	markReadFailureTest(t, t.Context(), db, key, start, time.Unix(86400, 0), 1, ErrMarkPriceReadDataset)
	marketStatement(t, db, "ALTER TABLE momentum.datasets DROP CONSTRAINT datasets_category_check; UPDATE momentum.datasets SET source='bybit_v5',category='spot' WHERE environment='testnet' AND dataset_id='fixture'", true)
	markReadFailureTest(t, t.Context(), db, key, start, time.Unix(86400, 0), 1, ErrMarkPriceReadDataset)
	markReadFailureTest(t, t.Context(), openTestPostgres(t), key, start, time.Unix(86400, 0), 1, ErrMarkPriceRead)
	migrationResult(t, db.Close(), nil)
	markReadFailureTest(t, t.Context(), db, key, start, time.Unix(86400, 0), 1, ErrMarkPriceRead)
}

// TestReadMarkPriceSnapshot проверяет реальную блокировку, deadline и общий снимок двух SELECT.
func TestReadMarkPriceSnapshot(t *testing.T) {
	db, key := markReadFixture(t)
	key.DatasetID = "empty"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	holder, err := db.BeginTx(ctx, nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.ExecContext(ctx, "LOCK TABLE momentum.mark_prices IN ACCESS EXCLUSIVE MODE")
	migrationResult(t, err, nil)
	var pid int
	migrationResult(t, holder.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid), nil)
	type result struct {
		rows []model.MarkPriceCandle
		err  error
	}
	finished := make(chan result, 1)
	start, end := time.Unix(0, 0).UTC(), time.Unix(86400, 0).UTC()
	// Параллельный вызов позволяет второй транзакции подтвердить старый snapshot.
	go func() {
		rows, err := ReadMarkPriceRange(ctx, db, key, start, end, 1)
		finished <- result{rows, err}
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		migrationResult(t, db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked), nil)
		if blocked {
			break
		}
		select {
		case got := <-finished:
			t.Fatalf("read completed before release: %+v", got)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	deadline, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	markReadFailureTest(t, deadline, db, key, start, end, 1, context.DeadlineExceeded)
	_, err = holder.ExecContext(ctx, "INSERT INTO momentum.mark_prices VALUES('testnet','empty','BTCUSDT','D','epoch',1,1,1,1,false)")
	migrationResult(t, err, nil)
	migrationResult(t, holder.Commit(), nil)
	got := <-finished
	if got.err != nil || got.rows == nil || len(got.rows) != 0 {
		t.Fatalf("snapshot included concurrent commit: %+v", got)
	}
	rows, err := ReadMarkPriceRange(ctx, db, key, start, end, 1)
	if err != nil || len(rows) != 1 || rows[0].OpenTime != start || rows[0].Confirmed {
		t.Fatalf("fresh snapshot=%v error=%v", rows, err)
	}
}
