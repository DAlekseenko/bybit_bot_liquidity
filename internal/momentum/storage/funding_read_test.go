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

// fundingReadFixture создаёт независимую схему с нерегулярными timestamps и точными ставками.
func fundingReadFixture(t *testing.T) (*sql.DB, FundingStream) {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	_, err := db.Exec(`INSERT INTO momentum.datasets(environment,dataset_id,source,category) VALUES
 ('testnet','fixture','bybit_v5','linear'),('mainnet','fixture','bybit_v5','linear'),
 ('testnet','other','bybit_v5','linear'),('testnet','empty','bybit_v5','linear');
 INSERT INTO momentum.funding VALUES
 ('testnet','fixture','BTCUSDT','1970-01-01 00:00:09.631+00',9007199254740993.1234567890123456789),
 ('testnet','fixture','BTCUSDT','1970-01-01 00:00:00.011+00',-9007199254740993.000000000000000001),
 ('testnet','fixture','BTCUSDT','1970-01-01 00:00:00+00',0),
 ('mainnet','fixture','BTCUSDT','1970-01-01 00:00:00+00',1),
 ('testnet','other','BTCUSDT','1970-01-01 00:00:00+00',2),
 ('testnet','fixture','ETHUSDT','1970-01-01 00:00:00+00',3);`)
	migrationResult(t, err, nil)
	return db, FundingStream{Environment: "testnet", DatasetID: "fixture", Symbol: "BTCUSDT"}
}

// requireFundingReadFailure проверяет sentinel, отсутствие частичного результата и безопасные ошибки.
func requireFundingReadFailure(t *testing.T, ctx context.Context, db *sql.DB, key FundingStream, start, end time.Time, limit int, want error) {
	t.Helper()
	got, err := ReadFundingRange(ctx, db, key, start, end, limit)
	if got != nil || !errors.Is(err, ErrFunding) || !errors.Is(err, want) {
		t.Fatalf("rows=%v err=%v want=%v", got, err, want)
	}
	for _, marker := range []string{"secret-marker", "pq:", "postgres://"} {
		if strings.Contains(err.Error(), marker) {
			t.Fatalf("raw diagnostic leaked: %v", err)
		}
	}
	if want == ErrFunding && (errors.Is(err, ErrFundingDataset) || errors.Is(err, ErrFundingLimit)) {
		t.Fatalf("storage failure was reclassified: %v", err)
	}
}

// TestFundingReadCanceledContextWithoutPool проверяет приоритет отмены без БД.
func TestFundingReadCanceledContextWithoutPool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, err := ReadFundingRange(ctx, nil, FundingStream{}, time.Time{}, time.Time{}, 0)
	if rows != nil || !errors.Is(err, ErrFunding) || !errors.Is(err, context.Canceled) {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}

// TestReadFundingRangeProjection проверяет точность, порядок, обе границы и isolation ключа.
func TestReadFundingRangeProjection(t *testing.T) {
	db, key := fundingReadFixture(t)
	db.SetMaxOpenConns(1)
	_, err := db.Exec("SET TimeZone='Pacific/Honolulu'")
	migrationResult(t, err, nil)
	_, err = db.Exec("UPDATE momentum.funding SET rate=('0.'||repeat('0',75))::numeric WHERE environment='testnet' AND dataset_id='fixture' AND symbol='BTCUSDT' AND funding_time='1970-01-01 00:00:00+00'")
	migrationResult(t, err, nil)

	zone := time.FixedZone("plus3", 3*3600)
	start, end := time.UnixMilli(0).In(zone), time.UnixMilli(9631).In(zone)
	ctx := context.Background()
	got, err := ReadFundingRange(ctx, db, key, start, end, 3)
	if err != nil || len(got) != 3 {
		t.Fatalf("rows=%v err=%v", got, err)
	}
	for i, want := range []struct {
		ms   int64
		rate string
	}{{0, "0"}, {11, "-9007199254740993.000000000000000001"}, {9631, "9007199254740993.1234567890123456789"}} {
		rate, err := got[i].Rate.WireDecimal()
		if got[i].FundingTime != time.UnixMilli(want.ms).UTC() || err != nil || rate != want.rate {
			t.Fatalf("event[%d]=%v rate=%q err=%v", i, got[i], rate, err)
		}
	}

	for _, tc := range []struct {
		key  FundingStream
		rate string
	}{
		{key, "0"},
		{FundingStream{Environment: "mainnet", DatasetID: "fixture", Symbol: "BTCUSDT"}, "1"},
		{FundingStream{Environment: "testnet", DatasetID: "other", Symbol: "BTCUSDT"}, "2"},
		{FundingStream{Environment: "testnet", DatasetID: "fixture", Symbol: "ETHUSDT"}, "3"},
	} {
		got, err := ReadFundingRange(ctx, db, tc.key, start, start, 1)
		if err != nil || len(got) != 1 {
			t.Fatalf("single instant rows=%v err=%v", got, err)
		}
		rate, err := got[0].Rate.WireDecimal()
		if err != nil || rate != tc.rate || got[0].FundingTime != start.UTC() {
			t.Fatalf("wrong key/instant rate=%q err=%v", rate, err)
		}
	}

	got, err = ReadFundingRange(ctx, db, key, time.UnixMilli(12), time.UnixMilli(9630), 1)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("gap rows=%v err=%v", got, err)
	}
	emptyKey := FundingStream{Environment: "testnet", DatasetID: "empty", Symbol: "BTCUSDT"}
	if got, err := ReadFundingRange(ctx, db, emptyKey, start, end, 1); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty dataset rows=%v err=%v", got, err)
	}
}

