package data

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
)

// manifestDecodeBytes создаёт canonical bytes и только существующий configuration pin.
func manifestDecodeBytes(t *testing.T, input ManifestInput) ([]byte, ManifestSelector) {
	t.Helper()
	raw, digest, err := EncodeManifest(input)
	if err != nil {
		t.Fatal(err)
	}
	return raw, ManifestSelector{input.Source.Environment, input.Source.DatasetID, digest}
}

// requireManifestDecodeFailure исключает partial output, утечку и изменение входа.
func requireManifestDecodeFailure(t *testing.T, raw []byte, selector ManifestSelector) {
	t.Helper()
	before := bytes.Clone(raw)
	got, err := DecodeManifest(raw, selector)
	if !errors.Is(err, ErrManifest) || !reflect.DeepEqual(got, ManifestInput{}) || !bytes.Equal(raw, before) || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("invalid manifest returned data or unsafe error: %v", err)
	}
}

// manifestCorruption проверяет повреждение с совпадающим SHA, чтобы digest mismatch не маскировал проверку.
func manifestCorruption(t *testing.T, raw []byte, selector ManifestSelector) {
	t.Helper()
	digest := sha256.Sum256(raw)
	selector.SHA256 = hex.EncodeToString(digest[:])
	requireManifestDecodeFailure(t, raw, selector)
}

// TestDecodeManifestGolden проверяет независимый golden, UTC и полное владение decoded памятью.
func TestDecodeManifestGolden(t *testing.T) {
	const golden = `{"version":"momentum/dataset-manifest/v1","source":{"environment":"testnet","dataset_id":"frozen-1","source":"bybit_v5","category":"linear"}` +
		`,"range":{"start_ms":0,"end_ms":172800000},"loaded_ms":172800123,"model_assumptions":{"format":"assumptions/v1","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}` +
		`,"margin_evidence":{"format":"margin/v1","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}` +
		`,"streams":[{"kind":"funding","symbol":"BTCUSDT","endpoint":"https://api-testnet.bybit.com/v5/market/funding/history","range":{"start_ms":123,"end_ms":172799999}` +
		`,"content":{"format":"rows/v1","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"coverage":{"format":"coverage/v1","sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}}]}`
	input := manifestFixture()
	raw, selector := manifestDecodeBytes(t, input)
	if string(raw) != golden || len(raw) != 828 || selector.SHA256 != "681ba73ad83c5333e8cfe11cbfb5094a3745e21825bda235bcba39a7c701a18f" {
		t.Fatal("golden changed")
	}
	original := bytes.Clone(raw)
	got, err := DecodeManifest(raw, selector)
	if err != nil || !reflect.DeepEqual(got, input) || !bytes.Equal(raw, original) {
		t.Fatalf("manifest changed during decode: %v", err)
	}
	for i := range raw {
		raw[i] = 'x'
	}
	encoded, digest, err := EncodeManifest(got)
	if err != nil || !bytes.Equal(encoded, original) || digest != selector.SHA256 {
		t.Fatal("decoded strings alias input", err)
	}
	got.Streams[0].Content.Format = "changed/v1"
	again, err := DecodeManifest(original, selector)
	if err != nil || !reflect.DeepEqual(again, input) {
		t.Fatal("independent decoded outputs share state", err)
	}
}

// TestDecodeManifestConfigSelector связывает только SHA/environment/dataset настоящего Config.
func TestDecodeManifestConfigSelector(t *testing.T) {
	input := manifestFixture()
	raw, selector := manifestDecodeBytes(t, input)
	document, err := os.ReadFile("../../../configs/momentum.paper.yaml")
	if err != nil {
		t.Fatal(err)
	}
	yaml := strings.Replace(string(document), "mode: paper", "mode: backtest", 1)
	yaml = strings.Replace(yaml, "kind: bybit_testnet_public", "kind: frozen_dataset", 1)
	yaml += "  dataset_id: " + selector.DatasetID + "\n  manifest_sha256: " + selector.SHA256 + "\n"
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	pin := cfg.Values().DataSource
	got, err := DecodeManifest(raw, ManifestSelector{pin.Environment, pin.DatasetID, pin.ManifestSHA256})
	if err != nil || got.Range != input.Range {
		t.Fatal("configuration pin cannot load declared range", err)
	}
	input.Range.End = input.Range.End.Add(24 * time.Hour)
	changed, changedPin := manifestDecodeBytes(t, input)
	requireManifestDecodeFailure(t, changed, selector)
	got, err = DecodeManifest(changed, changedPin)
	if err != nil || got.Range != input.Range {
		t.Fatal("new SHA must bind and expose changed range without an extra range argument", err)
	}
}

// TestDecodeManifestSelectorRejects проверяет неверные pins без введения новых identity ошибок.
func TestDecodeManifestSelectorRejects(t *testing.T) {
	raw, selector := manifestDecodeBytes(t, manifestFixture())
	for _, wrong := range []ManifestSelector{
		{}, {"paper", selector.DatasetID, selector.SHA256}, {"mainnet", selector.DatasetID, selector.SHA256},
		{selector.Environment, "", selector.SHA256}, {selector.Environment, "other", selector.SHA256},
		{selector.Environment, "Frozen-1", selector.SHA256}, {selector.Environment, " secret-marker", selector.SHA256},
		{selector.Environment, "bad\x00id", selector.SHA256}, {selector.Environment, "bad\xffid", selector.SHA256},
		{selector.Environment, strings.Repeat("я", 129), selector.SHA256},
		{selector.Environment, selector.DatasetID, ""}, {selector.Environment, selector.DatasetID, strings.Repeat("0", 64)},
		{selector.Environment, selector.DatasetID, strings.ToUpper(selector.SHA256)},
		{selector.Environment, selector.DatasetID, strings.Repeat("z", 64)}, {selector.Environment, selector.DatasetID, "abc"},
	} {
		requireManifestDecodeFailure(t, raw, wrong)
	}
}

