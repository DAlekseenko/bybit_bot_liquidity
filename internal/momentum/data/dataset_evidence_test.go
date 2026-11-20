package data_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/storage"
)

// evidenceFixture задаёт только явно supplied unknown, без inference расписания/маржи.
func evidenceFixture() data.DatasetEvidenceInput {
	span := data.ManifestRange{Start: time.UnixMilli(0).UTC(), End: time.UnixMilli(86400000).UTC()}
	return data.DatasetEvidenceInput{Source: data.ManifestSource{Environment: "testnet", DatasetID: "opaque/α", Source: "bybit_v5", Category: "linear"}, Range: span,
		Streams:          []data.StreamEvidence{{Kind: "funding", Symbol: "BTCUSDT", Range: span, ContentSHA256: strings.Repeat("a", 64), Status: "unknown", Reason: "expected_funding_schedule_not_supplied"}},
		HistoricalMargin: data.HistoricalMarginEvidence{Status: "unknown", Reason: "historical_margin_rules_not_supplied"}}
}

// evidenceOK не продолжает сценарий при нарушении инварианта.
func evidenceOK(t *testing.T, ok bool) {
	t.Helper()
	if !ok {
		t.Fatal("evidence invariant failed")
	}
}

// evidenceEncode проверяет точный independent SHA, не только повтор serializer.
func evidenceEncode(t *testing.T, in data.DatasetEvidenceInput) ([]byte, string) {
	t.Helper()
	b, h, e := data.EncodeDatasetEvidence(in)
	evidenceOK(t, e == nil)
	sum := sha256.Sum256(b)
	evidenceOK(t, h == hex.EncodeToString(sum[:]) && len(h) == 64)
	return b, h
}

// TestDatasetEvidenceGolden фиксирует wire v1 и literal unknown, а не disabled или0.
func TestDatasetEvidenceGolden(t *testing.T) {
	const golden = `{"format":"momentum/dataset-evidence/v1","source":{"environment":"testnet","dataset_id":"opaque/α","source":"bybit_v5","category":"linear"},"range":{"start_ms":0,"end_ms":86400000},"streams":[{"kind":"funding","symbol":"BTCUSDT","range":{"start_ms":0,"end_ms":86400000},"content_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"unknown","reason":"expected_funding_schedule_not_supplied","funding_comparison":null}],"historical_margin":{"status":"unknown","reason":"historical_margin_rules_not_supplied"}}`
	in := evidenceFixture()
	b, h := evidenceEncode(t, in)
	evidenceOK(t, string(b) == golden && h == "782c2d4c53c7903e2364f98e9c177be4e52749dd9fcf9e4593f0cc78877c0d09")
	b[0] = 'x'
	again, _ := evidenceEncode(t, in)
	evidenceOK(t, string(again) == golden)
	zone := time.FixedZone("east", 19800)
	in.Range.Start, in.Range.End = in.Range.Start.In(zone), in.Range.End.In(zone)
	in.Streams[0].Range = in.Range
	b, h = evidenceEncode(t, in)
	evidenceOK(t, string(b) == golden)
	manifest := data.ManifestInput{Source: in.Source, Range: in.Range, LoadedAt: in.Range.End, ModelAssumptions: data.ManifestArtifact{Format: "model/unapproved-v1", SHA256: strings.Repeat("b", 64)}, MarginEvidence: data.ManifestArtifact{Format: data.DatasetEvidenceFormat, SHA256: h}, Streams: []data.ManifestStream{{Source: in.Source, Range: in.Range, Kind: "funding", Symbol: "BTCUSDT", Endpoint: "https://api-testnet.bybit.com/v5/market/funding/history", Content: data.ManifestArtifact{Format: "momentum/funding-content/v1", SHA256: in.Streams[0].ContentSHA256}, Coverage: data.ManifestArtifact{Format: data.DatasetEvidenceFormat, SHA256: h}}}}
	_, _, e := data.EncodeManifest(manifest)
	evidenceOK(t, e == nil)
}

