package data

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// fundingContentRate разбирает точную ставку тестового ряда.
func fundingContentRate(t *testing.T, raw string) model.Decimal {
	t.Helper()
	rate, err := model.ParseRate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return rate
}

// fundingContentFixture создаёт ряд с нерегулярными моментами и точным нулём.
func fundingContentFixture(t *testing.T) FundingContentInput {
	t.Helper()
	return FundingContentInput{
		Environment: "testnet", DatasetID: "funding-A", Source: "bybit_v5", Category: "linear", Symbol: "BTCUSDT",
		Endpoint: "https://api-testnet.bybit.com/v5/market/funding/history",
		Start:    time.UnixMilli(0).UTC(), End: time.UnixMilli(14400000).UTC(),
		Events: []model.FundingEvent{
			{FundingTime: time.UnixMilli(10800000).UTC(), Rate: fundingContentRate(t, "0.000004000")},
			{FundingTime: time.UnixMilli(0).UTC(), Rate: fundingContentRate(t, "-0.00003100")},
			{FundingTime: time.UnixMilli(3600000).UTC(), Rate: fundingContentRate(t, "-0.0000")},
		},
	}
}

// TestEncodeFundingContentGolden проверяет байты и независимый SHA-256.
func TestEncodeFundingContentGolden(t *testing.T) {
	const golden = `{"format":"momentum/funding-content/v1","environment":"testnet","dataset_id":"funding-A","source":"bybit_v5","category":"linear","symbol":"BTCUSDT","endpoint":"https://api-testnet.bybit.com/v5/market/funding/history","start_unix_ms":0,"end_unix_ms":14400000,"events":[{"funding_time_unix_ms":0,"rate":"-0.000031"},{"funding_time_unix_ms":3600000,"rate":"0"},{"funding_time_unix_ms":10800000,"rate":"0.000004"}]}`
	const goldenDigest = "0d48873010535ed0ce14ccb15d10b609f0492e58e1b9eadc09a0a39343d16556"
	in := fundingContentFixture(t)
	original := slices.Clone(in.Events)
	got, hash, err := EncodeFundingContent(in)
	if err != nil || len(got) != 411 || string(got) != golden || hash != goldenDigest {
		t.Fatalf("golden bytes=%s SHA-256=%q error=%v", got, hash, err)
	}
	if !reflect.DeepEqual(in.Events, original) {
		t.Fatal("input order changed")
	}
	got[0] = 0
	zone := time.FixedZone("west", -7*3600)
	in.Start, in.End = in.Start.In(zone), in.End.In(zone)
	slices.Reverse(in.Events)
	for i := range in.Events {
		in.Events[i].FundingTime = in.Events[i].FundingTime.In(zone)
	}
	in.Events[2].Rate = fundingContentRate(t, "0.000004")
	original = slices.Clone(in.Events)
	again, againHash, err := EncodeFundingContent(in)
	if err != nil || string(again) != golden || againHash != goldenDigest {
		t.Fatalf("permutation/time zone changed artifact: %s %s %v", again, againHash, err)
	}
	if !reflect.DeepEqual(in.Events, original) {
		t.Fatal("input order or decimal representation changed")
	}
}

// TestEncodeFundingContentIdentity проверяет, что хеш связывает все входные поля.
func TestEncodeFundingContentIdentity(t *testing.T) {
	base := fundingContentFixture(t)
	base.Events = []model.FundingEvent{base.Events[0], base.Events[2]}
	_, baseHash, err := EncodeFundingContent(base)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*FundingContentInput){
		"mainnet": func(in *FundingContentInput) {
			in.Environment = "mainnet"
			in.Endpoint = "https://api.bybit.com/v5/market/funding/history"
		},
		"dataset": func(in *FundingContentInput) { in.DatasetID = "Набор<&>" },
		"symbol":  func(in *FundingContentInput) { in.Symbol = "HYPEUSDT" },
		"end":     func(in *FundingContentInput) { in.End = in.End.Add(time.Millisecond) },
		"start":   func(in *FundingContentInput) { in.Start = in.Start.Add(time.Millisecond) },
		"rate":    func(in *FundingContentInput) { in.Events[0].Rate = fundingContentRate(t, "-0.000004") },
		"event-time": func(in *FundingContentInput) {
			in.Events[0].FundingTime = in.Events[0].FundingTime.Add(time.Millisecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := base
			in.Events = slices.Clone(base.Events)
			change(&in)
			got, hash, err := EncodeFundingContent(in)
			var wire fundingContentWire
			if err != nil || hash == baseHash || json.Unmarshal(got, &wire) != nil || wire.DatasetID != in.DatasetID {
				t.Fatalf("content=%s SHA-256=%s error=%v", got, hash, err)
			}
		})
	}
}

