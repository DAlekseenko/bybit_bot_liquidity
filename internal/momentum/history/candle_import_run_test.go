package history

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/storage"
)

// TestRunCandleImportBudget проверяет предел страниц и возобновление по БД.
func TestRunCandleImportBudget(t *testing.T) {
	db, resumed := candleImportTestDB(t)
	lower := time.Unix(86400, 0).UTC()
	for _, interval := range []struct {
		code, environment string
		step              time.Duration
	}{{"D", "testnet", 24 * time.Hour}, {"60", "mainnet", time.Hour}} {
		t.Run(interval.code, func(t *testing.T) {
			ctx := t.Context()
			key := candleImportTestJob(t, db, interval.environment, interval.code, lower, interval.step)
			middle, latest := lower.Add(interval.step), lower.Add(2*interval.step)
			observed := key.UpperOpen.Add(time.Second)
			pages := []struct {
				pool      *sql.DB
				end, open time.Time
			}{
				{db, key.UpperOpen.Add(-time.Millisecond), latest},
				{db, latest.Add(-time.Millisecond), middle},
				{resumed, middle.Add(-time.Millisecond), lower},
			}
			calls := 0
			source := func(ctx context.Context, symbol, interval string, startMs, endMs int64) (model.CandlePage, error) {
				t.Helper()
				if calls >= len(pages) {
					t.Fatal("unexpected source call")
				}
				page := pages[calls]
				calls++
				fetch := candleImportTestSource(t, page.pool, key, page.end, func() (int, string) {
					return 200, candleImportTestJSON(key, observed, page.open)
				})
				return fetch(ctx, symbol, interval, startMs, endMs)
			}
			got, err := RunCandleImport(ctx, db, source, key, 2)
			candleImportTestOK(t, err)
			progress := storage.CandleImportCheckpoint{Revision: 2, NextEnd: middle.Add(-time.Millisecond), State: "progress"}
			durable := candleImportTestDurable(t, resumed, key, progress, "", observed, middle, latest)
			if calls != 2 || !reflect.DeepEqual(got, durable) {
				t.Fatalf("budget: calls=%d result=%+v durable=%+v", calls, got, durable)
			}

			got, err = RunCandleImport(ctx, resumed, source, key, 4)
			candleImportTestOK(t, err)
			complete := storage.CandleImportCheckpoint{Revision: 3, NextEnd: lower.Add(-time.Millisecond), State: "complete"}
			durable = candleImportTestDurable(t, db, key, complete, "", observed, lower, middle, latest)
			if calls != 3 || !reflect.DeepEqual(got, durable) {
				t.Fatalf("resume: calls=%d result=%+v durable=%+v", calls, got, durable)
			}
			replay, err := RunCandleImport(ctx, db, source, key, 4)
			candleImportTestOK(t, err)
			if calls != 3 || !reflect.DeepEqual(replay, durable) {
				t.Fatalf("complete replay: calls=%d result=%+v", calls, replay)
			}
		})
	}
}

// TestRunCandleImportStops проверяет сохранение первой страницы при отказе второй.
func TestRunCandleImportStops(t *testing.T) {
	db, other := candleImportTestDB(t)
	for _, tc := range []struct {
		name string
		want error
	}{
		{"HTTP", ErrCandleImportFetch},
		{"cancel", context.Canceled},
		{"stale", storage.ErrCandleImportStale},
		{"commit", ErrCandleImportCommitUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			lower := time.Unix(86400, 0).UTC()
			key := candleImportTestJob(t, db, "testnet", "60", lower, time.Hour)
			middle, latest := lower.Add(time.Hour), lower.Add(2*time.Hour)
			observed := key.UpperOpen.Add(time.Second)
			want := storage.CandleImportCheckpoint{Revision: 1, NextEnd: latest.Add(-time.Millisecond), State: "progress"}
			saved := []time.Time{latest}

			if tc.name == "commit" {
				_, err := db.ExecContext(ctx, `CREATE FUNCTION momentum.reject_run_commit() RETURNS trigger LANGUAGE plpgsql AS
				 $$BEGIN RAISE EXCEPTION '`+candleImportTestMarker+`'; END$$;
				 CREATE CONSTRAINT TRIGGER reject_run_commit AFTER UPDATE ON momentum.candle_import_progress
				 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.revision = 2) EXECUTE FUNCTION momentum.reject_run_commit()`)
				candleImportTestOK(t, err)
			}
			calls := 0
			source := func(ctx context.Context, symbol, interval string, startMs, endMs int64) (model.CandlePage, error) {
				t.Helper()
				calls++
				if calls > 2 {
					t.Fatal("run retried a failed step")
				}
				end, open := key.UpperOpen.Add(-time.Millisecond), latest
				if calls == 2 {
					end, open = latest.Add(-time.Millisecond), middle
				}
				body := candleImportTestJSON(key, observed, open)
				fetch := candleImportTestSource(t, db, key, end, func() (int, string) {
					if calls == 2 && tc.name == "HTTP" {
						return 503, candleImportTestMarker
					}
					if calls == 2 && tc.name == "stale" {
						winner := candleImportTestSource(t, other, key, end, func() (int, string) {
							return 200, body
						})
						job, err := StepCandleImport(ctx, other, winner, key)
						candleImportTestOK(t, err)
						want = storage.CandleImportCheckpoint{Revision: 2, NextEnd: middle.Add(-time.Millisecond), State: "progress"}
						if job.Checkpoint != want {
							t.Fatalf("winner checkpoint: %+v", job.Checkpoint)
						}
						saved = []time.Time{middle, latest}
					}
					return 200, body
				})
				page, err := fetch(ctx, symbol, interval, startMs, endMs)
				if calls == 2 && tc.name == "cancel" {
					candleImportTestOK(t, err)
					cancel()
				}
				return page, err
			}
			got, err := RunCandleImport(ctx, db, source, key, 4)
			candleImportTestFailure(t, got, err, tc.want)
			if calls != 2 {
				t.Fatalf("got %d source calls, want 2", calls)
			}
			candleImportTestDurable(t, other, key, want, "", observed, saved...)
		})
	}
}

