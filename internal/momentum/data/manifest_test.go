package data

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// manifestFixture создаёт локальные ссылки на синтетические внешние артефакты.
func manifestFixture() ManifestInput {
	source := ManifestSource{"testnet", "frozen-1", "bybit_v5", "linear"}
	return ManifestInput{
		Source:           source,
		Range:            ManifestRange{time.UnixMilli(0).UTC(), time.UnixMilli(172800000).UTC()},
		LoadedAt:         time.UnixMilli(172800123).UTC(),
		ModelAssumptions: ManifestArtifact{"assumptions/v1", strings.Repeat("a", 64)},
		MarginEvidence:   ManifestArtifact{"margin/v1", strings.Repeat("b", 64)},
		Streams: []ManifestStream{{
			Source: source, Range: ManifestRange{time.UnixMilli(123).UTC(), time.UnixMilli(172799999).UTC()},
			Kind: "funding", Symbol: "BTCUSDT", Endpoint: "https://api-testnet.bybit.com/v5/market/funding/history",
			Content: ManifestArtifact{"rows/v1", strings.Repeat("c", 64)}, Coverage: ManifestArtifact{"coverage/v1", strings.Repeat("d", 64)},
		}},
	}
}

// TestEncodeManifestGolden проверяет точные wire bytes и независимый эталон SHA-256.
func TestEncodeManifestGolden(t *testing.T) {
	const golden = `{"version":"momentum/dataset-manifest/v1","source":{"environment":"testnet","dataset_id":"frozen-1","source":"bybit_v5","category":"linear"}` +
		`,"range":{"start_ms":0,"end_ms":172800000},"loaded_ms":172800123,"model_assumptions":{"format":"assumptions/v1","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}` +
		`,"margin_evidence":{"format":"margin/v1","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}` +
		`,"streams":[{"kind":"funding","symbol":"BTCUSDT","endpoint":"https://api-testnet.bybit.com/v5/market/funding/history","range":{"start_ms":123,"end_ms":172799999}` +
		`,"content":{"format":"rows/v1","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"coverage":{"format":"coverage/v1","sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}}]}`
	const checksum = "681ba73ad83c5333e8cfe11cbfb5094a3745e21825bda235bcba39a7c701a18f"
	input := manifestFixture()
	payload, digest, err := EncodeManifest(input)
	if err != nil || string(payload) != golden || digest != checksum {
		t.Fatalf("golden mismatch err=%v SHA=%q payload=%s", err, digest, payload)
	}
	payload[0] = 'x'
	again, digest, err := EncodeManifest(input)
	if err != nil || string(again) != golden || digest != checksum {
		t.Fatal("repeated call retained changed output")
	}
}

