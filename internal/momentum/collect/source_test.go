package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/signals"
)

const paperConfig = "../../../configs/momentum.paper.yaml"
const clockMS = 1794700800000
const startMS = 1792022400000
const dayMS = 86400000

type fixtureTransport struct {
	t       *testing.T
	variant string
	cause   error
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
	stamp := int64(clockMS)
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
		if f.variant == "redact" {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("source-sensitive-marker")), Request: r}, nil
		}
		if f.cause != nil {
			return nil, f.cause
		}
		if f.variant == "cutoff" {
			stamp = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
		}
		if f.variant == "too-early" {
			stamp = dayMS
		}
		if f.variant == "clock" {
			stamp = 0
		}
	case "/v5/market/kline":
		if q.Get("category") != "linear" || q.Get("interval") != "D" || q.Get("limit") != "200" || q.Get("start") != "1792022399999" || q.Get("end") != "1794700799999" {
			f.t.Fatalf("bad bounds: %s", r.URL.RawQuery)
		}
		result["category"], result["symbol"], result["list"] = "linear", symbol, candleRows(symbol, f.variant)
		if f.variant == "rollover" && symbol == "HYPEUSDT" {
			stamp += dayMS
		}
		if f.variant == "unconfirmed" && symbol == "HYPEUSDT" {
			stamp--
		}
	default:
		f.t.Fatalf("unexpected endpoint: %s", r.URL.Path)
	}
	body, _ := json.Marshal(map[string]any{"retCode": 0, "time": stamp, "result": result})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

// candleRows сохраняет настоящий newest-first порядок Bybit.
func candleRows(symbol, variant string) [][]string {
	rows := make([][]string, 0, 31)
	for day := 30; day >= 0; day-- {
		if symbol == "HYPEUSDT" && (variant == "empty" || variant == "partial" && day == 0 || variant == "gap" && day == 15) {
			continue
		}
		price := fmt.Sprint(map[string]int{"BTCUSDT": 100 + day, "ETHUSDT": 100, "HYPEUSDT": 200 - day}[symbol])
		rows = append(rows, []string{fmt.Sprint(startMS + day*dayMS), price, price, price, price, "1", "1"})
	}
	return rows
}

// TestReadBatch получает три newest-first страницы и проверяет канонический пакет.
func TestReadBatch(t *testing.T) {
	cfg := loadPaperConfig(t)
	transport := &fixtureTransport{t: t}
	batch, cutoff, err := ReadBatch(context.Background(), cfg, transport)
	if err != nil || cutoff.UnixMilli() != clockMS || cutoff.Location() != time.UTC || batch.Hash() == "" {
		t.Fatalf("batch=%s cutoff=%v error=%v", batch.Hash(), cutoff, err)
	}
	const want = "/v5/market/time ,/v5/market/kline BTCUSDT,/v5/market/kline ETHUSDT,/v5/market/kline HYPEUSDT"
	if strings.Join(transport.calls, ",") != want {
		t.Fatalf("requests=%v", transport.calls)
	}
	replay, err := signals.DecodeBatch(batch.Payload())
	if err != nil || replay.Hash() != batch.Hash() || !bytes.Equal(replay.Payload(), batch.Payload()) {
		t.Fatalf("canonical replay failed: %v", err)
	}
}

// TestReadBatchRejects проверяет ошибки источника без возврата частичного результата.
func TestReadBatchRejects(t *testing.T) {
	cfg := loadPaperConfig(t)
	for _, tc := range []struct {
		name, reason string
		cause        error
		wantCalls    int
	}{
		{"empty", "valid signal batch", nil, 4},
		{"partial", "valid signal batch", nil, 4},
		{"gap", "valid signal batch", nil, 4},
		{"rollover", "valid signal batch", nil, 4},
		{"unconfirmed", "valid signal batch", nil, 4},
		{"clock", "Testnet clock", nil, 1},
		{"cutoff", "RFC3339", nil, 1},
		{"too-early", "full daily window", nil, 1},
		{"redact", "Testnet clock", nil, 1},
		{"Canceled", "", context.Canceled, 1},
		{"DeadlineExceeded", "", context.DeadlineExceeded, 1},
		{"timeout", "", context.DeadlineExceeded, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := &fixtureTransport{t: t, variant: tc.name, cause: tc.cause}
			if tc.name == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				f.cause = nil
			}
			batch, cutoff, err := ReadBatch(ctx, cfg, f)
			if err == nil || batch.Hash() != "" || !cutoff.IsZero() || len(f.calls) != tc.wantCalls {
				t.Fatalf("batch=%s cutoff=%v error=%v requests=%v", batch.Hash(), cutoff, err, f.calls)
			}
			if !strings.Contains(err.Error(), tc.reason) || tc.cause != nil && !errors.Is(err, tc.cause) || strings.Contains(err.Error(), "source-sensitive-marker") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestReadBatchValidatesBeforeHTTP проверяет обе причины отказа до первого запроса.
func TestReadBatchValidatesBeforeHTTP(t *testing.T) {
	raw, err := os.ReadFile(paperConfig)
	if err != nil {
		t.Fatal(err)
	}
	unsupported, err := config.Parse(bytes.Replace(raw, []byte("model_id: roc30"), []byte("model_id: unknown"), 1))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixtureTransport{t: t}
	batch, cutoff, err := ReadBatch(context.Background(), unsupported, f)
	if err == nil || !strings.Contains(err.Error(), "unsupported Momentum collection configuration") || batch.Hash() != "" || !cutoff.IsZero() || len(f.calls) != 0 {
		t.Fatalf("unsupported config accepted: batch=%s cutoff=%v error=%v requests=%v", batch.Hash(), cutoff, err, f.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	batch, cutoff, err = ReadBatch(ctx, loadPaperConfig(t), f)
	if !errors.Is(err, context.Canceled) || batch.Hash() != "" || !cutoff.IsZero() || len(f.calls) != 0 {
		t.Fatalf("canceled request not stopped: batch=%s cutoff=%v error=%v requests=%v", batch.Hash(), cutoff, err, f.calls)
	}
}

// loadPaperConfig загружает тот же пример, который использует CLI.
func loadPaperConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(paperConfig)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
