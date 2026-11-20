package backtest

import (
	"bytes"
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
)

// TestReplaySchedule проверяет фактические события, недели и конечную границу.
func TestReplaySchedule(t *testing.T) {
	dataset, start, end := scheduleFixture(t, 336, "")
	before, _ := json.Marshal(dataset)
	symbols := []string{"HYPEUSDT", "BTCUSDT", "ETHUSDT"}
	got, err := BuildReplaySchedule(dataset, symbols, start, end)
	if err != nil || len(got) != 337 || len(got[0].Funding) != 3 {
		t.Fatalf("schedule: %d, %v", len(got), err)
	}
	after, _ := json.Marshal(dataset)
	if !bytes.Equal(before, after) {
		t.Fatal("schedule mutated input")
	}
	if replayFundingID(strings.Repeat("a", 64), "BTCUSDT", start) != "bbafc028e3a5269a710821172c9f978a4881a989094f88e0b4a94a17fc7331a3" {
		t.Fatal("funding ID framing changed")
	}
	weeks := 0
	seen := map[string]bool{}
	for i, point := range got {
		if point.NominalWeekly {
			weeks++
			if i != 0 && i != 168 {
				t.Fatal("unexpected weekly boundary")
			}
		}
		for j, event := range point.Funding {
			raw, err := hex.DecodeString(event.FundingID)
			cmp, priceErr := event.MarkPrice.Cmp(point.Market.Marks[j].Price)
			if err != nil || len(raw) != 32 || seen[event.FundingID] || priceErr != nil || cmp != 0 || !event.Event.FundingTime.Equal(point.Market.At) {
				t.Fatal("funding identity or mark mismatch")
			}
			seen[event.FundingID] = true
		}
	}
	if weeks != 2 || len(got[336].Funding) != 0 || got[336].NominalWeekly || !got[336].Market.Terminal {
		t.Fatal("incorrect terminal or weekly count")
	}
	slices.Reverse(dataset.Manifest.Streams)
	again, err := BuildReplaySchedule(dataset, symbols, start.In(time.FixedZone("east", 10800)), end)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal("unstable ordering or timezone")
	}
	got[0].Funding[0].Symbol = "changed"
	repeated, err := BuildReplaySchedule(dataset, symbols, start, end)
	if err != nil || !reflect.DeepEqual(again, repeated) {
		t.Fatal("output changed input")
	}
	between, err := BuildReplaySchedule(dataset, symbols, start.Add(time.Hour), start.Add(169*time.Hour))
	if err != nil || between[0].NominalWeekly || !between[167].NominalWeekly {
		t.Fatal("between-Monday schedule")
	}
}

// TestReplayScheduleEdges проверяет нулевые наблюдения и ошибки присоединения.
func TestReplayScheduleEdges(t *testing.T) {
	for _, name := range []string{"empty-funding", "missing-funding", "short-span", "off-grid", "bad-market"} {
		t.Run(name, func(t *testing.T) {
			dataset, start, end := scheduleFixture(t, 2, name)
			got, err := BuildReplaySchedule(dataset, []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}, start, end)
			if name == "empty-funding" {
				if err != nil || len(got[0].Funding) != 0 {
					t.Fatal("empty observations rejected")
				}
				return
			}
			if got != nil || !errors.Is(err, ErrReplaySchedule) {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

// scheduleFixture расширяет существующий канонический рынок и добавляет funding.
func scheduleFixture(t *testing.T, hours int, variant string) (FrozenDataset, time.Time, time.Time) {
	t.Helper()
	dataset, start := tapeFixture(t, 0), tapeStart
	end := start.Add(time.Duration(hours) * time.Hour)
	dataset.Manifest.Range.End = end.Add(time.Hour)
	for i, stream := range dataset.Manifest.Streams {
		var raw []byte
		var hash string
		var err error
		if stream.Kind == "mark" {
			content, _ := data.DecodeMarkPriceContent(dataset.Artifacts[stream.Content.SHA256])
			row := content.Rows[0]
			content.Rows = nil
			content.End = end
			for at := start.Add(-time.Hour); at.Before(end); at = at.Add(time.Hour) {
				row.OpenTime = at
				content.Rows = append(content.Rows, row)
			}
			raw, hash, err = data.EncodeMarkPriceContent(content)
		} else {
			content, _ := data.DecodeTradeCandleContent(dataset.Artifacts[stream.Content.SHA256])
			row := content.Rows[0]
			content.Rows = nil
			content.End = end
			for at := start; at.Before(end); at = at.Add(time.Hour) {
				row.OpenTime = at
				content.Rows = append(content.Rows, row)
			}
			raw, hash, err = data.EncodeTradeCandleContent(content)
		}
		if err != nil {
			t.Fatal(err)
		}
		dataset.Artifacts[hash] = raw
		dataset.Manifest.Streams[i].Content.SHA256 = hash
		dataset.Manifest.Streams[i].Range.End = end
	}
	for i, symbol := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		if variant == "missing-funding" && i == 0 {
			continue
		}
		span := dataset.Manifest.Range
		if variant == "short-span" {
			span.Start = start.Add(time.Hour)
		}
		rate, _ := model.ParseRate([]string{"0.01", "-0.02", "0"}[i])
		events := []model.FundingEvent{}
		for _, at := range []time.Time{span.Start, start, start.Add(time.Hour), end} {
			if at.Before(span.Start) || !at.Before(span.End) || variant == "empty-funding" {
				continue
			}
			if variant == "off-grid" && at.Equal(start) {
				at = at.Add(30 * time.Minute)
			}
			if len(events) == 0 || !events[len(events)-1].FundingTime.Equal(at) {
				events = append(events, model.FundingEvent{FundingTime: at, Rate: rate})
			}
		}
		source := dataset.Manifest.Source
		endpoint := "https://api-testnet.bybit.com/v5/market/funding/history"
		raw, hash, err := data.EncodeFundingContent(data.FundingContentInput{
			Environment: source.Environment, DatasetID: source.DatasetID, Source: source.Source,
			Category: source.Category, Symbol: symbol, Endpoint: endpoint, Start: span.Start, End: span.End, Events: events,
		})
		if err != nil {
			t.Fatal(err)
		}
		dataset.Artifacts[hash] = raw
		dataset.Manifest.Streams = append(dataset.Manifest.Streams, data.ManifestStream{
			Source: source, Range: span, Kind: "funding", Symbol: symbol, Endpoint: endpoint,
			Content:  data.ManifestArtifact{Format: "momentum/funding-content/v1", SHA256: hash},
			Coverage: dataset.Manifest.MarginEvidence,
		})
	}
	if variant == "bad-market" {
		dataset.Manifest.Streams[0].Symbol = "OTHERUSDT"
	}
	return dataset, start, end
}