// TestEncodeManifestOrder проверяет все виды рядов и независимость от порядка и timezone.
func TestEncodeManifestOrder(t *testing.T) {
	input := manifestFixture()
	funding := input.Streams[0]
	input.Streams = nil
	for _, symbol := range []string{"ZZZUSDT", "BTCUSDT", "ETHUSDT"} {
		funding.Symbol = symbol
		input.Streams = append(input.Streams, funding)
		for _, interval := range []string{"D", "60"} {
			mark, trade := funding, funding
			mark.Kind, mark.IntervalCode, mark.Endpoint = "mark", interval, "https://api-testnet.bybit.com/v5/market/mark-price-kline"
			trade.Kind, trade.IntervalCode, trade.Endpoint = "trade", interval, "https://api-testnet.bybit.com/v5/market/kline"
			input.Streams = append(input.Streams, trade, mark)
		}
	}
	before := slices.Clone(input.Streams)
	payload, digest, err := EncodeManifest(input)
	if err != nil || !slices.Equal(before, input.Streams) {
		t.Fatalf("15 streams failed or input mutated: %v", err)
	}
	var wire manifestWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{
		"funding/BTCUSDT/", "funding/ETHUSDT/", "funding/ZZZUSDT/",
		"mark/BTCUSDT/60", "mark/BTCUSDT/D", "mark/ETHUSDT/60", "mark/ETHUSDT/D", "mark/ZZZUSDT/60", "mark/ZZZUSDT/D",
		"trade/BTCUSDT/60", "trade/BTCUSDT/D", "trade/ETHUSDT/60", "trade/ETHUSDT/D", "trade/ZZZUSDT/60", "trade/ZZZUSDT/D",
	}
	var gotOrder []string
	for _, stream := range wire.Streams {
		gotOrder = append(gotOrder, stream.Kind+"/"+stream.Symbol+"/"+stream.IntervalCode)
	}
	if !slices.Equal(wantOrder, gotOrder) {
		t.Fatalf("unexpected manifest stream order %v", gotOrder)
	}
	zone := time.FixedZone("offset", -3*3600)
	input.LoadedAt = input.LoadedAt.In(zone)
	input.Range.Start, input.Range.End = input.Range.Start.In(zone), input.Range.End.In(zone)
	slices.Reverse(input.Streams)
	for i := range input.Streams {
		r := &input.Streams[i].Range
		r.Start, r.End = r.Start.In(zone), r.End.In(zone)
	}
	reordered, otherDigest, err := EncodeManifest(input)
	if err != nil || !bytes.Equal(payload, reordered) || digest != otherDigest {
		t.Fatal("canonical bytes or checksum depended on input order or timezone")
	}
}

// TestEncodeManifestCommitments проверяет чувствительность к изменению evidence и identity.
func TestEncodeManifestCommitments(t *testing.T) {
	_, original, err := EncodeManifest(manifestFixture())
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ManifestInput){
		"loading observation": func(in *ManifestInput) { in.LoadedAt = in.LoadedAt.Add(time.Millisecond) },
		"dataset range":       func(in *ManifestInput) { in.Range.End = in.Range.End.Add(time.Millisecond) },
		"stream range":        func(in *ManifestInput) { in.Streams[0].Range.Start = in.Streams[0].Range.Start.Add(time.Millisecond) },
		"model assumptions":   func(in *ManifestInput) { in.ModelAssumptions.SHA256 = strings.Repeat("0", 64) },
		"evidence format":     func(in *ManifestInput) { in.MarginEvidence.Format = "margin/v2" },
		"margin evidence":     func(in *ManifestInput) { in.MarginEvidence.SHA256 = strings.Repeat("0", 64) },
		"coverage evidence":   func(in *ManifestInput) { in.Streams[0].Coverage.SHA256 = strings.Repeat("0", 64) },
		"funding content":     func(in *ManifestInput) { in.Streams[0].Content.SHA256 = strings.Repeat("0", 64) },
		"opaque dataset ID": func(in *ManifestInput) {
			in.Source.DatasetID = "история 'v1'"
			in.Streams[0].Source = in.Source
		},
		"mainnet historical": func(in *ManifestInput) {
			in.Source.Environment = "mainnet"
			in.Streams[0].Source = in.Source
			in.Streams[0].Endpoint = "https://api.bybit.com/v5/market/funding/history"
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := manifestFixture()
			change(&input)
			payload, hash, err := EncodeManifest(input)
			if err != nil || hash == original || len(hash) != 64 {
				t.Fatalf("change did not bind to manifest SHA: %s %v", hash, err)
			}
			var wire manifestWire
			if err := json.Unmarshal(payload, &wire); err != nil || wire.Source != input.Source {
				t.Fatalf("source did not round trip: %v", err)
			}
		})
	}
}

// requireManifestFailure проверяет безопасную ошибку без частичного результата.
func requireManifestFailure(t *testing.T, input ManifestInput) {
	t.Helper()
	got, hash, err := EncodeManifest(input)
	if got != nil || hash != "" || !errors.Is(err, ErrManifest) || err.Error() != "invalid momentum dataset manifest" {
		t.Fatalf("invalid manifest result bytes=%d hash=%q err=%v", len(got), hash, err)
	}
}