// TestDatasetEvidenceProducers сохраняет результаты actual model и READY314 без новой классификации.
func TestDatasetEvidenceProducers(t *testing.T) {
	in := evidenceFixture()
	in.Streams = nil
	expected := []time.Time{time.UnixMilli(123).UTC(), time.UnixMilli(4567).UTC()}
	observed := []model.FundingEvent{{FundingTime: expected[0], Rate: model.Zero()}, {FundingTime: time.UnixMilli(9876).UTC(), Rate: model.Zero()}}
	coverage, e := model.CompareFundingCoverage(expected, observed)
	evidenceOK(t, e == nil)
	evidenceOK(t, len(coverage.Missing) == 1 && coverage.Missing[0].Equal(expected[1]) && len(coverage.Unexpected) == 1)
	for _, symbol := range []string{"HYPEUSDT", "ETHUSDT", "BTCUSDT"} {
		for _, kind := range []string{"trade", "mark"} {
			for _, interval := range []string{"60", "D"} {
				row := data.StreamEvidence{Kind: kind, Symbol: symbol, IntervalCode: interval, ContentSHA256: strings.Repeat("a", 64), Range: in.Range, Status: "unknown", Reason: "coverage_not_assessed"}
				if interval == "60" {
					rows := make([]storage.StoredCandle, 24)
					for i := range rows {
						rows[i] = storage.StoredCandle{OpenTime: in.Range.Start.Add(time.Duration(i) * time.Hour), Confirmed: true}
					}
					if kind == "mark" {
						rows = append(rows[:7], rows[8:]...)
					}
					status, e := storage.CheckHourlyCandleCoverage(storage.CandleStream{Environment: in.Source.Environment, DatasetID: in.Source.DatasetID, Symbol: symbol, IntervalCode: interval}, in.Range.Start, in.Range.End, rows)
					evidenceOK(t, e == nil)
					row.Status, row.Reason = string(status), ""
				}
				in.Streams = append(in.Streams, row)
			}
		}
		in.Streams = append(in.Streams, data.StreamEvidence{Kind: "funding", Symbol: symbol, ContentSHA256: strings.Repeat("c", 64), Range: in.Range, Status: "compared", Funding: &data.FundingComparisonEvidence{Expected: expected, Coverage: coverage}})
	}
	b, h := evidenceEncode(t, in)
	evidenceOK(t, len(in.Streams) == 15 && bytes.Contains(b, []byte(`"status":"gap"`)) && bytes.Contains(b, []byte(`"status":"covered"`)) && bytes.Contains(b, []byte(`"missing_times_ms":[4567]`)) && bytes.Contains(b, []byte(`"unexpected_times_ms":[9876]`)))
	original := append([]data.StreamEvidence(nil), in.Streams...)
	reversed := in
	reversed.Streams = append([]data.StreamEvidence(nil), in.Streams...)
	slices.Reverse(reversed.Streams)
	again, hash := evidenceEncode(t, reversed)
	evidenceOK(t, bytes.Equal(b, again) && h == hash && reflect.DeepEqual(original, in.Streams))
	in.Streams[0].ContentSHA256 = strings.Repeat("d", 64)
	_, changed := evidenceEncode(t, in)
	evidenceOK(t, h != changed)
}

