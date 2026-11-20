package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// fundingJobTime создаёт независимый UTC timestamp для nullable metadata.
func fundingJobTime(ms int64) *time.Time {
	value := time.UnixMilli(ms).UTC()
	return &value
}

// fundingJobCount сохраняет различие отсутствующего и нулевого page count.
func fundingJobCount(value int) *int { return &value }

// fundingJobStates возвращает точные пять форм metadata по #304 без истории/cadence.
func fundingJobStates() []StoredFundingImport {
	key := FundingImportWindow{FundingStream{"testnet", "fixture", "BTCUSDT"}, *fundingJobTime(0), *fundingJobTime(100)}
	return []StoredFundingImport{
		{Window: key, Action: "pending", NextEnd: fundingJobTime(100)},
		{
			Window: key, Revision: 1, Action: model.FundingPageContinue,
			NextEnd: fundingJobTime(49), LastRequestEnd: fundingJobTime(100),
			LastServerTime: fundingJobTime(80), LastPageOldest: fundingJobTime(50), LastPageCount: fundingJobCount(1),
		},
		{
			Window: key, Revision: math.MaxInt64, Action: model.FundingPageLowerReached,
			LastRequestEnd: fundingJobTime(10), LastServerTime: fundingJobTime(1),
			LastPageOldest: fundingJobTime(0), LastPageCount: fundingJobCount(200),
		},
		{
			Window: key, Revision: 2, Action: model.FundingPageEmpty,
			LastRequestEnd: fundingJobTime(100), LastServerTime: fundingJobTime(50), LastPageCount: fundingJobCount(0),
		},
		{
			Window: key, Revision: 3, Action: model.FundingPageSourceBoundary,
			LastRequestEnd: fundingJobTime(2), LastServerTime: fundingJobTime(1),
			LastPageOldest: fundingJobTime(1), LastPageCount: fundingJobCount(1),
		},
	}
}

// fundingJobFixture подготавливает временную базу с действующим каталогом миграций.
func fundingJobFixture(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(t.Context(), db), nil)
	for _, environment := range []string{"testnet", "mainnet"} {
		migrationResult(t, RegisterDataset(t.Context(), db, environment, "fixture"), nil)
	}
	return db
}

