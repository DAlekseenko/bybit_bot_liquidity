package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// cliFixture записывает самодостаточные canonical входы без абсолютных source путей.
func cliFixture(t *testing.T) ([]string, config.Config, data.ManifestInput, []byte) {
	t.Helper()
	root := t.TempDir()
	source := data.ManifestSource{Environment: "testnet", DatasetID: "cli", Source: "bybit_v5", Category: "linear"}
	start := time.Date(2025, 7, 7, 0, 0, 0, 0, time.UTC)
	span := data.ManifestRange{Start: start.AddDate(0, 0, -32), End: start.Add(time.Hour)}
	opaque := []byte("explicit-unknown-evidence")
	sum := sha256.Sum256(opaque)
	ref := data.ManifestArtifact{Format: "evidence/v1", SHA256: hex.EncodeToString(sum[:])}
	manifest := data.ManifestInput{Source: source, Range: span, LoadedAt: span.End, ModelAssumptions: ref, MarginEvidence: ref}
	files := map[string][]byte{"objects/" + ref.SHA256 + ".bin": opaque}
	one, err := model.ParsePrice("1")
	if err != nil {
		t.Fatal(err)
	}
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		for _, kind := range []string{"mark", "trade", "daily", "funding"} {
			endpoint, interval, format := "https://api-testnet.bybit.com/v5/market/kline", "60", "momentum/trade-candle-content/v1"
			var raw []byte
			var hash string
			var err error
			streamRange := span
			switch kind {
			case "mark":
				endpoint, format = "https://api-testnet.bybit.com/v5/market/mark-price-kline", "momentum/mark-price-content/v1"
				rows := []model.MarkPriceCandle{}
				for _, at := range []time.Time{start.Add(-time.Hour), start} {
					rows = append(rows, model.MarkPriceCandle{OpenTime: at, Open: one, High: one, Low: one, Close: one, Confirmed: true})
				}
				raw, hash, err = data.EncodeMarkPriceContent(data.MarkPriceContentInput{Environment: source.Environment, DatasetID: source.DatasetID, Source: source.Source, Category: source.Category, Symbol: symbol, IntervalCode: interval, Endpoint: endpoint, Start: streamRange.Start, End: streamRange.End, Rows: rows})
			case "funding":
				endpoint, interval, format = "https://api-testnet.bybit.com/v5/market/funding/history", "", "momentum/funding-content/v1"
				raw, hash, err = data.EncodeFundingContent(data.FundingContentInput{Environment: source.Environment, DatasetID: source.DatasetID, Source: source.Source, Category: source.Category, Symbol: symbol, Endpoint: endpoint, Start: span.Start, End: span.End, Events: []model.FundingEvent{{FundingTime: start, Rate: model.Zero()}}})
			default:
				rows := []data.TradeCandleContentRow{{OpenTime: start, Open: one, High: one, Low: one, Close: one, Volume: model.Zero(), Confirmed: true}}
				if kind == "daily" {
					interval, rows = "D", nil
					streamRange.End = start
					for day := -32; day < 0; day++ {
						price, _ := model.ParsePrice(fmt.Sprint(day + 33))
						rows = append(rows, data.TradeCandleContentRow{OpenTime: start.AddDate(0, 0, day), Open: price, High: price, Low: price, Close: price, Volume: model.Zero(), Confirmed: true})
					}
				}
				raw, hash, err = data.EncodeTradeCandleContent(data.TradeCandleContentInput{Environment: source.Environment, DatasetID: source.DatasetID, Source: source.Source, Category: source.Category, Symbol: symbol, IntervalCode: interval, Endpoint: endpoint, Start: streamRange.Start, End: streamRange.End, Rows: rows})
			}
			if err != nil {
				t.Fatal(err)
			}
			streamKind := kind
			if kind == "daily" {
				streamKind = "trade"
			}
			files["objects/"+hash+".bin"] = raw
			manifest.Streams = append(manifest.Streams, data.ManifestStream{Source: source, Range: streamRange, Kind: streamKind, Symbol: symbol, IntervalCode: interval, Endpoint: endpoint, Content: data.ManifestArtifact{Format: format, SHA256: hash}, Coverage: ref})
		}
	}
	manifestBytes, manifestHash, err := data.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf(`schema_version: 1
strategy_id: cli-test
profile_version: v1
model_id: explicit
mode: backtest
profile: baseline_delta_1x
start_policy: next_monday
symbols: [BTCUSDT, ETHUSDT, HYPEUSDT]
leverage: 1
margin_mode: ISOLATED_MARGIN
auto_add_margin: false
position_mode: one_way
initial_equity_usdt: '10000'
data_source:
  kind: frozen_dataset
  environment: testnet
  dataset_id: cli
  manifest_sha256: %s
`, manifestHash)
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]planner.MarketDeltaRules{}
	maximum, _ := model.ParsePrice("1000000")
	for _, symbol := range cfg.Values().Symbols {
		rules[symbol] = planner.MarketDeltaRules{QtyStep: one, MinQty: one, MaxMarketQty: maximum, MinIncreaseNotional: model.Zero()}
	}
	simulation, err := backtest.NewSimulationModel(cfg, manifest, backtest.SimulationParameters{FeeRate: model.Zero(), SlippageRate: model.Zero(), MarkMaxAge: time.Hour, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	modelBytes, _, err := simulation.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "objects"), 0700); err != nil {
		t.Fatal(err)
	}
	files["config.yaml"], files["model.json"], files["manifest.json"] = []byte(yaml), modelBytes, manifestBytes
	for name, payload := range files {
		if err := os.WriteFile(filepath.Join(root, name), payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"-config", filepath.Join(root, "config.yaml"), "-dataset", root, "-model", filepath.Join(root, "model.json"), "-start", "2025-07-07T00:00:00Z", "-end", "2025-07-07T01:00:00Z", "-output", filepath.Join(root, "output"), "-approval", "g10-owner-offline-scenario-v1"}
	return args, cfg, manifest, modelBytes
}