// TestReadFundingRangeCapAndOpaqueKey проверяет предел 200 и точные opaque dataset IDs.
func TestReadFundingRangeCapAndOpaqueKey(t *testing.T) {
	db, _ := fundingReadFixture(t)
	ctx := context.Background()
	start := time.UnixMilli(0)
	for _, id := range []string{strings.Repeat("я", 128), "x'--", "\tid\t"} {
		migrationResult(t, RegisterDataset(ctx, db, "testnet", id), nil)
		_, err := db.Exec(`INSERT INTO momentum.funding
 SELECT 'testnet',$1,'BTCUSDT',TIMESTAMPTZ '1970-01-01 00:00:00+00' + n * INTERVAL '1 ms',n
 FROM generate_series(0,200) n`, id)
		migrationResult(t, err, nil)
		key := FundingStream{Environment: "testnet", DatasetID: id, Symbol: "BTCUSDT"}
		got, err := ReadFundingRange(ctx, db, key, start, time.UnixMilli(199), 200)
		if err != nil || len(got) != 200 || got[199].FundingTime != time.UnixMilli(199).UTC() {
			t.Fatalf("cap boundary len=%d err=%v", len(got), err)
		}
		requireFundingReadFailure(t, ctx, db, key, start, time.UnixMilli(200), 200, ErrFundingLimit)
	}
}

// TestReadFundingRangeInvalid проверяет аргументы до SQL и отличает отсутствие namespace от SQL-сбоя.
func TestReadFundingRangeInvalid(t *testing.T) {
	db, key := fundingReadFixture(t)
	ctx := context.Background()
	start, end := time.UnixMilli(0), time.UnixMilli(10000)
	for _, bad := range []FundingStream{
		{Environment: "paper", DatasetID: "fixture", Symbol: "BTCUSDT"},
		{Environment: "testnet", DatasetID: "", Symbol: "BTCUSDT"},
		{Environment: "testnet", DatasetID: " fixture", Symbol: "BTCUSDT"},
		{Environment: "testnet", DatasetID: "fixture ", Symbol: "BTCUSDT"},
		{Environment: "testnet", DatasetID: strings.Repeat("я", 129), Symbol: "BTCUSDT"},
		{Environment: "testnet", DatasetID: "bad\x00id", Symbol: "BTCUSDT"},
		{Environment: "testnet", DatasetID: "bad\xffid", Symbol: "BTCUSDT"},
		{Environment: "testnet", DatasetID: "fixture", Symbol: "btcusdt"},
		{Environment: "testnet", DatasetID: "fixture", Symbol: "BTCUSDC"},
	} {
		requireFundingReadFailure(t, ctx, db, bad, start, end, 1, ErrFundingArgument)
	}
	for _, tc := range []struct {
		start, end time.Time
		limit      int
	}{{start, end, 0}, {start, end, 201}, {end, start, 1}, {time.UnixMilli(-1), end, 1},
		{start.Add(time.Nanosecond), end, 1}, {start, end.Add(time.Nanosecond), 1}, {start, time.Unix(1<<63-1, 0), 1}} {
		requireFundingReadFailure(t, ctx, db, key, tc.start, tc.end, tc.limit, ErrFundingArgument)
	}
	missing := FundingStream{Environment: "mainnet", DatasetID: "other", Symbol: "BTCUSDT"}
	requireFundingReadFailure(t, ctx, db, missing, start, end, 1, ErrFundingDataset)
	requireFundingReadFailure(t, ctx, nil, key, start, end, 1, ErrFundingArgument)
	requireFundingReadFailure(t, nil, db, key, start, end, 1, ErrFundingArgument)
	key.DatasetID = "secret-marker"
	requireFundingReadFailure(t, ctx, openTestPostgres(t), key, start, end, 1, ErrFunding)
	migrationResult(t, db.Close(), nil)
	requireFundingReadFailure(t, ctx, db, key, start, end, 1, ErrFunding)
}

