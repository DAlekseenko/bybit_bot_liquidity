package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/collect"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/signals"
	"bybit_bot_liquidity/internal/momentum/storage"

	"github.com/lib/pq"
)

const testnetOrigin = "https://api-testnet.bybit.com"
const paperConfig = "../../configs/momentum.paper.yaml"
const clockMS = 1794700800000
const startMS = 1792022400000
const dayMS = 86400000

type fixtureTransport struct {
	t       *testing.T
	variant string
	calls   []string
}

// RoundTrip эмулирует публичные Testnet time и kline ответы.
func (f *fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != "api-testnet.bybit.com" {
		f.t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
	}
	for key := range r.Header {
		if strings.HasPrefix(strings.ToLower(key), "x-bapi-") {
			f.t.Fatalf("authenticated request: %s", key)
		}
	}
	q := r.URL.Query()
	symbol := q.Get("symbol")
	f.calls = append(f.calls, r.URL.Path+" "+symbol)
	result := map[string]any{}
	switch r.URL.Path {
	case "/v5/market/time":
		if r.URL.RawQuery != "" {
			f.t.Fatal("unexpected clock query")
		}
		if f.variant == "timeout" {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
	case "/v5/market/kline":
		if q.Get("category") != "linear" || q.Get("interval") != "D" || q.Get("limit") != "200" || q.Get("start") != "1792022399999" || q.Get("end") != "1794700799999" {
			f.t.Fatalf("bad bounds: %s", r.URL.RawQuery)
		}
		result["category"], result["symbol"], result["list"] = "linear", symbol, candleRows(symbol, f.variant)
	default:
		f.t.Fatalf("unexpected endpoint: %s", r.URL.Path)
	}
	body, _ := json.Marshal(map[string]any{"retCode": 0, "time": clockMS, "result": result})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

// candleRows сохраняет настоящий newest-first порядок Bybit.
func candleRows(symbol, variant string) [][]string {
	rows := make([][]string, 0, 31)
	for day := 30; day >= 0; day-- {
		if symbol == "HYPEUSDT" && variant == "empty" {
			continue
		}
		price := fmt.Sprint(map[string]int{"BTCUSDT": 100 + day, "ETHUSDT": 100, "HYPEUSDT": 200 - day}[symbol])
		rows = append(rows, []string{fmt.Sprint(startMS + day*dayMS), price, price, price, price, "1", "1"})
	}
	return rows
}

// argsFor устанавливает неизменные аргументы сценария, кроме таймаута.
func argsFor(timeout string) []string {
	return []string{"collect-once", "-config", paperConfig, "-timeout", timeout}
}

// TestCollectInputRejects проверяет отказ CLI и конфигурации до публичных запросов.
func TestCollectInputRejects(t *testing.T) {
	f := &fixtureTransport{t: t}
	t.Setenv("MOMENTUM_POSTGRES_DSN", "probe")
	for _, args := range [][]string{nil, {"bad"}, {"collect-once", "-wat"}, append(argsFor("1s"), "tail"), argsFor("0s"), {"collect-once", "-timeout", "1s"}, {"collect-once", "-config", "/no-such-config", "-timeout", "1s"}} {
		if err := run(args, io.Discard, f); err == nil {
			t.Fatalf("accepted args %v", args)
		}
	}
	t.Setenv("MOMENTUM_POSTGRES_DSN", "")
	if err := run(argsFor("1s"), io.Discard, f); err == nil || len(f.calls) != 0 {
		t.Fatalf("missing DSN err=%v requests=%v", err, f.calls)
	}
}

// TestCollectFailures проверяет, что отказ источника опережает открытие локального журнала.
func TestCollectFailures(t *testing.T) {
	const dsn = "postgres://test:synthetic@127.0.0.1:5432/momentum_test_probe?sslmode=disable"
	t.Setenv("MOMENTUM_POSTGRES_DSN", dsn)
	opens := 0
	unavailable := func(_ context.Context, got string) (*sql.DB, error) {
		opens++
		if got != dsn {
			t.Fatal("unexpected journal config")
		}
		return nil, storage.ErrPostgresConnect
	}
	for _, tc := range []struct {
		variant, timeout string
		reason           string
		cause            error
	}{
		{"empty", "5s", "valid signal batch", nil},
		{"timeout", "50ms", "", context.DeadlineExceeded},
	} {
		f := &fixtureTransport{t: t, variant: tc.variant}
		var output bytes.Buffer
		err := runWithJournal(argsFor(tc.timeout), &output, f, unavailable)
		if err == nil || !strings.Contains(err.Error(), tc.reason) || tc.cause != nil && !errors.Is(err, tc.cause) || output.Len() != 0 || opens != 0 {
			t.Fatalf("%+v opens=%d error=%v output=%q", tc, opens, err, output.String())
		}
	}
	if err := runWithJournal(argsFor("1s"), io.Discard, &fixtureTransport{t: t}, unavailable); err == nil || !strings.Contains(err.Error(), "local signal journal") || opens != 1 {
		t.Fatalf("inaccessible journal witness: opens=%d err=%v", opens, err)
	}
	t.Setenv("MOMENTUM_POSTGRES_DSN", "invalid-secret-marker")
	if err := run(argsFor("1s"), io.Discard, &fixtureTransport{t: t}); err == nil || !strings.Contains(err.Error(), "local signal journal") || strings.Contains(err.Error(), "invalid-secret-marker") {
		t.Fatalf("unexpected database error: %v", err)
	}
}

// TestInsertAndVerify проверяет решение по вставке, точный ключ и ошибки readback.
func TestInsertAndVerify(t *testing.T) {
	cfg, _ := config.Load(paperConfig)
	batch, _, err := collect.ReadBatch(context.Background(), cfg, &fixtureTransport{t: t})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		insert, read       error
		cancel, wrongBatch bool
		wantRead           int
	}{
		{storage.ErrSignalBatchInsertUnknown, nil, false, false, 1},
		{storage.ErrSignalBatchNotInserted, nil, false, false, 1},
		{storage.ErrSignalBatchInsertUnknown, storage.ErrSignalBatchAbsent, false, false, 1},
		{storage.ErrSignalBatchInsertUnknown, storage.ErrSignalBatchCorrupt, false, false, 1},
		{storage.ErrSignalBatchInsertUnknown, storage.ErrSignalBatchUnavailable, false, false, 1},
		{storage.ErrSignalBatchInsertUnknown, nil, false, true, 1},
		{storage.ErrSignalBatchInsertUnknown, nil, true, false, 0},
		{storage.ErrSignalBatchInsertUnavailable, nil, false, false, 0},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		inserts, reads := 0, 0
		err := insertAndVerify(ctx, nil, batch, func(context.Context, *sql.DB, signals.Batch) error {
			inserts++
			if tc.cancel {
				cancel()
			}
			return tc.insert
		}, func(_ context.Context, _ *sql.DB, hash string) (storage.StoredSignalBatch, error) {
			reads++
			if hash != batch.Hash() {
				t.Fatal("read used wrong key")
			}
			if tc.wrongBatch {
				return storage.StoredSignalBatch{}, nil
			}
			return storage.StoredSignalBatch{Batch: batch}, tc.read
		})
		cancel()
		success := reads == 1 && tc.read == nil && !tc.wrongBatch
		if inserts != 1 || reads != tc.wantRead || (err == nil) != success {
			t.Fatalf("%+v inserts=%d reads=%d err=%v", tc, inserts, reads, err)
		}
	}
}

// TestRunCollectOnce даёт реальный PostgreSQL round trip и независимую проверку.
func TestRunCollectOnce(t *testing.T) {
	db := testJournal(t)
	t.Setenv("BYBIT_API_KEY", "synthetic")
	t.Setenv("BYBIT_API_SECRET", "synthetic")
	f := &fixtureTransport{t: t}
	var out bytes.Buffer
	for _, writer := range []io.Writer{&out, &out, errorWriter{}} {
		err := run(argsFor("30s"), writer, f)
		if writer == &out && err != nil || writer != &out && (err == nil || err.Error() != "cannot write collection result") {
			t.Fatalf("unexpected collection result: %v", err)
		}
	}
	wantPath := "/v5/market/time ,/v5/market/kline BTCUSDT,/v5/market/kline ETHUSDT,/v5/market/kline HYPEUSDT"
	if strings.Join(f.calls, ",") != wantPath+","+wantPath+","+wantPath {
		t.Fatalf("requests=%v", f.calls)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var got success
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &got) != nil || got.CutoffUTC != "2026-11-15T00:00:00Z" || lines[0] != lines[1] {
		t.Fatalf("unexpected stdout %q", out.String())
	}
	var count int
	var raw []byte
	if err := db.QueryRow("SELECT count(*), (SELECT payload FROM momentum.signal_batches) FROM momentum.signal_batches").Scan(&count, &raw); err != nil || count != 1 || fmt.Sprintf("%x", sha256.Sum256(raw)) != got.BatchHash {
		t.Fatalf("raw journal rows=%d err=%v", count, err)
	}
	saved, err := storage.ReadSignalBatch(context.Background(), db, got.BatchHash)
	if err != nil || !bytes.Equal(raw, saved.Batch.Payload()) {
		t.Fatalf("independent replay: %v", err)
	}
	assertPayload(t, raw)
}

type errorWriter struct{}

// Write эмулирует ошибку записи результата.
func (errorWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// assertPayload сверяет фактический порядок, происхождение, окна и флаги.
func assertPayload(t *testing.T, raw []byte) {
	t.Helper()
	var doc struct {
		Windows []struct {
			Symbol, Environment, Source, Category, Endpoint string
			StartMS, EndMS, ServerMS                        int64
			Candles                                         []struct {
				OpenMS    int64
				Confirmed bool
			}
		}
		Signals []struct {
			Symbol string
			Active bool
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Windows) != 3 || len(doc.Signals) != 3 {
		t.Fatalf("unexpected journal shape: %v", err)
	}
	for i, symbol := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		w, sig := doc.Windows[i], doc.Signals[i]
		if w.Symbol != symbol || w.Environment != "testnet" || w.Source != "bybit_v5" || w.Category != "linear" || w.Endpoint != testnetOrigin+"/v5/market/kline" || w.StartMS != startMS || w.EndMS != clockMS-1 || w.ServerMS != clockMS || len(w.Candles) != 31 || sig.Symbol != symbol || sig.Active != (i == 0) {
			t.Fatalf("unexpected replay for %s", symbol)
		}
		for day, row := range w.Candles {
			if !row.Confirmed || row.OpenMS != int64(startMS+day*dayMS) {
				t.Fatalf("invalid candle %s day=%d %+v", symbol, day, row)
			}
		}
	}
}

// testJournal создаёт уникальную БД на явно указанном disposable PostgreSQL.
func testJournal(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MOMENTUM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL opt-in отсутствует")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(u.Path, "/momentum_test_") || strings.Count(u.Path, "/") != 1 {
		t.Fatal("invalid disposable PostgreSQL DSN")
	}
	ctx := context.Background()
	admin, err := storage.OpenMomentumPostgres(ctx, dsn)
	if err != nil {
		t.Fatal("cannot open disposable PostgreSQL")
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("momentum_test_run_%d", time.Now().UnixNano())
	if _, err = admin.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(name)); err != nil {
		t.Fatal("cannot create isolated database")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP DATABASE "+pq.QuoteIdentifier(name)); err != nil {
			t.Errorf("cleanup isolated database: %v", err)
		}
	})
	u.Path, u.RawPath = "/"+name, ""
	dsn = u.String()
	t.Setenv("MOMENTUM_POSTGRES_DSN", dsn)
	db, err := storage.OpenMomentumPostgres(ctx, dsn)
	if err != nil {
		t.Fatal("cannot open isolated journal")
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