// TestBacktestArguments проверяет ранние ошибки до загрузки данных.
func TestBacktestArguments(t *testing.T) {
	args, _, _, _ := cliFixture(t)
	for _, test := range []struct {
		name   string
		change func([]string) []string
	}{
		{"missing", func([]string) []string { return nil }},
		{"unknown", func(a []string) []string { return append(a, "-unknown") }},
		{"positional", func(a []string) []string { return append(a, "unexpected") }},
		{"date", func(a []string) []string { a[7] = "bad"; return a }},
		{"reverse", func(a []string) []string { a[9] = a[7]; return a }},
		{"approval", func(a []string) []string { a[13] = ""; return a }},
		{"config", func(a []string) []string { a[1] = "missing"; return a }},
		{"dataset", func(a []string) []string { a[3] = "missing"; return a }},
		{"model", func(a []string) []string { a[5] = "missing"; return a }},
		{"engine-grid", func(a []string) []string { a[7] = "2025-07-07T00:30:00Z"; return a }},
		{"bundle-ref", func(a []string) []string { a[13] = " bad"; return a }},
		{"writer-parent", func(a []string) []string { a[11] += "/missing/output"; return a }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			changed := test.change(append([]string(nil), args...))
			err := runBacktest(context.Background(), changed, &out)
			if err == nil || out.Len() != 0 {
				t.Fatal("invalid args accepted", err)
			}
			if len(changed) > 11 {
				if _, err := os.Stat(changed[11]); !os.IsNotExist(err) {
					t.Fatal("published failed run", err)
				}
			}
		})
	}
	if runBacktest(nil, args, io.Discard) == nil || runBacktest(context.Background(), args, nil) == nil { //nolint:staticcheck // Намеренно проверяем отказ для nil-контекста.
		t.Fatal("nil arguments")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(runBacktest(ctx, args, io.Discard), context.Canceled) {
		t.Fatal("cancellation lost")
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	if !errors.Is(runBacktest(ctx, args, io.Discard), context.DeadlineExceeded) {
		t.Fatal("deadline lost")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	controlled := &cliCancelContext{Context: ctx, cancel: cancel}
	if !errors.Is(runBacktest(controlled, args, io.Discard), context.Canceled) || controlled.checks < 4 {
		t.Fatal("cancellation during loading lost")
	}
}

// cliCancelContext отменяет реальное чтение после нескольких проверок контекста.
type cliCancelContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

// Err детерминированно отменяет родительский контекст во время загрузки.
func (c *cliCancelContext) Err() error {
	c.checks++
	if c.checks == 4 {
		c.cancel()
	}
	return c.Context.Err()
}

// rejectedTransport подтверждает отсутствие HTTP при раннем backtest dispatch.
type rejectedTransport struct{ t *testing.T }

// RoundTrip прерывает тест при попытке сетевого запроса.
func (r rejectedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Fatal("network attempted")
	return nil, errors.New("network attempted")
}

// TestBacktestDispatch проверяет нормальный entry point с выключенным DSN.
func TestBacktestDispatch(t *testing.T) {
	args, _, _, _ := cliFixture(t)
	t.Setenv("MOMENTUM_POSTGRES_DSN", "")
	previous := http.DefaultTransport
	http.DefaultTransport = rejectedTransport{t}
	defer func() { http.DefaultTransport = previous }()
	var first, second bytes.Buffer
	if err := run(append([]string{"backtest"}, args...), &first, rejectedTransport{t}); err != nil {
		t.Fatal(err)
	}
	firstOutput := args[11]
	args[11] += "-again"
	if err := run(append([]string{"backtest"}, args...), &second, rejectedTransport{t}); err != nil {
		t.Fatal(err)
	}
	if len(first.String()) != 65 || first.String() != second.String() {
		t.Fatal("dispatch output")
	}
	if _, err := hex.DecodeString(strings.TrimSpace(first.String())); err != nil {
		t.Fatal("run id", err)
	}
	raw, err := os.ReadFile(filepath.Join(firstOutput, "journal.json"))
	var journal struct{ Rows []struct{ Role string } }
	if err != nil || json.Unmarshal(raw, &journal) != nil || len(journal.Rows) == 0 || journal.Rows[len(journal.Rows)-1].Role != "terminal" {
		t.Fatal("terminal journal", err)
	}
	entries, err := os.ReadDir(firstOutput)
	if err != nil || len(entries) != 6 {
		t.Fatal("inventory", err)
	}
	var sums strings.Builder
	for _, name := range []string{"config.json", "journal.json", "run.json", "simulation-model.json", "source-manifest.json", "SHA256SUMS"} {
		one, err := os.ReadFile(filepath.Join(firstOutput, name))
		if err != nil {
			t.Fatal(err)
		}
		two, err := os.ReadFile(filepath.Join(args[11], name))
		if err != nil || !bytes.Equal(one, two) {
			t.Fatal("different independent results", name, err)
		}
		if name == "SHA256SUMS" {
			if string(one) != sums.String() {
				t.Fatal("checksums")
			}
		} else {
			fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(one), name)
		}
	}
	if err := run(append([]string{"backtest"}, args...), io.Discard, rejectedTransport{t}); err != nil {
		t.Fatal("idempotence", err)
	}
}

// failedBacktestOutput моделирует отказ вывода завершённого результата.
type failedBacktestOutput struct{}

// Write возвращает ошибку без частично успешного stdout.
func (failedBacktestOutput) Write([]byte) (int, error) { return 0, errors.New("write refused") }

// TestBacktestOutputFailure сохраняет ошибку stdout и отказывает неверному mode.
func TestBacktestOutputFailure(t *testing.T) {
	args, _, _, _ := cliFixture(t)
	if err := runBacktest(context.Background(), args, failedBacktestOutput{}); err == nil {
		t.Fatal("stdout failure lost")
	}
	published, err := os.ReadDir(args[11])
	if err != nil || len(published) != 6 {
		t.Fatal("stdout failure removed complete publication", err)
	}
	raw, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("mode: backtest"), []byte("mode: paper"), 1)
	if err := os.WriteFile(args[1], raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runBacktest(context.Background(), args, io.Discard); err == nil {
		t.Fatal("paper config accepted")
	}
}
