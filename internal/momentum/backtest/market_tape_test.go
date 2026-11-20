package backtest

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
)

var tapeSymbols = []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}
var tapeStart = time.Date(2025, 7, 7, 0, 0, 0, 0, time.UTC)

// tapeDecimal создаёт точную тестовую цену.
func tapeDecimal(t *testing.T, value string) model.Decimal {
	t.Helper()
	got, err := model.ParsePrice(value)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// tapeEqual сравнивает точные значения Decimal.
func tapeEqual(left, right model.Decimal) bool {
	order, err := left.Cmp(right)
	return err == nil && order == 0
}

// tapeFixture строит связанные manifest и три часовых потока через штатные кодеки.
func tapeFixture(t *testing.T, variant int) FrozenDataset {
	t.Helper()
	rangeStart, rangeEnd := tapeStart.Add(-time.Hour), tapeStart.Add(2*time.Hour)
	source := data.ManifestSource{Environment: "testnet", DatasetID: "tape", Source: "bybit_v5", Category: "linear"}
	artifacts := make(map[string][]byte)
	put := func(raw []byte, hash string) data.ManifestArtifact {
		artifacts[hash] = raw
		return data.ManifestArtifact{Format: "opaque/v1", SHA256: hash}
	}
	digest := func(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
	assume, evidence := []byte("model"), []byte("margin")
	manifest := data.ManifestInput{Source: source, Range: data.ManifestRange{Start: rangeStart, End: rangeEnd}, LoadedAt: rangeEnd.Add(time.Hour),
		ModelAssumptions: put(assume, digest(assume)), MarginEvidence: put(evidence, digest(evidence))}
	for si, symbol := range tapeSymbols {
		marks := make([]model.MarkPriceCandle, 0, 3)
		trades := make([]data.TradeCandleContentRow, 0, 2)
		for h := 0; h < 3; h++ {
			if variant == 3 && h == 0 {
				continue
			}
			price := fmt.Sprint(10 + si*10 + h)
			high, low, close := price, price, price
			if variant == 1 && h == 1 {
				high, low, close = "777", fmt.Sprint(9+si*10+h), "777"
			}
			marks = append(marks, model.MarkPriceCandle{OpenTime: rangeStart.Add(time.Duration(h) * time.Hour), Open: tapeDecimal(t, price), High: tapeDecimal(t, high), Low: tapeDecimal(t, low), Close: tapeDecimal(t, close), Confirmed: true})
		}
		for h := 0; h < 2; h++ {
			if variant == 2 && h == 0 {
				continue
			}
			price := fmt.Sprint(100 + si*10 + h)
			trades = append(trades, data.TradeCandleContentRow{OpenTime: tapeStart.Add(time.Duration(h) * time.Hour), Open: tapeDecimal(t, price), High: tapeDecimal(t, price), Low: tapeDecimal(t, price), Close: tapeDecimal(t, price), Volume: model.Zero(), Confirmed: true})
		}
		markRaw, markHash, err := data.EncodeMarkPriceContent(data.MarkPriceContentInput{Environment: "testnet", DatasetID: "tape", Source: "bybit_v5", Category: "linear", Symbol: symbol, IntervalCode: "60", Endpoint: "https://api-testnet.bybit.com/v5/market/mark-price-kline", Start: rangeStart, End: rangeEnd, Rows: marks})
		if err != nil {
			t.Fatal(err)
		}
		tradeRaw, tradeHash, err := data.EncodeTradeCandleContent(data.TradeCandleContentInput{Environment: "testnet", DatasetID: "tape", Source: "bybit_v5", Category: "linear", Symbol: symbol, IntervalCode: "60", Endpoint: "https://api-testnet.bybit.com/v5/market/kline", Start: rangeStart, End: rangeEnd, Rows: trades})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range []struct {
			kind                   string
			raw                    []byte
			hash, format, endpoint string
		}{{"mark", markRaw, markHash, "momentum/mark-price-content/v1", "https://api-testnet.bybit.com/v5/market/mark-price-kline"}, {"trade", tradeRaw, tradeHash, "momentum/trade-candle-content/v1", "https://api-testnet.bybit.com/v5/market/kline"}} {
			ref := put(item.raw, item.hash)
			ref.Format = item.format
			coverage := put([]byte("coverage-"+symbol+item.kind), digest([]byte("coverage-"+symbol+item.kind)))
			manifest.Streams = append(manifest.Streams, data.ManifestStream{Source: source, Range: data.ManifestRange{Start: rangeStart, End: rangeEnd}, Kind: item.kind, Symbol: symbol, IntervalCode: "60", Endpoint: item.endpoint, Content: ref, Coverage: coverage})
		}
	}
	return FrozenDataset{Manifest: manifest, Artifacts: artifacts}
}

// TestBuildMarketBoundariesProjectsClosedMarks проверяет UTC, порядок и ссылки цен.
func TestBuildMarketBoundariesProjectsClosedMarks(t *testing.T) {
	start := tapeStart.In(time.FixedZone("east", 3*3600))
	got, err := BuildMarketBoundaries(tapeFixture(t, 0), []string{"HYPEUSDT", "BTCUSDT", "ETHUSDT"}, start, tapeStart.Add(2*time.Hour))
	if err != nil || len(got) != 3 {
		t.Fatalf("groups=%d err=%v", len(got), err)
	}
	for i, group := range got {
		if !group.At.Equal(tapeStart.Add(time.Duration(i)*time.Hour)) || group.At.Location() != time.UTC || group.Terminal != (i == 2) || len(group.Marks) != 3 {
			t.Fatalf("group %d: %#v", i, group)
		}
		for j, symbol := range tapeSymbols {
			want := tapeDecimal(t, fmt.Sprint(10+j*10+i))
			if group.Marks[j].Symbol != symbol || !group.Marks[j].ObservedAt.Equal(group.At) || !tapeEqual(group.Marks[j].Price, want) {
				t.Fatalf("mark %d/%d: %#v", i, j, group.Marks[j])
			}
		}
		if i == 2 {
			if len(group.TradeOpens) != 0 {
				t.Fatalf("terminal opens: %#v", group.TradeOpens)
			}
			continue
		}
		if len(group.TradeOpens) != 3 {
			t.Fatalf("opens: %#v", group.TradeOpens)
		}
		for j, open := range group.TradeOpens {
			if open.Symbol != tapeSymbols[j] || !tapeEqual(open.Price, tapeDecimal(t, fmt.Sprint(100+j*10+i))) {
				t.Fatalf("open: %#v", open)
			}
		}
	}
	second, err := BuildMarketBoundaries(tapeFixture(t, 0), tapeSymbols, tapeStart, tapeStart.Add(2*time.Hour))
	if err != nil || !reflect.DeepEqual(got, second) {
		t.Fatalf("repeat differs: %v", err)
	}
}

// TestBuildMarketBoundariesNoLookahead проверяет, что текущий бар виден только на следующей границе.
func TestBuildMarketBoundariesNoLookahead(t *testing.T) {
	base, err := BuildMarketBoundaries(tapeFixture(t, 0), tapeSymbols, tapeStart, tapeStart.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := BuildMarketBoundaries(tapeFixture(t, 1), tapeSymbols, tapeStart, tapeStart.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for i := range tapeSymbols {
		if !tapeEqual(base[0].Marks[i].Price, changed[0].Marks[i].Price) || tapeEqual(base[1].Marks[i].Price, changed[1].Marks[i].Price) {
			t.Fatalf("lookahead at index %d", i)
		}
	}
}

// TestBuildMarketBoundariesRejectsIncompleteInputs проверяет fail-closed границы и bindings.
func TestBuildMarketBoundariesRejectsIncompleteInputs(t *testing.T) {
	cases := map[string]func(*FrozenDataset, *time.Time, *time.Time){
		"missing stream":  func(d *FrozenDataset, _, _ *time.Time) { d.Manifest.Streams = d.Manifest.Streams[1:] },
		"symbol mismatch": func(d *FrozenDataset, _, _ *time.Time) { d.Manifest.Streams[0].Symbol = "OTHERUSDT" },
		"mutated bytes": func(d *FrozenDataset, _, _ *time.Time) {
			hash := d.Manifest.Streams[0].Content.SHA256
			d.Artifacts[hash] = []byte("changed")
		},
		"missing prior mark":      func(d *FrozenDataset, _, _ *time.Time) { *d = tapeFixture(t, 3) },
		"missing trade open":      func(d *FrozenDataset, _, _ *time.Time) { *d = tapeFixture(t, 2) },
		"insufficient prehistory": func(_ *FrozenDataset, s, _ *time.Time) { *s = tapeStart.Add(-time.Hour) },
		"unaligned":               func(_ *FrozenDataset, s, _ *time.Time) { *s = s.Add(time.Minute) },
		"zero start":              func(_ *FrozenDataset, s, _ *time.Time) { *s = time.Time{} },
		"reversed":                func(_ *FrozenDataset, s, e *time.Time) { *s, *e = *e, *s },
		"empty":                   func(_ *FrozenDataset, s, e *time.Time) { *s, *e = tapeStart, tapeStart },
		"out of bounds":           func(_ *FrozenDataset, _, e *time.Time) { *e = e.Add(2 * time.Hour) },
		"symbols":                 func(_ *FrozenDataset, _, _ *time.Time) {},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := tapeFixture(t, 0)
			start, end := tapeStart, tapeStart.Add(time.Hour)
			baseline, baselineErr := BuildMarketBoundaries(d, tapeSymbols, start, end)
			if baselineErr != nil || len(baseline) == 0 {
				t.Fatalf("fixture baseline invalid: %v", baselineErr)
			}
			mutate(&d, &start, &end)
			symbols := tapeSymbols
			if name == "symbols" {
				symbols = []string{"BTCUSDT", "BTCUSDT", "HYPEUSDT"}
			}
			got, err := BuildMarketBoundaries(d, symbols, start, end)
			if !errors.Is(err, ErrMarketTape) || got != nil {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

// TestBuildMarketBoundariesOwnsOutput проверяет, что результат не меняет вход и владеет срезами.
func TestBuildMarketBoundariesOwnsOutput(t *testing.T) {
	dataset := tapeFixture(t, 0)
	before := FrozenDataset{Manifest: dataset.Manifest, Artifacts: make(map[string][]byte, len(dataset.Artifacts))}
	before.Manifest.Streams = append([]data.ManifestStream(nil), dataset.Manifest.Streams...)
	for key, raw := range dataset.Artifacts {
		before.Artifacts[key] = append([]byte(nil), raw...)
	}
	first, err := BuildMarketBoundaries(dataset, tapeSymbols, tapeStart, tapeStart.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	first[0].Marks[0].Symbol = "MUTATED"
	first[0].TradeOpens[0].Symbol = "MUTATED"
	second, err := BuildMarketBoundaries(dataset, tapeSymbols, tapeStart, tapeStart.Add(time.Hour))
	if err != nil || !reflect.DeepEqual(dataset, before) || second[0].Marks[0].Symbol != tapeSymbols[0] || second[0].TradeOpens[0].Symbol != tapeSymbols[0] {
		t.Fatalf("input/output alias: %v", err)
	}
}
