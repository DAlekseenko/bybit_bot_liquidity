package data

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// bindingFixture создаёт 15 canonical streams с gaps, signed funding и explicit zero.
func bindingFixture(t *testing.T, environment string) (ManifestInput, map[string][]byte) {
	t.Helper()
	source := ManifestSource{environment, "binding-A", "bybit_v5", "linear"}
	span := ManifestRange{time.UnixMilli(0).UTC(), time.UnixMilli(172800000).UTC()}
	unknown := ManifestArtifact{"evidence/explicit-unknown/v1", strings.Repeat("f", 64)}
	input := ManifestInput{Source: source, Range: span, LoadedAt: span.End, ModelAssumptions: unknown, MarginEvidence: unknown}
	artifacts := make(map[string][]byte)
	origin := "https://api-testnet.bybit.com"
	if environment == "mainnet" {
		origin = "https://api.bybit.com"
	}
	zeroQty, _ := model.ParseQty("0")
	zeroRate, _ := model.ParseRate("0")
	price, _ := model.ParsePrice("100.0000000000000000000000000000000000000000000000000000000000000001")
	rate, _ := model.ParseRate("-0.0000000000000000000000000000000000000000000000000000000000000001")
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		for _, key := range []struct{ kind, interval, path string }{
			{"trade", "D", "/v5/market/kline"}, {"trade", "60", "/v5/market/kline"},
			{"mark", "D", "/v5/market/mark-price-kline"}, {"mark", "60", "/v5/market/mark-price-kline"},
			{"funding", "", "/v5/market/funding/history"},
		} {
			endpoint := origin + key.path
			var raw []byte
			var hash, format string
			var err error
			switch key.kind {
			case "trade":
				row := TradeCandleContentRow{OpenTime: span.Start, Open: price, High: price, Low: price, Close: price, Volume: zeroQty, Confirmed: true}
				raw, hash, err = EncodeTradeCandleContent(TradeCandleContentInput{environment, source.DatasetID, source.Source, source.Category, symbol, key.interval, endpoint, span.Start, span.End, []TradeCandleContentRow{row}})
				format = tradeCandleContentFormat
			case "mark":
				row := model.MarkPriceCandle{OpenTime: span.Start, Open: price, High: price, Low: price, Close: price, Confirmed: true}
				raw, hash, err = EncodeMarkPriceContent(MarkPriceContentInput{environment, source.DatasetID, source.Source, source.Category, symbol, key.interval, endpoint, span.Start, span.End, []model.MarkPriceCandle{row}})
				format = markPriceContentFormat
			case "funding":
				events := []model.FundingEvent{{FundingTime: span.Start, Rate: rate}, {FundingTime: span.Start.Add(time.Hour), Rate: zeroRate}}
				raw, hash, err = EncodeFundingContent(FundingContentInput{environment, source.DatasetID, source.Source, source.Category, symbol, endpoint, span.Start, span.End, events})
				format = fundingContentFormat
			}
			if err != nil {
				t.Fatal(err)
			}
			artifacts[hash] = raw
			input.Streams = append(input.Streams, ManifestStream{source, span, key.kind, symbol, key.interval, endpoint, ManifestArtifact{format, hash}, unknown})
		}
	}
	return input, artifacts
}

// bindingReplace перепривязывает точный digest изменённых bytes, исключая masking hash-проверкой.
func bindingReplace(input *ManifestInput, artifacts map[string][]byte, index int, raw []byte) {
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	input.Streams[index].Content.SHA256 = hash
	artifacts[hash] = raw
}

// TestManifestContentBindings проверяет все форматы и сохраняет opaque unknown evidence.
func TestManifestContentBindings(t *testing.T) {
	for _, environment := range []string{"testnet", "mainnet"} {
		input, artifacts := bindingFixture(t, environment)
		before, _, _ := EncodeManifest(input)
		saved := make(map[string][]byte)
		for hash, raw := range artifacts {
			saved[hash] = bytes.Clone(raw)
		}
		if err := CheckManifestContentBindings(input, artifacts); err != nil {
			t.Fatal(err)
		}
		after, _, _ := EncodeManifest(input)
		if !bytes.Equal(before, after) || !reflect.DeepEqual(saved, artifacts) {
			t.Fatal("input mutation")
		}
		slices.Reverse(input.Streams)
		offset := time.FixedZone("offset", 3*3600)
		for i := range input.Streams {
			input.Streams[i].Range.Start = input.Streams[i].Range.Start.In(offset)
			input.Streams[i].Range.End = input.Streams[i].Range.End.In(offset)
		}
		artifacts["unreferenced"] = []byte("opaque extra content")
		if err := CheckManifestContentBindings(input, artifacts); err != nil {
			t.Fatal("order/offset/extra content", err)
		}
		for i, stream := range input.Streams {
			raw := artifacts[stream.Content.SHA256]
			at := bytes.IndexByte(raw, '[')
			bindingReplace(&input, artifacts, i, append(bytes.Clone(raw[:at]), []byte("[]}")...))
		}
		if err := CheckManifestContentBindings(input, artifacts); err != nil {
			t.Fatal("empty is content, not evidence", err)
		}
	}
}

