package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange/bybit"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/storage"
)

const candleImportTestMarker = "redact-this-candle-import-marker"

type candleImportTransport func(*http.Request) (*http.Response, error)

// RoundTrip передаёт запрос подставному transport без подключения к Bybit.
func (f candleImportTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// candleImportTestDB создаёт уникальную БД и два независимых пула на disposable сервере.
func candleImportTestDB(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("MOMENTUM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL opt-in отсутствует")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(u.Path, "/momentum_test_") || strings.Count(u.Path, "/") != 1 {
		t.Fatal("expected disposable loopback PostgreSQL")
	}
	ctx := t.Context()
	admin, err := storage.OpenMomentumPostgres(ctx, dsn)
	candleImportTestOK(t, err)
	defer admin.Close()
	name := "momentum_test_import_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
	candleImportTestOK(t, err)
	u.Path, u.RawPath = "/"+name, ""
	var pools [2]*sql.DB
	for i := range pools {
		pool, err := storage.OpenMomentumPostgres(ctx, u.String())
		candleImportTestOK(t, err)
		t.Cleanup(func() { _ = pool.Close() })
		pool.SetMaxOpenConns(1)
		pools[i] = pool
	}
	candleImportTestOK(t, storage.MigrateMomentumPostgres(ctx, pools[0]))
	return pools[0], pools[1]
}

// candleImportTestJob создаёт отдельное окно из трёх свечей.
func candleImportTestJob(t *testing.T, db *sql.DB, environment, interval string, lower time.Time, step time.Duration) storage.CandleImportWindow {
	t.Helper()
	ctx := t.Context()
	key := storage.CandleImportWindow{
		Stream:    storage.CandleStream{Environment: environment, DatasetID: t.Name(), Symbol: "HYPEUSDT", IntervalCode: interval},
		LowerOpen: lower, UpperOpen: lower.Add(3 * step),
	}
	candleImportTestOK(t, storage.RegisterDataset(ctx, db, environment, key.Stream.DatasetID))
	candleImportTestOK(t, storage.InitCandleImport(ctx, db, key))
	return key
}

// candleImportTestOK завершает проверку при неожиданной ошибке.
func candleImportTestOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// candleImportTestFailure проверяет sentinel, redaction и отсутствие результата на ошибке.
func candleImportTestFailure(t *testing.T, got storage.StoredCandleImport, err, want error) {
	t.Helper()
	if got != (storage.StoredCandleImport{}) || !errors.Is(err, want) || strings.Contains(err.Error(), candleImportTestMarker) {
		t.Fatalf("result=%+v error=%v, want %v and zero", got, err, want)
	}
}

// candleImportTestDurable сверяет checkpoint, source clock и точный отсортированный SQL payload.
func candleImportTestDurable(t *testing.T, db *sql.DB, key storage.CandleImportWindow, want storage.CandleImportCheckpoint, reason string, observed time.Time, opens ...time.Time) storage.StoredCandleImport {
	t.Helper()
	ctx := t.Context()
	got, err := storage.ReadCandleImport(ctx, db, key)
	candleImportTestOK(t, err)
	if got.Checkpoint != want || got.Reason != reason || (got.LastServerTime == nil) != observed.IsZero() ||
		(got.LastServerTime != nil && *got.LastServerTime != observed) {
		t.Fatalf("unexpected durable state: %+v", got)
	}
	rows, err := storage.ReadCandleRange(ctx, db, key.Stream, key.LowerOpen, key.UpperOpen, 3)
	candleImportTestOK(t, err)
	if len(rows) != len(opens) {
		t.Fatalf("stored rows=%d, want %d", len(rows), len(opens))
	}
	for i, row := range rows {
		gotFields := [...]model.Decimal{row.Open, row.High, row.Low, row.Close, row.Volume}
		for n, text := range [...]string{"1.01", "2.02", "0.000003", "1.24", "9007199254740993.12"} {
			wire, err := gotFields[n].WireDecimal()
			if err != nil || wire != text {
				t.Fatalf("inexact candle field %d: %q (%v)", n, wire, err)
			}
		}
		if row.OpenTime != opens[i] || !row.Confirmed {
			t.Fatalf("unexpected candle timestamp or confirmation: %+v", row)
		}
	}
	return got
}

// candleImportTestSource проверяет реальный unsigned запрос и задаёт ответы fixture.
func candleImportTestSource(t *testing.T, db *sql.DB, key storage.CandleImportWindow, end time.Time, response func() (int, string)) CandleImportPageSource {
	t.Helper()
	host := map[string]string{"mainnet": "api.bybit.com", "testnet": "api-testnet.bybit.com"}[key.Stream.Environment]
	query := url.Values{"category": {"linear"}, "symbol": {key.Stream.Symbol}, "interval": {key.Stream.IntervalCode}, "limit": {"200"},
		"start": {strconv.FormatInt(key.LowerOpen.UnixMilli()-1, 10)}, "end": {strconv.FormatInt(end.UnixMilli(), 10)}}
	origin := "https://" + host
	wantURL := origin + "/v5/market/kline?" + query.Encode()
	client, err := bybit.NewClient(origin, &http.Client{Transport: candleImportTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.String() != wantURL ||
			r.Header.Get("X-BAPI-SIGN") != "" || r.Header.Get("X-BAPI-API-KEY") != "" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected public request: %v %v", r.Method, r.URL)
		}
		probe, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		candleImportTestOK(t, db.PingContext(probe))
		status, body := response()
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	candleImportTestOK(t, err)
	return client.GetMomentumCandlePage
}

// candleImportTestJSON строит ответ с точными полями и переданным source clock.
func candleImportTestJSON(key storage.CandleImportWindow, observed time.Time, opens ...time.Time) string {
	rows := make([]string, 0, len(opens))
	for _, open := range opens {
		rows = append(rows, fmt.Sprintf(`["%d","1.01","2.02","0.000003","1.24","9007199254740993.12","0"]`, open.UnixMilli()))
	}
	return fmt.Sprintf(`{"retCode":0,"time":%d,"result":{"symbol":%q,"category":"linear","list":[%s]}}`, observed.UnixMilli(), key.Stream.Symbol, strings.Join(rows, ","))
}

// TestStepCandleImportResume проверяет durable pagination, повтор и HTTP сбой между страницами.
func TestStepCandleImportResume(t *testing.T) {
	db, resumed := candleImportTestDB(t)
	ctx, lower := context.Background(), time.Unix(86400, 0).UTC()
	for _, interval := range []struct {
		code, environment string
		width             time.Duration
	}{{"D", "testnet", 24 * time.Hour}, {"60", "mainnet", time.Hour}} {
		t.Run(interval.code, func(t *testing.T) {
			key := candleImportTestJob(t, db, interval.environment, interval.code, lower, interval.width)
			early, latest := lower.Add(interval.width), lower.Add(2*interval.width)
			observed := key.UpperOpen.Add(137 * time.Millisecond)
			part := storage.CandleImportCheckpoint{Revision: 1, NextEnd: latest.Add(-time.Millisecond), State: "progress"}
			final := storage.CandleImportCheckpoint{Revision: 2, NextEnd: lower.Add(-time.Millisecond), State: "complete"}
			end, calls := key.UpperOpen.Add(-time.Millisecond), 0
			for _, round := range []struct {
				pool           *sql.DB
				status         int
				fetched, saved []time.Time
				want           storage.CandleImportCheckpoint
			}{
				{db, 200, []time.Time{latest}, []time.Time{latest}, part},
				{resumed, 503, nil, []time.Time{latest}, part},
				{resumed, 200, []time.Time{early, lower}, []time.Time{lower, early, latest}, final},
				{resumed, 0, nil, []time.Time{lower, early, latest}, final},
			} {
				before := calls
				source := candleImportTestSource(t, round.pool, key, end, func() (int, string) {
					calls++
					if round.status == 503 {
						return 503, candleImportTestMarker
					}
					return round.status, candleImportTestJSON(key, observed, round.fetched...)
				})
				got, err := StepCandleImport(ctx, round.pool, source, key)
				if calls-before > 1 || (calls == before) != (round.status == 0) {
					t.Fatalf("step status=%d made %d requests", round.status, calls-before)
				}
				if round.status == 503 {
					candleImportTestFailure(t, got, err, ErrCandleImportFetch)
				} else if err != nil || got.Window != key || got.Checkpoint != round.want || got.Reason != "" || got.LastServerTime == nil || *got.LastServerTime != observed {
					t.Fatalf("step result: %+v, err %v", got, err)
				}
				candleImportTestDurable(t, resumed, key, round.want, "", observed, round.saved...)
				end = round.want.NextEnd
			}
		})
	}
}

// TestStepCandleImportTransitions проверяет terminal replay, отмену, stale и неизвестный Commit.
func TestStepCandleImportTransitions(t *testing.T) {
	db, other := candleImportTestDB(t)
	for _, tc := range []struct {
		name, reason string
		want         error
		calls        int
	}{
		{"empty", "empty", nil, 1}, {"epoch", "source_boundary", nil, 0}, {"nil-source", "", storage.ErrCandleArgument, 0},
		{"missing", "", storage.ErrCandleImportMissing, 0}, {"exhausted", "", storage.ErrCandleImportState, 0},
		{"invalid-key", "", storage.ErrCandleArgument, 0},
		{"canceled-before", "", context.Canceled, 0}, {"canceled-fetch", "", context.Canceled, 1},
		{"stale", "", storage.ErrCandleImportStale, 1}, {"commit", "", ErrCandleImportCommitUnknown, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			const step = time.Hour
			lower := time.Unix(86400, 0).UTC()
			if tc.name == "epoch" {
				lower = time.Unix(0, 0).UTC()
			}
			key := candleImportTestJob(t, db, "testnet", "60", lower, step)
			end := key.UpperOpen.Add(-time.Millisecond)
			want := storage.CandleImportCheckpoint{NextEnd: end, State: "pending"}
			latest, observed := lower.Add(2*step), key.UpperOpen.Add(time.Second)
			var storedClock time.Time
			var fetched, stored []time.Time
			lookup := key
			switch tc.name {
			case "missing":
				lookup.Stream.DatasetID = "absent"
			case "invalid-key":
				lookup.Stream.IntervalCode = "1"
			case "exhausted":
				want = storage.CandleImportCheckpoint{Revision: math.MaxInt64, NextEnd: lower.Add(step - time.Millisecond), State: "progress"}
				_, err := db.Exec(`UPDATE momentum.candle_import_progress SET state='progress',revision=$1,next_end=$2,last_server_time=$3 WHERE dataset_id=$4`,
					want.Revision, want.NextEnd, observed, key.Stream.DatasetID)
				candleImportTestOK(t, err)
				storedClock = observed
			case "canceled-before":
				cancel()
			case "empty":
				storedClock = observed
			case "stale":
				fetched, stored, storedClock = []time.Time{latest}, []time.Time{latest}, observed
				want = storage.CandleImportCheckpoint{Revision: 1, NextEnd: latest.Add(-time.Millisecond), State: "progress"}
			case "commit":
				fetched = []time.Time{latest}
				_, err := db.Exec(`CREATE FUNCTION momentum.reject_import_commit() RETURNS trigger LANGUAGE plpgsql AS
				 $$BEGIN RAISE EXCEPTION '` + candleImportTestMarker + `'; END$$;
				 CREATE CONSTRAINT TRIGGER reject_import_commit AFTER UPDATE ON momentum.candle_import_progress
				 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION momentum.reject_import_commit()`)
				candleImportTestOK(t, err)
			}
			calls := 0
			body := candleImportTestJSON(key, observed, fetched...)
			fetch := candleImportTestSource(t, db, key, end, func() (int, string) {
				calls++
				if tc.name == "stale" {
					winner := candleImportTestSource(t, other, key, end, func() (int, string) { return 200, body })
					_, err := StepCandleImport(ctx, other, winner, key)
					candleImportTestOK(t, err)
				}
				return 200, body
			})
			source := func(ctx context.Context, symbol, interval string, startMs, endMs int64) (model.CandlePage, error) {
				page, err := fetch(ctx, symbol, interval, startMs, endMs)
				if tc.name == "canceled-fetch" {
					candleImportTestOK(t, err)
					cancel()
				}
				return page, err
			}
			if tc.name == "nil-source" {
				source = nil
			}
			got, err := StepCandleImport(ctx, db, source, lookup)
			if tc.want != nil {
				candleImportTestFailure(t, got, err, tc.want)
			} else {
				want.Revision, want.State = 1, "blocked"
				candleImportTestOK(t, err)
				replay, err := StepCandleImport(ctx, db, source, key)
				candleImportTestOK(t, err)
				if !reflect.DeepEqual(got, replay) {
					t.Fatalf("blocked result=%+v replay=%+v", got, replay)
				}
			}
			durable := candleImportTestDurable(t, other, key, want, tc.reason, storedClock, stored...)
			if calls != tc.calls || tc.want == nil && !reflect.DeepEqual(got, durable) {
				t.Fatalf("calls=%d, want %d; result=%+v durable=%+v", calls, tc.calls, got, durable)
			}
		})
	}
}