// TestEncodeManifestRejectsInvalid проверяет namespace, границы, ссылки и дубли.
func TestEncodeManifestRejectsInvalid(t *testing.T) {
	for name, change := range map[string]func(*ManifestInput){
		"missing streams":     func(in *ManifestInput) { in.Streams = nil },
		"unsupported env":     func(in *ManifestInput) { in.Source.Environment = "paper" },
		"empty ID":            func(in *ManifestInput) { in.Source.DatasetID = "" },
		"ID edge space":       func(in *ManifestInput) { in.Source.DatasetID = " secret-marker" },
		"ID NUL":              func(in *ManifestInput) { in.Source.DatasetID = "x\x00" },
		"ID invalid UTF8":     func(in *ManifestInput) { in.Source.DatasetID = "\xff" },
		"ID oversized":        func(in *ManifestInput) { in.Source.DatasetID = strings.Repeat("я", 129) },
		"wrong source":        func(in *ManifestInput) { in.Source.Source = "other" },
		"wrong category":      func(in *ManifestInput) { in.Source.Category = "spot" },
		"mixed env":           func(in *ManifestInput) { in.Streams[0].Source.Environment = "mainnet" },
		"foreign ID":          func(in *ManifestInput) { in.Streams[0].Source.DatasetID = "other" },
		"mixed source":        func(in *ManifestInput) { in.Streams[0].Source.Source = "other" },
		"mixed category":      func(in *ManifestInput) { in.Streams[0].Source.Category = "spot" },
		"unsupported symbol":  func(in *ManifestInput) { in.Streams[0].Symbol = "btcusdt" },
		"other origin":        func(in *ManifestInput) { in.Streams[0].Endpoint = "https://api.bybit.com/v5/market/funding/history" },
		"trade substituted":   func(in *ManifestInput) { in.Streams[0].Endpoint = "https://api-testnet.bybit.com/v5/market/kline" },
		"unknown stream":      func(in *ManifestInput) { in.Streams[0].Kind = "synthetic" },
		"funding interval":    func(in *ManifestInput) { in.Streams[0].IntervalCode = "60" },
		"wrong mark interval": func(in *ManifestInput) { in.Streams[0].Kind, in.Streams[0].IntervalCode = "mark", "1" },
		"zero duration":       func(in *ManifestInput) { in.Range.End = in.Range.Start },
		"before epoch":        func(in *ManifestInput) { in.Range.Start = in.Range.Start.Add(-time.Millisecond) },
		"reversed stream":     func(in *ManifestInput) { in.Streams[0].Range.Start = in.Streams[0].Range.End.Add(time.Millisecond) },
		"before dataset":      func(in *ManifestInput) { in.Range.Start = in.Streams[0].Range.Start.Add(time.Millisecond) },
		"after dataset":       func(in *ManifestInput) { in.Streams[0].Range.End = in.Range.End.Add(time.Millisecond) },
		"sub-ms bound":        func(in *ManifestInput) { in.Range.End = in.Range.End.Add(time.Nanosecond) },
		"sub-ms load":         func(in *ManifestInput) { in.LoadedAt = in.LoadedAt.Add(time.Nanosecond) },
		"load out of range":   func(in *ManifestInput) { in.LoadedAt = time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"uppercase digest":    func(in *ManifestInput) { in.Streams[0].Content.SHA256 = strings.Repeat("A", 64) },
		"missing coverage":    func(in *ManifestInput) { in.Streams[0].Coverage = ManifestArtifact{} },
		"invalid format":      func(in *ManifestInput) { in.ModelAssumptions.Format = "bad format" },
		"missing margin":      func(in *ManifestInput) { in.MarginEvidence.SHA256 = "" },
		"exact duplicate":     func(in *ManifestInput) { in.Streams = append(in.Streams, in.Streams[0]) },
		"divergent duplicate": func(in *ManifestInput) {
			duplicate := in.Streams[0]
			duplicate.Content.SHA256 = strings.Repeat("0", 64)
			in.Streams = append(in.Streams, duplicate)
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := manifestFixture()
			change(&in)
			requireManifestFailure(t, in)
		})
	}
}