// fundingJobInsert сохраняет только test fixture согласованной формы metadata.
func fundingJobInsert(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, row StoredFundingImport) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `INSERT INTO momentum.funding_import_progress
 (environment,dataset_id,symbol,lower_time,upper_time,revision,action,next_end,last_request_end,last_server_time,last_page_oldest,last_page_count)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, row.Window.Stream.Environment, row.Window.Stream.DatasetID, row.Window.Stream.Symbol, row.Window.LowerTime, row.Window.UpperTime, row.Revision, string(row.Action), row.NextEnd, row.LastRequestEnd, row.LastServerTime, row.LastPageOldest, row.LastPageCount)
	migrationResult(t, err, nil)
}

// fundingJobError проверяет общий sentinel, typed cause и безопасную диагностику.
func fundingJobError(t *testing.T, err, want error) {
	t.Helper()
	if err == nil || !errors.Is(err, ErrFunding) || !errors.Is(err, want) {
		t.Fatalf("err=%v want=%v", err, want)
	}
	for _, marker := range []string{"secret-marker", "pq:", "postgres://", "funding_import_progress"} {
		if strings.Contains(err.Error(), marker) {
			t.Fatal("unsafe diagnostic")
		}
	}
}

// requireFundingJobReadFailure проверяет typed отказ и отсутствие любых partial metadata.
func requireFundingJobReadFailure(t *testing.T, ctx context.Context, db *sql.DB, key FundingImportWindow, want error) {
	t.Helper()
	got, err := ReadFundingImport(ctx, db, key)
	fundingJobError(t, err, want)
	if !reflect.ValueOf(got).IsZero() {
		t.Fatal("partial metadata on failure")
	}
}

// TestFundingImportShape проверяет полную форму без PostgreSQL и не выдаёт readiness.
func TestFundingImportShape(t *testing.T) {
	for _, row := range fundingJobStates() {
		if !validStoredFundingImport(row) {
			t.Fatalf("valid %s rejected", row.Action)
		}
	}
	for _, change := range []func(*StoredFundingImport){
		func(r *StoredFundingImport) { r.Revision = -1 }, func(r *StoredFundingImport) { r.Revision = 0 },
		func(r *StoredFundingImport) { r.Action = "complete" }, func(r *StoredFundingImport) { r.NextEnd = fundingJobTime(48) },
		func(r *StoredFundingImport) { r.LastRequestEnd = nil }, func(r *StoredFundingImport) { r.LastServerTime = nil },
		func(r *StoredFundingImport) { r.LastPageOldest = nil }, func(r *StoredFundingImport) { r.LastPageCount = nil },
		func(r *StoredFundingImport) { r.LastPageCount = fundingJobCount(0) }, func(r *StoredFundingImport) { r.LastPageCount = fundingJobCount(201) },
		func(r *StoredFundingImport) { r.LastServerTime = fundingJobTime(49) }, func(r *StoredFundingImport) { r.NextEnd = fundingJobTime(0) },
		func(r *StoredFundingImport) { r.LastRequestEnd = fundingJobTime(101) },
		func(r *StoredFundingImport) { v := r.LastServerTime.Add(time.Nanosecond); r.LastServerTime = &v },
		func(r *StoredFundingImport) { r.Window.Stream.Environment = "paper" },
	} {
		row := fundingJobStates()[1]
		change(&row)
		if validStoredFundingImport(row) {
			t.Fatal("invalid metadata accepted")
		}
	}
	row := fundingJobStates()[0]
	row.LastPageCount = fundingJobCount(0)
	if validStoredFundingImport(row) {
		t.Fatal("pending gained observation")
	}
	row = fundingJobStates()[3]
	row.LastPageOldest = fundingJobTime(0)
	if validStoredFundingImport(row) {
		t.Fatal("empty gained event")
	}
	row = fundingJobStates()[4]
	row.Window.LowerTime = *fundingJobTime(1)
	if validStoredFundingImport(row) {
		t.Fatal("source boundary gained lower")
	}
	db, err := sql.Open("postgres", "")
	migrationResult(t, err, nil)
	migrationResult(t, db.Close(), nil)
	key := fundingJobStates()[0].Window
	maximum := time.Date(294276, 12, 31, 23, 59, 59, 999000000, time.UTC)
	for _, bad := range []FundingImportWindow{{}, {key.Stream, *fundingJobTime(0), *fundingJobTime(0)}, {key.Stream, *fundingJobTime(2), *fundingJobTime(1)}, {key.Stream, time.UnixMilli(-1), key.UpperTime}, {key.Stream, key.LowerTime.Add(time.Nanosecond), key.UpperTime}, {key.Stream, key.LowerTime, maximum.Add(time.Millisecond)}, {key.Stream, key.LowerTime, maximum.Add(time.Nanosecond)}} {
		fundingJobError(t, InitFundingImport(t.Context(), db, bad), ErrFundingArgument)
		requireFundingJobReadFailure(t, t.Context(), db, bad, ErrFundingArgument)
	}
	if _, err := fundingImportJobArgs(FundingImportWindow{key.Stream, maximum, maximum}); err != nil {
		t.Fatal("maximum inclusive millisecond rejected")
	}
	fundingJobError(t, InitFundingImport(nilFundingContext(), db, key), ErrFundingArgument)
	fundingJobError(t, InitFundingImport(t.Context(), nil, key), ErrFundingArgument)
	fundingJobError(t, InitFundingImport(t.Context(), db, key), ErrFunding)
	requireFundingJobReadFailure(t, nil, db, key, ErrFundingArgument)
	requireFundingJobReadFailure(t, t.Context(), nil, key, ErrFundingArgument)
	requireFundingJobReadFailure(t, t.Context(), db, key, ErrFunding)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	fundingJobError(t, InitFundingImport(ctx, db, key), context.Canceled)
	requireFundingJobReadFailure(t, ctx, db, key, context.Canceled)
}

// TestFundingImportInit проверяет no-reset pending/advanced и точные scopes/границы.
func TestFundingImportInit(t *testing.T) {
	db := fundingJobFixture(t)
	key := fundingJobStates()[0].Window
	migrationResult(t, InitFundingImport(t.Context(), db, key), nil)
	got, err := ReadFundingImport(t.Context(), db, key)
	if err != nil || !reflect.DeepEqual(got, fundingJobStates()[0]) {
		t.Fatal("wrong initialized progress")
	}
	before := migrationValue(t, db, "SELECT xmin::text FROM momentum.funding_import_progress")
	zone := time.FixedZone("east", 10800)
	key.LowerTime, key.UpperTime = key.LowerTime.In(zone), key.UpperTime.In(zone)
	fundingJobError(t, InitFundingImport(t.Context(), db, key), ErrFundingImportNotInserted)
	migrationValue(t, db, "SELECT xmin::text FROM momentum.funding_import_progress", before)
	_, err = db.Exec(`UPDATE momentum.funding_import_progress SET revision=2,action='empty',next_end=NULL,last_request_end=upper_time,last_server_time='epoch'::timestamptz+INTERVAL '50 ms',last_page_count=0`)
	migrationResult(t, err, nil)
	before = migrationValue(t, db, "SELECT row_to_json(p)::text FROM momentum.funding_import_progress p")
	fundingJobError(t, InitFundingImport(t.Context(), db, key), ErrFundingImportNotInserted)
	migrationValue(t, db, "SELECT row_to_json(p)::text FROM momentum.funding_import_progress p", before)
	for _, change := range []func(*FundingImportWindow){
		func(k *FundingImportWindow) { k.Stream.Environment = "mainnet" },
		func(k *FundingImportWindow) { k.Stream.Symbol = "ETHUSDT" },
		func(k *FundingImportWindow) { k.LowerTime, k.UpperTime = *fundingJobTime(20), *fundingJobTime(20) },
	} {
		other := key
		change(&other)
		migrationResult(t, InitFundingImport(t.Context(), db, other), nil)
	}
	id := strings.Repeat("я", 128)
	migrationResult(t, RegisterDataset(t.Context(), db, "testnet", id), nil)
	key.Stream.DatasetID = id
	migrationResult(t, InitFundingImport(t.Context(), db, key), nil)
	key.Stream.DatasetID = "secret-marker"
	fundingJobError(t, InitFundingImport(t.Context(), db, key), ErrFundingImportNotInserted)
	requireFundingJobReadFailure(t, t.Context(), db, key, ErrFundingImportMissing)
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding_import_progress", "5")
}

// TestFundingImportRead проверяет все состояния, nullable fields, max BIGINT и независимый output.
func TestFundingImportRead(t *testing.T) {
	db := fundingJobFixture(t)
	for i, row := range fundingJobStates() {
		row.Window.UpperTime = row.Window.UpperTime.Add(time.Duration(i) * time.Millisecond)
		fundingJobInsert(t, db, row)
		got, err := ReadFundingImport(t.Context(), db, row.Window)
		if err != nil || !reflect.DeepEqual(got, row) {
			t.Fatalf("changed state %s: %v", row.Action, err)
		}
		for _, stamp := range []*time.Time{got.NextEnd, got.LastRequestEnd, got.LastServerTime, got.LastPageOldest} {
			if stamp != nil && stamp.Location() != time.UTC {
				t.Fatal("timestamp is not UTC")
			}
			if stamp != nil {
				*stamp = time.Time{}
			}
		}
		again, err := ReadFundingImport(t.Context(), db, row.Window)
		if err != nil || !reflect.DeepEqual(again, row) {
			t.Fatal("shared output metadata")
		}
	}
}

// TestFundingImportCorrupt проверяет видимый corruption и SQL failures без partial metadata.
func TestFundingImportCorrupt(t *testing.T) {
	db := fundingJobFixture(t)
	row := fundingJobStates()[0]
	fundingJobInsert(t, db, row)
	_, err := db.Exec(`DO $$DECLARE c RECORD;BEGIN FOR c IN SELECT conname FROM pg_constraint WHERE conrelid='momentum.funding_import_progress'::regclass AND contype='c' LOOP EXECUTE format('ALTER TABLE momentum.funding_import_progress DROP CONSTRAINT %I',c.conname);END LOOP;END$$;
 UPDATE momentum.funding_import_progress SET next_end='infinity'`)
	migrationResult(t, err, nil)
	requireFundingJobReadFailure(t, t.Context(), db, row.Window, ErrFundingImportCorrupt)
	_, err = db.Exec(`UPDATE momentum.funding_import_progress SET next_end=upper_time;ALTER TABLE momentum.datasets DROP CONSTRAINT datasets_source_check;UPDATE momentum.datasets SET source='secret-marker'`)
	migrationResult(t, err, nil)
	requireFundingJobReadFailure(t, t.Context(), db, row.Window, ErrFundingImportCorrupt)
	empty := openTestPostgres(t)
	fundingJobError(t, InitFundingImport(t.Context(), empty, row.Window), ErrFundingImportInitUnknown)
	requireFundingJobReadFailure(t, t.Context(), empty, row.Window, ErrFunding)
}

// TestFundingImportConcurrent проверяет ожидание conflict и сохранение advanced строки.
func TestFundingImportConcurrent(t *testing.T) {
	db := fundingJobFixture(t)
	row := fundingJobStates()[3]
	holder, err := db.BeginTx(t.Context(), nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	fundingJobInsert(t, holder, row)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- InitFundingImport(ctx, db, row.Window) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		blocked := migrationValue(t, db, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND cardinality(pg_blocking_pids(pid))>0)")
		if blocked == "true" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initializer did not reach real unique lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	migrationResult(t, holder.Commit(), nil)
	fundingJobError(t, <-result, ErrFundingImportNotInserted)
	got, err := ReadFundingImport(t.Context(), db, row.Window)
	if err != nil || !reflect.DeepEqual(got, row) {
		t.Fatal("concurrent init reset advanced state")
	}
	migrationValue(t, db, "SELECT count(*) FROM momentum.funding_import_progress", "1")
}

// TestFundingImportDeadline проверяет реальные lock deadlines и подтверждённое отсутствие до retry.
func TestFundingImportDeadline(t *testing.T) {
	db := fundingJobFixture(t)
	key := fundingJobStates()[0].Window
	holder, err := db.BeginTx(t.Context(), nil)
	migrationResult(t, err, nil)
	defer holder.Rollback()
	_, err = holder.Exec("LOCK TABLE momentum.funding_import_progress IN ACCESS EXCLUSIVE MODE")
	migrationResult(t, err, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	err = InitFundingImport(ctx, db, key)
	cancel()
	fundingJobError(t, err, ErrFundingImportInitUnknown)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("init deadline lost")
	}
	ctx, cancel = context.WithTimeout(t.Context(), 150*time.Millisecond)
	requireFundingJobReadFailure(t, ctx, db, key, context.DeadlineExceeded)
	cancel()
	migrationResult(t, holder.Rollback(), nil)
	requireFundingJobReadFailure(t, t.Context(), db, key, ErrFundingImportMissing)
	migrationResult(t, InitFundingImport(t.Context(), db, key), nil)
}