// TestDatasetEvidenceComparedNoGaps сохраняет compared[] отдельно от неизвестных списков null.
func TestDatasetEvidenceComparedNoGaps(t *testing.T) {
	in := evidenceFixture()
	unknown, hash := evidenceEncode(t, in)
	in.Streams[0].Status, in.Streams[0].Reason = "compared", ""
	in.Streams[0].Funding = &data.FundingComparisonEvidence{Expected: []time.Time{time.UnixMilli(123).UTC()}, Coverage: model.FundingCoverage{}}
	compared, other := evidenceEncode(t, in)
	evidenceOK(t, hash != other && bytes.Contains(unknown, []byte(`"funding_comparison":null`)) && bytes.Contains(compared, []byte(`"missing_times_ms":[],"unexpected_times_ms":[]`)))
	for _, status := range []string{"covered", "empty", "short_prefix", "gap", "unconfirmed", "unknown"} {
		in := evidenceFixture()
		in.Streams[0].Kind, in.Streams[0].IntervalCode, in.Streams[0].Status = "mark", "60", status
		in.Streams[0].Reason = ""
		if status == "unknown" {
			in.Streams[0].Reason = "coverage_not_assessed"
		}
		_, _ = evidenceEncode(t, in)
	}
}

// TestDatasetEvidenceRejects проверяет malformed shape, не превращая corruption в unknown.
func TestDatasetEvidenceRejects(t *testing.T) {
	for name, mutate := range map[string]func(*data.DatasetEvidenceInput){
		"source":            func(in *data.DatasetEvidenceInput) { in.Source.Source = "foreign" },
		"category":          func(in *data.DatasetEvidenceInput) { in.Source.Category = "spot" },
		"environment":       func(in *data.DatasetEvidenceInput) { in.Source.Environment = "Mainnet" },
		"opaque id":         func(in *data.DatasetEvidenceInput) { in.Source.DatasetID = "secret-marker\x00" },
		"header time":       func(in *data.DatasetEvidenceInput) { in.Range.Start = time.UnixMilli(-1) },
		"empty range":       func(in *data.DatasetEvidenceInput) { in.Range.End = in.Range.Start },
		"sub ms":            func(in *data.DatasetEvidenceInput) { in.Range.End = in.Range.End.Add(time.Nanosecond) },
		"overflow":          func(in *data.DatasetEvidenceInput) { in.Range.End = time.Date(294277, 1, 1, 0, 0, 0, 0, time.UTC) },
		"no records":        func(in *data.DatasetEvidenceInput) { in.Streams = nil },
		"bad kind":          func(in *data.DatasetEvidenceInput) { in.Streams[0].Kind = "margin" },
		"bad symbol":        func(in *data.DatasetEvidenceInput) { in.Streams[0].Symbol = "BTC" },
		"bad hash":          func(in *data.DatasetEvidenceInput) { in.Streams[0].ContentSHA256 = strings.Repeat("A", 64) },
		"stream range":      func(in *data.DatasetEvidenceInput) { in.Streams[0].Range.End = in.Range.End.Add(time.Millisecond) },
		"zero stream range": func(in *data.DatasetEvidenceInput) { in.Streams[0].Range.End = in.Streams[0].Range.Start },
		"interval funding":  func(in *data.DatasetEvidenceInput) { in.Streams[0].IntervalCode = "60" },
		"interval candle":   func(in *data.DatasetEvidenceInput) { in.Streams[0].Kind = "trade" },
		"status funding":    func(in *data.DatasetEvidenceInput) { in.Streams[0].Status = "covered" },
		"reason funding":    func(in *data.DatasetEvidenceInput) { in.Streams[0].Reason = "funding_disabled" },
		"known margin":      func(in *data.DatasetEvidenceInput) { in.HistoricalMargin.Status = "available" },
		"disabled margin":   func(in *data.DatasetEvidenceInput) { in.HistoricalMargin.Status = "disabled" },
		"reason margin":     func(in *data.DatasetEvidenceInput) { in.HistoricalMargin.Reason = "margin_zero" },
		"duplicate":         func(in *data.DatasetEvidenceInput) { in.Streams = append(in.Streams, in.Streams[0]) },
		"divergent duplicate": func(in *data.DatasetEvidenceInput) {
			copy := in.Streams[0]
			copy.ContentSHA256 = strings.Repeat("b", 64)
			in.Streams = append(in.Streams, copy)
		},
		"unknown comparison": func(in *data.DatasetEvidenceInput) { in.Streams[0].Funding = &data.FundingComparisonEvidence{} },
		"compared no list":   func(in *data.DatasetEvidenceInput) { in.Streams[0].Status, in.Streams[0].Reason = "compared", "" },
		"candle status": func(in *data.DatasetEvidenceInput) {
			in.Streams[0].Kind, in.Streams[0].IntervalCode, in.Streams[0].Status = "mark", "D", "ready"
		},
		"candle reason": func(in *data.DatasetEvidenceInput) { in.Streams[0].Kind, in.Streams[0].IntervalCode = "trade", "D" },
		"candle known reason": func(in *data.DatasetEvidenceInput) {
			in.Streams[0].Kind, in.Streams[0].IntervalCode, in.Streams[0].Status = "mark", "60", "gap"
		},
		"candle comparison": func(in *data.DatasetEvidenceInput) {
			in.Streams[0].Kind, in.Streams[0].IntervalCode = "mark", "60"
			in.Streams[0].Funding = &data.FundingComparisonEvidence{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := evidenceFixture()
			mutate(&in)
			b, h, e := data.EncodeDatasetEvidence(in)
			evidenceOK(t, b == nil && h == "" && errors.Is(e, data.ErrDatasetEvidence) && !strings.Contains(e.Error(), "secret-marker"))
		})
	}
	for _, which := range []string{"expected", "missing", "unexpected"} {
		for _, times := range [][]time.Time{{time.UnixMilli(-1)}, {time.UnixMilli(86400000)}, {time.UnixMilli(3).Add(time.Nanosecond)}, {time.UnixMilli(3), time.UnixMilli(3)}, {time.UnixMilli(4), time.UnixMilli(3)}} {
			in := evidenceFixture()
			in.Streams[0].Status, in.Streams[0].Reason = "compared", ""
			c := &data.FundingComparisonEvidence{Expected: []time.Time{time.UnixMilli(123)}}
			switch which {
			case "expected":
				c.Expected = times
			case "missing":
				c.Coverage.Missing = times
			case "unexpected":
				c.Coverage.Unexpected = times
			}
			in.Streams[0].Funding = c
			b, h, e := data.EncodeDatasetEvidence(in)
			evidenceOK(t, b == nil && h == "" && errors.Is(e, data.ErrDatasetEvidence))
		}
	}
	in := evidenceFixture()
	in.Streams[0].Status, in.Streams[0].Reason = "compared", ""
	in.Streams[0].Funding = &data.FundingComparisonEvidence{}
	b, h, e := data.EncodeDatasetEvidence(in)
	evidenceOK(t, b == nil && h == "" && errors.Is(e, data.ErrDatasetEvidence))
	var decoded map[string]any
	good, _ := evidenceEncode(t, evidenceFixture())
	evidenceOK(t, json.Unmarshal(good, &decoded) == nil && len(decoded) == 5)
}

// TestDatasetEvidenceBindings фиксирует source/content/window в независимом evidence SHA.
func TestDatasetEvidenceBindings(t *testing.T) {
	_, base := evidenceEncode(t, evidenceFixture())
	for _, change := range []func(*data.DatasetEvidenceInput){
		func(in *data.DatasetEvidenceInput) { in.Source.Environment = "mainnet" },
		func(in *data.DatasetEvidenceInput) { in.Source.DatasetID = "another exact id" },
		func(in *data.DatasetEvidenceInput) { in.Range.End = in.Range.End.Add(time.Millisecond) },
		func(in *data.DatasetEvidenceInput) {
			in.Streams[0].Range.End = in.Streams[0].Range.End.Add(-time.Millisecond)
		},
		func(in *data.DatasetEvidenceInput) { in.Streams[0].ContentSHA256 = strings.Repeat("b", 64) },
	} {
		in := evidenceFixture()
		change(&in)
		_, hash := evidenceEncode(t, in)
		evidenceOK(t, hash != base)
	}
}