// TestEncodeFundingContentEdges проверяет пределы времени, decimal и пустое окно.
func TestEncodeFundingContentEdges(t *testing.T) {
	in := fundingContentFixture(t)
	in.DatasetID = strings.Repeat("я", 128)
	if _, _, err := EncodeFundingContent(in); err != nil {
		t.Fatalf("128-codepoint dataset ID rejected: %v", err)
	}
	in.Events = nil
	empty, emptyHash, err := EncodeFundingContent(in)
	if err != nil || !bytes.HasSuffix(empty, []byte(`"events":[]}`)) {
		t.Fatalf("empty content=%s error=%v", empty, err)
	}
	in.Events = []model.FundingEvent{}
	emptyAgain, emptyHashAgain, err := EncodeFundingContent(in)
	if err != nil || !bytes.Equal(empty, emptyAgain) || emptyHash != emptyHashAgain {
		t.Fatalf("empty slice changed artifact: %s %s %v", emptyAgain, emptyHashAgain, err)
	}
	in.Events = []model.FundingEvent{{FundingTime: in.Start, Rate: model.Zero()}}
	zero, zeroHash, err := EncodeFundingContent(in)
	if err != nil || zeroHash == emptyHash || !bytes.HasSuffix(zero, []byte(`"events":[{"funding_time_unix_ms":0,"rate":"0"}]}`)) {
		t.Fatalf("zero rate content=%s SHA-256=%s error=%v", zero, zeroHash, err)
	}

	raw := "-" + strings.Repeat("9", 62) + "." + strings.Repeat("9", 64)
	upper := time.Date(294276, time.December, 31, 23, 59, 59, 999000000, time.UTC)
	in.End = upper.In(time.FixedZone("east", 3600))
	in.Events[0] = model.FundingEvent{FundingTime: upper.Add(-time.Millisecond), Rate: fundingContentRate(t, raw)}
	got, _, err := EncodeFundingContent(in)
	var wire fundingContentWire
	if err != nil || json.Unmarshal(got, &wire) != nil || wire.EndMs != upper.UnixMilli() ||
		!reflect.DeepEqual(wire.Events, []fundingContentRow{{FundingTimeMs: upper.UnixMilli() - 1, Rate: raw}}) {
		t.Fatalf("upper-rail artifact=%s error=%v", got, err)
	}
}

// TestEncodeFundingContentInvalid проверяет отказы без частичного результата.
func TestEncodeFundingContentInvalid(t *testing.T) {
	one := fundingContentRate(t, "1")
	third, err := one.Quo(fundingContentRate(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	tooPrecise, err := fundingContentRate(t, "0."+strings.Repeat("0", 63)+"1").Mul(fundingContentRate(t, "0.1"))
	if err != nil {
		t.Fatal(err)
	}
	above := time.Date(294277, time.January, 1, 0, 0, 0, 0, time.UTC)
	for name, change := range map[string]func(*FundingContentInput){
		"environment":      func(in *FundingContentInput) { in.Environment = "paper" },
		"mixed-endpoint":   func(in *FundingContentInput) { in.Environment = "mainnet" },
		"source":           func(in *FundingContentInput) { in.Source = "proxy" },
		"category":         func(in *FundingContentInput) { in.Category = "spot" },
		"empty-dataset":    func(in *FundingContentInput) { in.DatasetID = "" },
		"long-dataset":     func(in *FundingContentInput) { in.DatasetID = strings.Repeat("Я", 129) },
		"whitespace":       func(in *FundingContentInput) { in.DatasetID = " opaque" },
		"invalid-utf8":     func(in *FundingContentInput) { in.DatasetID = string([]byte{0xff}) },
		"nul-dataset":      func(in *FundingContentInput) { in.DatasetID = "dataset\x00" },
		"symbol":           func(in *FundingContentInput) { in.Symbol = "btcusdt" },
		"wrong-path":       func(in *FundingContentInput) { in.Endpoint = "https://api-testnet.bybit.com/v5/market/kline" },
		"endpoint-query":   func(in *FundingContentInput) { in.Endpoint += "?symbol=BTCUSDT" },
		"epoch-rail":       func(in *FundingContentInput) { in.Start = time.UnixMilli(-1) },
		"upper-rail":       func(in *FundingContentInput) { in.End = above },
		"window-precision": func(in *FundingContentInput) { in.End = in.End.Add(time.Nanosecond) },
		"equal-bounds":     func(in *FundingContentInput) { in.End = in.Start },
		"reverse-bounds":   func(in *FundingContentInput) { in.Start = in.End.Add(time.Millisecond) },
		"before-start":     func(in *FundingContentInput) { in.Start = in.Start.Add(time.Millisecond) },
		"at-end":           func(in *FundingContentInput) { in.Events[2].FundingTime = in.End },
		"event-epoch":      func(in *FundingContentInput) { in.Events[2].FundingTime = time.UnixMilli(-1) },
		"event-upper":      func(in *FundingContentInput) { in.Events[2].FundingTime = above },
		"event-precision": func(in *FundingContentInput) {
			in.Events[2].FundingTime = in.Events[2].FundingTime.Add(time.Nanosecond)
		},
		"duplicate":      func(in *FundingContentInput) { in.Events = append(in.Events, in.Events[2]) },
		"changed-repeat": func(in *FundingContentInput) { in.Events[2].FundingTime = in.Events[1].FundingTime },
		"unset-rate":     func(in *FundingContentInput) { in.Events[2].Rate = model.Decimal{} },
		"recurring-rate": func(in *FundingContentInput) { in.Events[2].Rate = third },
		"over-scale":     func(in *FundingContentInput) { in.Events[2].Rate = tooPrecise },
	} {
		t.Run(name, func(t *testing.T) {
			in := fundingContentFixture(t)
			change(&in)
			before := slices.Clone(in.Events)
			got, hash, err := EncodeFundingContent(in)
			if got != nil || hash != "" || !errors.Is(err, ErrFundingContent) || err.Error() != ErrFundingContent.Error() ||
				!reflect.DeepEqual(in.Events, before) {
				t.Fatalf("partial or unsafe result=%s SHA-256=%q error=%v", got, hash, err)
			}
		})
	}
}