// TestDecodeManifestQualityAndShapes сохраняет unknown evidence и все15 допустимых ключей без admission.
func TestDecodeManifestQualityAndShapes(t *testing.T) {
	input := manifestFixture()
	input.Source = ManifestSource{"mainnet", strings.Repeat("я", 124) + "\"<&\n", "bybit_v5", "linear"}
	start := time.Date(294276, 1, 1, 0, 0, 0, 0, time.UTC)
	zone := time.FixedZone("east", 3*3600)
	input.Range = ManifestRange{start.In(zone), start.Add(48 * time.Hour).In(zone)}
	input.LoadedAt = start.Add(time.Millisecond).In(zone)
	input.ModelAssumptions.Format, input.MarginEvidence.Format = "assumptions/unapproved-v1", "margin/unknown-v1"
	original := input.Streams[0]
	input.Streams = nil
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		for _, kind := range []string{"funding", "mark", "trade"} {
			intervals := []string{"D", "60"}
			path := "/v5/market/" + kind + "-price-kline"
			switch kind {
			case "funding":
				intervals, path = []string{""}, "/v5/market/funding/history"
			case "trade":
				path = "/v5/market/kline"
			}
			for _, interval := range intervals {
				stream := original
				stream.Source, stream.Range = input.Source, input.Range
				stream.Kind, stream.Symbol, stream.IntervalCode, stream.Endpoint = kind, symbol, interval, "https://api.bybit.com"+path
				stream.Content.Format, stream.Coverage.Format = "opaque-rows/v9", "coverage/unknown-v1"
				input.Streams = append(input.Streams, stream)
			}
		}
	}
	raw, selector := manifestDecodeBytes(t, input)
	got, err := DecodeManifest(raw, selector)
	if err != nil || len(got.Streams) != 15 || got.MarginEvidence != input.MarginEvidence || got.ModelAssumptions != input.ModelAssumptions ||
		got.Range.Start != start || got.LoadedAt != start.Add(time.Millisecond) {
		t.Fatal("manifest changed ranges or evidence", err)
	}
	for _, stream := range got.Streams {
		if stream.Source != input.Source || stream.Content.Format != "opaque-rows/v9" || stream.Coverage.Format != "coverage/unknown-v1" {
			t.Fatal("decoder invented evidence or codec policy")
		}
	}
	var wire manifestWire
	_ = json.Unmarshal(raw, &wire)
	slices.Reverse(wire.Streams)
	changed, _ := json.Marshal(wire)
	manifestCorruption(t, changed, selector)
}

// TestDecodeManifestCorruption проверяет canonical decode даже при matching SHA повреждённых bytes.
func TestDecodeManifestCorruption(t *testing.T) {
	raw, selector := manifestDecodeBytes(t, manifestFixture())
	repaired := selector
	repaired.DatasetID = "�rozen-1"
	manifestCorruption(t, bytes.Replace(raw, []byte("frozen-1"), []byte("\xffrozen-1"), 1), repaired)
	manifestCorruption(t, bytes.Replace(raw, []byte("frozen-1"), []byte(`\ud800rozen-1`), 1), repaired)
	for cut := range len(raw) {
		manifestCorruption(t, raw[:cut], selector)
	}
	for _, pair := range [][2]string{
		{"v1", "v2"}, {`"version":`, `"Version":`}, {`"dataset_id":`, `"Dataset_ID":`},
		{`"environment":"testnet"`, `"environment":"testnet","environment":"testnet"`},
		{`"source":"bybit_v5"`, `"source":"secret-marker"`}, {`"category":"linear"`, `"category":"spot"`},
		{`"start_ms":0,`, ""}, {`"start_ms":0`, `"start_ms":-0`}, {`"start_ms":0`, `"start_ms":-1`},
		{`"start_ms":0`, `"start_ms":9223372036854775808`}, {`"start_ms":0`, `"start_ms":9223372036854775807`},
		{`"end_ms":172800000`, `"end_ms":0`}, {`"end_ms":172799999`, `"end_ms":172800001`},
		{`"loaded_ms":172800123,`, ""}, {`"loaded_ms":172800123`, `"loaded_ms":172800123.0`},
		{`"loaded_ms":172800123`, `"loaded_ms":-1`}, {`"kind":"funding"`, `"kind":"other"`},
		{`"symbol":"BTCUSDT"`, `"symbol":"BTCUSDT","interval_code":""`}, {"BTCUSDT", `BTC\u0055SDT`},
		{"/v5/market/funding/history", "/v5/market/kline"}, {`"format":"rows/v1"`, `"format":"Rows/v1"`},
		{strings.Repeat("c", 64), "bad"}, {`"streams":[`, `"api_key":"secret-marker","streams":[`},
	} {
		manifestCorruption(t, bytes.Replace(raw, []byte(pair[0]), []byte(pair[1]), 1), selector)
	}
	for _, changed := range [][]byte{nil, []byte("null"), []byte("[]"), []byte("{}"), append(bytes.Clone(raw), '\n'), append([]byte(" "), raw...), append(bytes.Clone(raw), raw...)} {
		manifestCorruption(t, changed, selector)
	}
	var wire manifestWire
	_ = json.Unmarshal(raw, &wire)
	for _, streams := range [][]manifestStreamWire{nil, {}, {wire.Streams[0], wire.Streams[0]}} {
		wire.Streams = streams
		changed, _ := json.Marshal(wire)
		manifestCorruption(t, changed, selector)
	}
}