// TestManifestContentBindingReject проверяет каждый binding без переопределения row semantics.
func TestManifestContentBindingReject(t *testing.T) {
	for _, kind := range []string{"trade", "mark", "funding"} {
		t.Run(kind, func(t *testing.T) {
			for _, name := range []string{"missing", "nil", "empty", "hash", "format", "wrong-kind", "dataset", "environment", "symbol", "interval", "start", "end", "source", "category", "endpoint", "whitespace", "malformed", "unknown-field"} {
				t.Run(name, func(t *testing.T) {
					input, artifacts := bindingFixture(t, "testnet")
					index := slices.IndexFunc(input.Streams, func(s ManifestStream) bool { return s.Kind == kind })
					stream := &input.Streams[index]
					raw := bytes.Clone(artifacts[stream.Content.SHA256])
					switch name {
					case "missing":
						delete(artifacts, stream.Content.SHA256)
					case "nil":
						artifacts[stream.Content.SHA256] = nil
					case "empty":
						artifacts[stream.Content.SHA256] = []byte{}
					case "hash":
						artifacts[stream.Content.SHA256][0] = 'x'
					case "format":
						stream.Content.Format = "other/content/v1"
					case "wrong-kind":
						other := (index + 2) % len(input.Streams)
						stream.Content = input.Streams[other].Content
					default:
						replacements := map[string][2]string{
							"dataset":       {`"binding-A"`, `"foreign"`},
							"environment":   {"api-testnet.bybit.com", "api.bybit.com"},
							"symbol":        {"BTCUSDT", "SOLUSDT"},
							"interval":      {`"D"`, `"60"`},
							"start":         {`"start_unix_ms":0`, `"start_unix_ms":86400000`},
							"end":           {`"end_unix_ms":172800000`, `"end_unix_ms":259200000`},
							"source":        {"bybit_v5", "foreign-source"},
							"category":      {"linear", "inverse"},
							"endpoint":      {"https://api-testnet.bybit.com", "https://foreign.invalid"},
							"unknown-field": {`"format":`, `"unknown":1,"format":`},
						}
						if name == "interval" && kind == "funding" {
							raw = bytes.Replace(raw, []byte(`"symbol":`), []byte(`"interval_code":"60","symbol":`), 1)
						} else if name == "whitespace" {
							raw = append(raw, '\n')
						} else if name == "malformed" {
							raw = raw[:len(raw)-1]
						} else {
							change := replacements[name]
							raw = bytes.ReplaceAll(raw, []byte(change[0]), []byte(change[1]))
						}
						if name == "environment" {
							raw = bytes.Replace(raw, []byte(`"environment":"testnet"`), []byte(`"environment":"mainnet"`), 1)
						}
						bindingReplace(&input, artifacts, index, raw)
					}
					beforeStreams := slices.Clone(input.Streams)
					beforeArtifacts := make(map[string][]byte, len(artifacts))
					for hash, payload := range artifacts {
						beforeArtifacts[hash] = bytes.Clone(payload)
					}
					if err := CheckManifestContentBindings(input, artifacts); !errors.Is(err, ErrManifestContentBinding) || err.Error() != ErrManifestContentBinding.Error() {
						t.Fatalf("safe binding failure: %v", err)
					}
					if !reflect.DeepEqual(input.Streams, beforeStreams) || !reflect.DeepEqual(artifacts, beforeArtifacts) {
						t.Fatal("input mutation on binding failure")
					}
				})
			}
		})
	}
}

// TestManifestContentBindingMetadata проверяет валидные foreign metadata и общий отказ shape.
func TestManifestContentBindingMetadata(t *testing.T) {
	for _, name := range []string{"empty-manifest", "mixed-source", "duplicate", "dataset", "environment", "symbol", "interval", "start", "end"} {
		t.Run(name, func(t *testing.T) {
			input, artifacts := bindingFixture(t, "testnet")
			switch name {
			case "empty-manifest":
				input.Streams = nil
			case "mixed-source":
				input.Streams[0].Source.Environment = "mainnet"
			case "duplicate":
				input.Streams = append(input.Streams, input.Streams[0])
			case "dataset", "environment":
				if name == "dataset" {
					input.Source.DatasetID = "foreign"
				} else {
					input.Source.Environment = "mainnet"
				}
				for i := range input.Streams {
					input.Streams[i].Source = input.Source
					if name == "environment" {
						input.Streams[i].Endpoint = strings.ReplaceAll(input.Streams[i].Endpoint, "api-testnet", "api")
					}
				}
			case "symbol":
				input.Streams[0].Symbol = "SOLUSDT"
			case "interval":
				input.Streams[0].IntervalCode = "60"
				input.Streams = input.Streams[:1]
			case "start":
				input.Streams[0].Range.Start = input.Range.Start.Add(24 * time.Hour)
			case "end":
				input.Streams[0].Range.End = input.Range.End.Add(-24 * time.Hour)
			}
			if err := CheckManifestContentBindings(input, artifacts); !errors.Is(err, ErrManifestContentBinding) {
				t.Fatal("metadata binding", err)
			}
		})
	}
}