// TestReadFundingRangeCorruption отвергает невалидный provenance и не теряет точность молча.
func TestReadFundingRangeCorruption(t *testing.T) {
	db, key := fundingReadFixture(t)
	ctx := context.Background()
	start, end := time.UnixMilli(0), time.UnixMilli(9631)
	_, err := db.Exec(`UPDATE momentum.funding SET rate=('0.'||repeat('0',64)||'1')::numeric
 WHERE environment='testnet' AND dataset_id='fixture' AND symbol='BTCUSDT' AND funding_time='1970-01-01 00:00:09.631+00'`)
	migrationResult(t, err, nil)
	requireFundingReadFailure(t, ctx, db, key, start, end, 3, ErrFunding)
	_, err = db.Exec(`ALTER TABLE momentum.datasets DROP CONSTRAINT datasets_source_check;
 UPDATE momentum.datasets SET source='secret-marker' WHERE environment='testnet' AND dataset_id='fixture'`)
	migrationResult(t, err, nil)
	requireFundingReadFailure(t, ctx, db, key, start, end, 3, ErrFundingDataset)
}

// TestReadFundingRangeDeadline проверяет точную ошибку по реальной SQL-блокировке и повтор после release.
func TestReadFundingRangeDeadline(t *testing.T) {
	db, key := fundingReadFixture(t)
	ctx := context.Background()
	holder, err := db.BeginTx(ctx, nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.Exec("LOCK TABLE momentum.funding IN ACCESS EXCLUSIVE MODE")
	migrationResult(t, err, nil)
	start := time.UnixMilli(0)
	deadline, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	requireFundingReadFailure(t, deadline, db, key, start, start, 1, context.DeadlineExceeded)
	migrationResult(t, holder.Rollback(), nil)
	got, err := ReadFundingRange(ctx, db, key, start, start, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("read after release rows=%v err=%v", got, err)
	}
}

// TestReadFundingRangeSnapshot доказывает общий snapshot provenance и funding rows.
func TestReadFundingRangeSnapshot(t *testing.T) {
	db, key := fundingReadFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	holder, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.ExecContext(ctx, "LOCK TABLE momentum.funding IN ACCESS EXCLUSIVE MODE")
	migrationResult(t, err, nil)
	var holderPID int
	migrationResult(t, holder.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&holderPID), nil)

	start, end := time.UnixMilli(12).UTC(), time.UnixMilli(13).UTC()
	type readResult struct {
		rows []model.FundingEvent
		err  error
	}
	result := make(chan readResult, 1)
	// Читатель сканирует provenance и ждёт блокировку таблицы funding.
	go func() {
		rows, err := ReadFundingRange(ctx, db, key, start, end, 1)
		result <- readResult{rows, err}
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		err = db.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM pg_catalog.pg_stat_activity
 WHERE datname=current_database() AND $1=ANY(pg_catalog.pg_blocking_pids(pid)))`, holderPID).Scan(&blocked)
		migrationResult(t, err, nil)
		if blocked {
			break
		}
		select {
		case got := <-result:
			t.Fatalf("read completed before funding lock released: %+v", got)
		case <-ctx.Done():
			t.Fatalf("funding read never waited on holder: %v", ctx.Err())
		case <-tick.C:
		}
	}

	page := model.FundingPage{
		Environment: key.Environment, Source: "bybit_v5", Category: "linear", Symbol: key.Symbol,
		Endpoint:  "https://api-testnet.bybit.com/v5/market/funding/history",
		StartTime: start, EndTime: end, ServerTime: end,
		Events: []model.FundingEvent{{FundingTime: end, Rate: model.Zero()}},
	}
	migrationResult(t, AppendFundingPage(ctx, holder, key, page), nil)
	migrationResult(t, holder.Commit(), nil)
	select {
	case got := <-result:
		if got.err != nil || got.rows == nil || len(got.rows) != 0 {
			t.Fatalf("read snapshot included the concurrent commit: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	got, err := ReadFundingRange(ctx, db, key, start, end, 1)
	if err != nil || len(got) != 1 || got[0].FundingTime != end {
		t.Fatalf("fresh read missed committed event: %v err=%v", got, err)
	}
	rate, err := got[0].Rate.WireDecimal()
	if err != nil || rate != "0" {
		t.Fatalf("fresh read lost explicit zero: %q err=%v", rate, err)
	}
}