// TestRunCandleImportBlocked проверяет причину блокировки и повтор без HTTP.
func TestRunCandleImportBlocked(t *testing.T) {
	db, other := candleImportTestDB(t)
	for _, tc := range []struct {
		reason string
		lower  time.Time
		calls  int
	}{
		{"empty", time.Unix(86400, 0).UTC(), 1},
		{"source_boundary", time.Unix(0, 0).UTC(), 0},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			ctx := t.Context()
			key := candleImportTestJob(t, db, "testnet", "60", tc.lower, time.Hour)
			clock, end := key.UpperOpen.Add(time.Second), key.UpperOpen.Add(-time.Millisecond)
			calls := 0
			source := candleImportTestSource(t, db, key, end, func() (int, string) {
				calls++
				return 200, candleImportTestJSON(key, clock)
			})
			got, err := RunCandleImport(ctx, db, source, key, 3)
			candleImportTestOK(t, err)
			replay, err := RunCandleImport(ctx, db, source, key, 3)
			candleImportTestOK(t, err)
			if tc.calls == 0 {
				clock = time.Time{}
			}
			want := storage.CandleImportCheckpoint{Revision: 1, NextEnd: end, State: "blocked"}
			durable := candleImportTestDurable(t, other, key, want, tc.reason, clock)
			if calls != tc.calls || !reflect.DeepEqual(got, durable) || !reflect.DeepEqual(replay, durable) {
				t.Fatalf("blocked: calls=%d result=%+v replay=%+v durable=%+v", calls, got, replay, durable)
			}
		})
	}
}

// TestRunCandleImportArguments проверяет отказ до обращения к БД или источнику.
func TestRunCandleImportArguments(t *testing.T) {
	ctx, db := t.Context(), new(sql.DB)
	source := func(context.Context, string, string, int64, int64) (model.CandlePage, error) {
		t.Helper()
		t.Fatal("unexpected source call")
		return model.CandlePage{}, nil
	}
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		db     *sql.DB
		source CandleImportPageSource
		steps  int
	}{
		{"zero", ctx, db, source, 0},
		{"negative", ctx, db, source, -1},
		{"nil-context", nil, db, source, 1},
		{"nil-db", ctx, nil, source, 1},
		{"nil-source", ctx, db, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RunCandleImport(tc.ctx, tc.db, tc.source, storage.CandleImportWindow{}, tc.steps)
			candleImportTestFailure(t, got, err, storage.ErrCandleArgument)
		})
	}
}

// TestRunCandleImportUnavailable проверяет отказ до API при отмене и отсутствии задания.
func TestRunCandleImportUnavailable(t *testing.T) {
	db, _ := candleImportTestDB(t)
	key := candleImportTestJob(t, db, "testnet", "60", time.Unix(86400, 0).UTC(), time.Hour)
	source := func(context.Context, string, string, int64, int64) (model.CandlePage, error) {
		t.Helper()
		t.Fatal("unexpected source call")
		return model.CandlePage{}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := RunCandleImport(ctx, db, source, key, 3)
	candleImportTestFailure(t, got, err, context.Canceled)
	key.Stream.DatasetID += "-absent"
	got, err = RunCandleImport(t.Context(), db, source, key, 3)
	candleImportTestFailure(t, got, err, storage.ErrCandleImportMissing)
}
