package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// backtestModelFixture создаёт минимальные независимые canonical входы модели.
func backtestModelFixture(t *testing.T) (config.Config, data.ManifestInput, []byte) {
	t.Helper()
	source := data.ManifestSource{Environment: "testnet", DatasetID: "model-test", Source: "bybit_v5", Category: "linear"}
	span := data.ManifestRange{Start: time.Unix(0, 0).UTC(), End: time.Unix(3600, 0).UTC()}
	evidence := []byte("explicit funding-only evidence")
	evidenceHash := sha256.Sum256(evidence)
	evidenceRef := data.ManifestArtifact{Format: "evidence/v1", SHA256: hex.EncodeToString(evidenceHash[:])}
	content, contentHash, err := data.EncodeFundingContent(data.FundingContentInput{Environment: source.Environment, DatasetID: source.DatasetID, Source: source.Source, Category: source.Category, Symbol: "BTCUSDT", Endpoint: "https://api-testnet.bybit.com/v5/market/funding/history", Start: span.Start, End: span.End})
	if err != nil {
		t.Fatal(err)
	}
	manifest := data.ManifestInput{Source: source, Range: span, LoadedAt: span.End, ModelAssumptions: evidenceRef, MarginEvidence: evidenceRef, Streams: []data.ManifestStream{{Source: source, Range: span, Kind: "funding", Symbol: "BTCUSDT", Endpoint: "https://api-testnet.bybit.com/v5/market/funding/history", Content: data.ManifestArtifact{Format: "momentum/funding-content/v1", SHA256: contentHash}, Coverage: evidenceRef}}}
	_, manifestHash, err := data.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("schema_version: 1\nstrategy_id: model-test\nprofile_version: v1\nmodel_id: explicit\nmode: backtest\nprofile: baseline_delta_1x\nstart_policy: next_monday\nsymbols: [BTCUSDT, ETHUSDT, HYPEUSDT]\nleverage: 1\nmargin_mode: ISOLATED_MARGIN\nauto_add_margin: false\nposition_mode: one_way\ninitial_equity_usdt: '10000'\ndata_source:\n  kind: frozen_dataset\n  environment: testnet\n  dataset_id: model-test\n  manifest_sha256: %s\n", manifestHash)
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	one, err := model.ParsePrice("1")
	if err != nil {
		t.Fatal(err)
	}
	rules := make(map[string]planner.MarketDeltaRules)
	for _, symbol := range cfg.Values().Symbols {
		rules[symbol] = planner.MarketDeltaRules{QtyStep: one, MinQty: one, MaxMarketQty: one, MinIncreaseNotional: model.Zero()}
	}
	created, err := backtest.NewSimulationModel(cfg, manifest, backtest.SimulationParameters{FeeRate: model.Zero(), SlippageRate: model.Zero(), MarkMaxAge: time.Hour, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := created.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(content) == 0 {
		t.Fatal("funding content missing")
	}
	return cfg, manifest, raw
}

// TestDecodeBacktestModel проверяет roundtrip и строгую каноническую границу.
func TestDecodeBacktestModel(t *testing.T) {
	cfg, manifest, raw := backtestModelFixture(t)
	decoded, err := decodeBacktestModel(raw, cfg, manifest)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err := decoded.Snapshot()
	if err != nil || !bytes.Equal(raw, canonical) {
		t.Fatal("snapshot mismatch", err)
	}
	mutations := map[string][]byte{
		"empty":              nil,
		"oversized":          bytes.Repeat([]byte("x"), maxBacktestModelBytes+1),
		"trailing value":     append(bytes.Clone(raw), []byte(" {}")...),
		"unknown field":      bytes.Replace(raw, []byte(`"format":`), []byte(`"unknown":1,"format":`), 1),
		"duplicate field":    bytes.Replace(raw, []byte(`"format":`), []byte(`"format":"wrong","format":`), 1),
		"leading whitespace": append([]byte(" "), raw...),
		"trailing newline":   append(bytes.Clone(raw), '\n'),
		"wrong version":      bytes.Replace(raw, []byte("simulation-model/v1"), []byte("simulation-model/v2"), 1),
		"foreign pin":        bytes.Replace(raw, []byte(manifest.ModelAssumptions.SHA256), []byte(strings.Repeat("f", 64)), 1),
		"missing revision":   bytes.Replace(raw, []byte(`"event_order_version":"D-07/v1",`), nil, 1),
		"unsupported status": bytes.Replace(raw, []byte(`"equity_stop":"disabled"`), []byte(`"equity_stop":"enabled"`), 1),
		"invalid fee":        bytes.Replace(raw, []byte(`"one_sided_fee_rate":"0"`), []byte(`"one_sided_fee_rate":"bad"`), 1),
		"noncanonical fee":   bytes.Replace(raw, []byte(`"one_sided_fee_rate":"0"`), []byte(`"one_sided_fee_rate":"0.0"`), 1),
		"invalid age":        bytes.Replace(raw, []byte(`"mark_max_age_ns":3600000000000`), []byte(`"mark_max_age_ns":0`), 1),
		"invalid rule":       bytes.Replace(raw, []byte(`"qty_step":"1"`), []byte(`"qty_step":"-1"`), 1),
		"duplicate symbol":   bytes.Replace(raw, []byte(`"symbol":"ETHUSDT"`), []byte(`"symbol":"BTCUSDT"`), 1),
	}
	for name, payload := range mutations {
		t.Run(name, func(t *testing.T) {
			result, err := decodeBacktestModel(payload, cfg, manifest)
			if err == nil {
				t.Fatal("invalid model accepted")
			}
			if _, _, err := result.Snapshot(); err == nil {
				t.Fatal("failure returned a model")
			}
		})
	}
	foreign := manifest
	foreign.Source.DatasetID = "foreign"
	if result, err := decodeBacktestModel(raw, cfg, foreign); err == nil {
		t.Fatal("foreign source accepted")
	} else if _, _, snapshotErr := result.Snapshot(); snapshotErr == nil {
		t.Fatal("foreign source returned a model")
	}
}

// TestReadBacktestModel проверяет ограниченное чтение файла и безопасные ошибки.
func TestReadBacktestModel(t *testing.T) {
	cfg, manifest, raw := backtestModelFixture(t)
	path := filepath.Join(t.TempDir(), "model.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBacktestModel(path, cfg, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := readBacktestModel(filepath.Join(t.TempDir(), "missing"), cfg, manifest); err == nil {
		t.Fatal("missing file accepted")
	}
	for _, payload := range [][]byte{bytes.Repeat([]byte("x"), maxBacktestModelBytes+1), append(bytes.Clone(raw), '\n')} {
		if err := os.WriteFile(path, payload, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBacktestModel(path, cfg, manifest); err == nil {
			t.Fatal("invalid file accepted")
		}
	}
}