// manifestEvidenceFixture связывает точные байты синтетического свидетельства с его форматом.
func manifestEvidenceFixture(format, raw string) ManifestArtifact {
	return ManifestArtifact{Format: format, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))}
}

// TestEncodeManifestFundingContent связывает реальный кодек funding и явное неизвестное evidence.
func TestEncodeManifestFundingContent(t *testing.T) {
	input := manifestFixture()
	input.Source.DatasetID = "funding-A"
	input.Range = ManifestRange{time.UnixMilli(0).UTC(), time.UnixMilli(14400000).UTC()}
	input.LoadedAt = time.UnixMilli(14400001).UTC()
	input.Streams[0].Source, input.Streams[0].Range = input.Source, input.Range
	content := FundingContentInput{Environment: input.Source.Environment, DatasetID: input.Source.DatasetID,
		Source: input.Source.Source, Category: input.Source.Category, Symbol: input.Streams[0].Symbol,
		Endpoint: input.Streams[0].Endpoint, Start: input.Range.Start, End: input.Range.End}
	for i, stamp := range []int64{0, 3600000, 10800000} {
		rate, err := model.ParseRate([]string{"-0.000031", "0", "0.000004"}[i])
		if err != nil {
			t.Fatal(err)
		}
		content.Events = append(content.Events, model.FundingEvent{FundingTime: time.UnixMilli(stamp).UTC(), Rate: rate})
	}
	raw, digest, err := EncodeFundingContent(content)
	if err != nil || len(raw) != 411 || digest != "0d48873010535ed0ce14ccb15d10b609f0492e58e1b9eadc09a0a39343d16556" {
		t.Fatalf("funding content=%s digest=%q error=%v", raw, digest, err)
	}
	input.Streams[0].Content = ManifestArtifact{Format: fundingContentFormat, SHA256: digest}
	input.Streams[0].Coverage = manifestEvidenceFixture("test/coverage-status/v1", `{"status":"unknown","reason":"historical schedule unavailable"}`)
	input.MarginEvidence = manifestEvidenceFixture("test/margin-status/v1", `{"status":"unknown","reason":"historical margin rules unavailable"}`)
	input.ModelAssumptions = manifestEvidenceFixture("test/model-assumptions/v1", `{"status":"unapproved","decision":"D-07"}`)
	payload, hash, err := EncodeManifest(input)
	var wire manifestWire
	if err != nil || json.Unmarshal(payload, &wire) != nil || wire.Streams[0].Content != input.Streams[0].Content ||
		wire.Streams[0].Coverage != input.Streams[0].Coverage || wire.ModelAssumptions != input.ModelAssumptions {
		t.Fatalf("manifest=%s digest=%q error=%v", payload, hash, err)
	}
	input.Streams[0].Coverage = manifestEvidenceFixture("test/coverage-status/v1", `{"status":"unknown","reason":"a different source observation"}`)
	_, changedHash, err := EncodeManifest(input)
	if err != nil || changedHash == hash {
		t.Fatal("changed unknown evidence did not change envelope hash")
	}
	content.Events = nil
	_, emptyDigest, err := EncodeFundingContent(content)
	if err != nil || emptyDigest == digest {
		t.Fatal("observed zero event collapsed to missing content")
	}
	input.Streams[0].Content.SHA256 = emptyDigest
	_, emptyHash, err := EncodeManifest(input)
	if err != nil || emptyHash == changedHash {
		t.Fatal("changed observed content did not change envelope hash")
	}
}
