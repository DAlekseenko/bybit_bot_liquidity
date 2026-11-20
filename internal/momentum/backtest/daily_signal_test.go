package backtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
)

// TestReplayDailySignal проверяет cutoff, общий ROC и недоступность будущего close.
func TestReplayDailySignal(t *testing.T) {
	symbols := []string{"HYPEUSDT", "ETHUSDT", "BTCUSDT"}
	dataset, at := dailySignalFixture(t, "")
	before, _ := json.Marshal(dataset)
	got, err := ReplayDailySignal(dataset, symbols, at)
	if err != nil || !got["BTCUSDT"] || got["ETHUSDT"] || got["HYPEUSDT"] {
		t.Fatalf("signal: %+v, %v", got, err)
	}
	after, _ := json.Marshal(dataset)
	if !bytes.Equal(before, after) {
		t.Fatal("signal mutated input")
	}
	exact, _ := dailySignalFixture(t, "exact31")
	exactResult, err := ReplayDailySignal(exact, symbols, at)
	if err != nil || !reflect.DeepEqual(got, exactResult) {
		t.Fatal("exactly 31 closed days rejected")
	}
	future, _ := dailySignalFixture(t, "future")
	next, err := ReplayDailySignal(future, symbols, at.In(time.FixedZone("west", -25200)))
	if err != nil || !reflect.DeepEqual(got, next) {
		t.Fatal("future close changed earlier signal")
	}
	closed, _ := dailySignalFixture(t, "closed")
	changed, err := ReplayDailySignal(closed, symbols, at)
	if err != nil || changed["BTCUSDT"] {
		t.Fatal("closed day did not change signal")
	}
	got["BTCUSDT"] = false
	again, err := ReplayDailySignal(dataset, symbols, at)
	if err != nil || !reflect.DeepEqual(next, again) {
		t.Fatal("output aliases source")
	}
}

// TestReplayDailyFailures проверяет требуемые сутки, непрерывность и ошибочные входы.
func TestReplayDailyFailures(t *testing.T) {
	for _, name := range []string{"latest", "gap", "missing", "insufficient", "mutated", "zero", "outside", "symbols"} {
		t.Run(name, func(t *testing.T) {
			dataset, at := dailySignalFixture(t, name)
			symbols := []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}
			if name == "zero" {
				at = time.Time{}
			}
			if name == "outside" {
				at = dataset.Manifest.Range.End.Add(time.Hour)
			}
			if name == "symbols" {
				symbols[0] = symbols[1]
			}
			got, err := ReplayDailySignal(dataset, symbols, at)
			if got != nil || !errors.Is(err, ErrReplaySignal) {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

// dailySignalFixture добавляет 32 прошлых суток и одни будущие к рынку.
func dailySignalFixture(t *testing.T, variant string) (FrozenDataset, time.Time) {
	t.Helper()
	dataset, at := tapeFixture(t, 0), tapeStart
	span := data.ManifestRange{Start: at.AddDate(0, 0, -32), End: at.AddDate(0, 0, 1)}
	dataset.Manifest.Range = span
	dataset.Manifest.LoadedAt = span.End
	for i, symbol := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		if variant == "missing" && i == 0 {
			continue
		}
		rows := []data.TradeCandleContentRow{}
		for day := -32; day <= 0; day++ {
			if variant == "exact31" && day == -32 {
				continue
			}
			if i == 0 && (variant == "latest" && day == -1 || variant == "gap" && day == -4 || variant == "insufficient" && day < -20) {
				continue
			}
			close := "10"
			if day == -1 {
				close = []string{"20", "5", "10"}[i]
				if variant == "closed" && i == 0 {
					close = "5"
				}
			}
			if variant == "future" && day == 0 {
				close = "90"
			}
			rows = append(rows, data.TradeCandleContentRow{
				OpenTime: at.AddDate(0, 0, day), Open: tapeDecimal(t, "10"), High: tapeDecimal(t, "100"),
				Low: tapeDecimal(t, "1"), Close: tapeDecimal(t, close), Volume: model.Zero(), Confirmed: true,
			})
		}
		source := dataset.Manifest.Source
		endpoint := "https://api-testnet.bybit.com/v5/market/kline"
		raw, hash, err := data.EncodeTradeCandleContent(data.TradeCandleContentInput{
			Environment: source.Environment, DatasetID: source.DatasetID, Source: source.Source,
			Category: source.Category, Symbol: symbol, IntervalCode: "D", Endpoint: endpoint,
			Start: span.Start, End: span.End, Rows: rows,
		})
		if err != nil {
			t.Fatal(err)
		}
		dataset.Artifacts[hash] = raw
		dataset.Manifest.Streams = append(dataset.Manifest.Streams, data.ManifestStream{
			Source: source, Range: span, Kind: "trade", Symbol: symbol, IntervalCode: "D", Endpoint: endpoint,
			Content: data.ManifestArtifact{Format: "momentum/trade-candle-content/v1", SHA256: hash}, Coverage: dataset.Manifest.MarginEvidence,
		})
	}
	if variant == "mutated" {
		dataset.Artifacts[dataset.Manifest.Streams[0].Content.SHA256][0] = '!'
	}
	return dataset, at
}
