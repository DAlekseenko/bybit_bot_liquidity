package data_test

import (
	"bytes"
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

// tradeContentAmount разбирает точное значение поля OHLCV для теста.
func tradeContentAmount(t *testing.T, text string) model.Decimal {
	t.Helper()
	d, err := model.ParseAmount(text)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// tradeContentFixture создаёт фактическую проекцию двух свечей с часовым пропуском.
func tradeContentFixture(t *testing.T) data.TradeCandleContentInput {
	t.Helper()
	input := data.TradeCandleContentInput{Environment: "testnet", DatasetID: "trade-A", Source: "bybit_v5", Category: "linear", Symbol: "BTCUSDT", IntervalCode: "60", Endpoint: "https://api-testnet.bybit.com/v5/market/kline", Start: time.UnixMilli(0).UTC(), End: time.UnixMilli(10800000).UTC()}
	for i, raw := range [][5]string{{"100", "110", "90", "101", "2.5000"}, {"102", "112", "92", "103", "-0.000"}} {
		stored := storage.StoredCandle{OpenTime: time.UnixMilli(int64(i) * 7200000).UTC(), Open: tradeContentAmount(t, raw[0]), High: tradeContentAmount(t, raw[1]), Low: tradeContentAmount(t, raw[2]), Close: tradeContentAmount(t, raw[3]), Volume: tradeContentAmount(t, raw[4]), Confirmed: true}
		input.Rows = append(input.Rows, data.TradeCandleContentRow{OpenTime: stored.OpenTime, Open: stored.Open, High: stored.High, Low: stored.Low, Close: stored.Close, Volume: stored.Volume, Confirmed: stored.Confirmed})
	}
	return input
}

// TestEncodeTradeCandleContentGolden проверяет независимые канонические байты и digest.
func TestEncodeTradeCandleContentGolden(t *testing.T) {
	const golden = `{"format":"momentum/trade-candle-content/v1","environment":"testnet","dataset_id":"trade-A","source":"bybit_v5","category":"linear","symbol":"BTCUSDT","interval_code":"60","endpoint":"https://api-testnet.bybit.com/v5/market/kline","start_unix_ms":0,"end_unix_ms":10800000,"candles":[{"open_time_unix_ms":0,"open":"100","high":"110","low":"90","close":"101","volume":"2.5","confirmed":true},{"open_time_unix_ms":7200000,"open":"102","high":"112","low":"92","close":"103","volume":"0","confirmed":true}]}`
	const digest = "0ac58d8bafec8caf8cee4086c8a72a707bf8dd324a077c3b904ae914f339df8b"
	in := tradeContentFixture(t)
	original := slices.Clone(in.Rows)
	got, hash, err := data.EncodeTradeCandleContent(in)
	if err != nil || string(got) != golden || len(got) != 502 || hash != digest || !reflect.DeepEqual(in.Rows, original) {
		t.Fatalf("golden %s %s %v", got, hash, err)
	}
	got[0] = 0
	slices.Reverse(in.Rows)
	zone := time.FixedZone("west", -25200)
	in.Start, in.End = in.Start.In(zone), in.End.In(zone)
	for i := range in.Rows {
		in.Rows[i].OpenTime = in.Rows[i].OpenTime.In(zone)
	}
	in.Rows[1].Open = tradeContentAmount(t, "100.0000")
	original = slices.Clone(in.Rows)
	again, hash, err := data.EncodeTradeCandleContent(in)
	if err != nil || string(again) != golden || hash != digest || !reflect.DeepEqual(in.Rows, original) {
		t.Fatalf("order/time/scale changed %s %s %v", again, hash, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(again, &decoded); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(again, []byte("turnover")) || len(decoded["candles"].([]any)) != 2 {
		t.Fatal("invented turnover or missing hour was filled")
	}
}

// TestEncodeTradeCandleContentIdentity проверяет связывание namespace, окна и всех цен.
func TestEncodeTradeCandleContentIdentity(t *testing.T) {
	base := tradeContentFixture(t)
	_, original, _ := data.EncodeTradeCandleContent(base)
	for name, mutate := range map[string]func(*data.TradeCandleContentInput){
		"mainnet": func(in *data.TradeCandleContentInput) {
			in.Environment = "mainnet"
			in.Endpoint = "https://api.bybit.com/v5/market/kline"
		},
		"dataset": func(in *data.TradeCandleContentInput) { in.DatasetID = "opaque/α\t\"" },
		"symbol":  func(in *data.TradeCandleContentInput) { in.Symbol = "ETHUSDT" },
		"end":     func(in *data.TradeCandleContentInput) { in.End = in.End.Add(time.Hour) },
		"rows":    func(in *data.TradeCandleContentInput) { in.Rows = in.Rows[:1] },
		"time":    func(in *data.TradeCandleContentInput) { in.Rows[1].OpenTime = in.Rows[1].OpenTime.Add(-time.Hour) },
		"open":    func(in *data.TradeCandleContentInput) { in.Rows[0].Open = tradeContentAmount(t, "99") },
		"high":    func(in *data.TradeCandleContentInput) { in.Rows[0].High = tradeContentAmount(t, "111") },
		"low":     func(in *data.TradeCandleContentInput) { in.Rows[0].Low = tradeContentAmount(t, "89") },
		"close":   func(in *data.TradeCandleContentInput) { in.Rows[0].Close = tradeContentAmount(t, "102") },
		"volume":  func(in *data.TradeCandleContentInput) { in.Rows[0].Volume = tradeContentAmount(t, "0") },
	} {
		t.Run(name, func(t *testing.T) {
			in := tradeContentFixture(t)
			mutate(&in)
			_, hash, err := data.EncodeTradeCandleContent(in)
			if err != nil || hash == original {
				t.Fatalf("digest did not bind %s: %v", name, err)
			}
		})
	}
	var intervalHashes []string
	for _, interval := range []string{"D", "60"} {
		in := tradeContentFixture(t)
		in.IntervalCode = interval
		in.Rows = in.Rows[:1]
		in.End = time.UnixMilli(86400000).UTC()
		_, hash, err := data.EncodeTradeCandleContent(in)
		if err != nil {
			t.Fatal(err)
		}
		intervalHashes = append(intervalHashes, hash)
	}
	if intervalHashes[0] == intervalHashes[1] {
		t.Fatal("digest did not bind interval")
	}
	in := tradeContentFixture(t)
	in.Rows = in.Rows[1:]
	_, first, err := data.EncodeTradeCandleContent(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Start = time.UnixMilli(3600000)
	_, later, err := data.EncodeTradeCandleContent(in)
	if err != nil || first == later {
		t.Fatal("digest did not bind lower boundary")
	}
}

// TestEncodeTradeCandleContentEdges проверяет пустой ряд, точность и крайние сутки.
func TestEncodeTradeCandleContentEdges(t *testing.T) {
	in := tradeContentFixture(t)
	in.Rows = nil
	a, ha, err := data.EncodeTradeCandleContent(in)
	if err != nil || !bytes.Contains(a, []byte(`"candles":[]`)) {
		t.Fatal("invalid empty content")
	}
	in.Rows = []data.TradeCandleContentRow{}
	b, hb, err := data.EncodeTradeCandleContent(in)
	if err != nil || !bytes.Equal(a, b) || ha != hb {
		t.Fatal("nil/empty changed content")
	}
	in = tradeContentFixture(t)
	in.Rows = in.Rows[:1]
	price := tradeContentAmount(t, "9007199254740993.00000000000000000001")
	in.Rows[0].Open, in.Rows[0].High, in.Rows[0].Low, in.Rows[0].Close = price, price, price, price
	in.Rows[0].Volume = tradeContentAmount(t, "0."+strings.Repeat("0", 63)+"1")
	encoded, _, err := data.EncodeTradeCandleContent(in)
	if err != nil || !bytes.Contains(encoded, []byte("9007199254740993.00000000000000000001")) {
		t.Fatalf("precision lost %v", err)
	}
	in.IntervalCode = "D"
	in.Start = time.Date(294276, 12, 29, 0, 0, 0, 0, time.UTC)
	in.End = in.Start.Add(24 * time.Hour)
	in.Rows[0].OpenTime = in.Start
	if _, _, err := data.EncodeTradeCandleContent(in); err != nil {
		t.Fatal(err)
	}
	in.DatasetID = strings.Repeat("я", 128)
	if _, _, err := data.EncodeTradeCandleContent(in); err != nil {
		t.Fatal(err)
	}
}

// TestEncodeTradeCandleContentInvalid проверяет безопасные отказы без частичного payload.
func TestEncodeTradeCandleContentInvalid(t *testing.T) {
	for name, mutate := range map[string]func(*data.TradeCandleContentInput){
		"environment":        func(in *data.TradeCandleContentInput) { in.Environment = "paper" },
		"source":             func(in *data.TradeCandleContentInput) { in.Source = "other" },
		"category":           func(in *data.TradeCandleContentInput) { in.Category = "spot" },
		"symbol":             func(in *data.TradeCandleContentInput) { in.Symbol = "BTCusdt" },
		"symbol-space":       func(in *data.TradeCandleContentInput) { in.Symbol = " BTCUSDT" },
		"interval":           func(in *data.TradeCandleContentInput) { in.IntervalCode = "1" },
		"empty-id":           func(in *data.TradeCandleContentInput) { in.DatasetID = "" },
		"space-id":           func(in *data.TradeCandleContentInput) { in.DatasetID = "id " },
		"nul-id":             func(in *data.TradeCandleContentInput) { in.DatasetID = "x\x00y" },
		"utf8-id":            func(in *data.TradeCandleContentInput) { in.DatasetID = string([]byte{0xff}) },
		"long-id":            func(in *data.TradeCandleContentInput) { in.DatasetID = strings.Repeat("я", 129) },
		"wrong-env-endpoint": func(in *data.TradeCandleContentInput) { in.Endpoint = "https://api.bybit.com/v5/market/kline" },
		"mark-endpoint": func(in *data.TradeCandleContentInput) {
			in.Endpoint = "https://api-testnet.bybit.com/v5/market/mark-price-kline"
		},
		"endpoint-query":   func(in *data.TradeCandleContentInput) { in.Endpoint += "?x=1" },
		"zero-bounds":      func(in *data.TradeCandleContentInput) { in.End = in.Start },
		"reverse":          func(in *data.TradeCandleContentInput) { in.Start = in.End.Add(time.Hour) },
		"before-epoch":     func(in *data.TradeCandleContentInput) { in.Start = time.UnixMilli(-3600000) },
		"unsupported-year": func(in *data.TradeCandleContentInput) { in.End = time.Date(294277, 1, 1, 0, 0, 0, 0, time.UTC) },
		"start-grid":       func(in *data.TradeCandleContentInput) { in.Start = in.Start.Add(time.Millisecond) },
		"end-precision":    func(in *data.TradeCandleContentInput) { in.End = in.End.Add(time.Nanosecond) },
		"row-before":       func(in *data.TradeCandleContentInput) { in.Start = time.UnixMilli(3600000) },
		"row-end":          func(in *data.TradeCandleContentInput) { in.Rows[1].OpenTime = in.End },
		"row-precision":    func(in *data.TradeCandleContentInput) { in.Rows[0].OpenTime = in.Start.Add(time.Nanosecond) },
		"row-grid":         func(in *data.TradeCandleContentInput) { in.Rows[0].OpenTime = in.Start.Add(time.Millisecond) },
		"day-grid": func(in *data.TradeCandleContentInput) {
			in.IntervalCode, in.End = "D", time.UnixMilli(86400000)
		},
		"unconfirmed": func(in *data.TradeCandleContentInput) { in.Rows[1].Confirmed = false },
		"duplicate":   func(in *data.TradeCandleContentInput) { in.Rows = append(in.Rows, in.Rows[0]) },
		"changed-duplicate": func(in *data.TradeCandleContentInput) {
			row := in.Rows[0]
			row.Volume = tradeContentAmount(t, "9")
			in.Rows = append(in.Rows, row)
		},
		"ohlc-open":  func(in *data.TradeCandleContentInput) { in.Rows[1].Open = tradeContentAmount(t, "113") },
		"ohlc-close": func(in *data.TradeCandleContentInput) { in.Rows[1].Close = tradeContentAmount(t, "91") },
	} {
		t.Run(name, func(t *testing.T) {
			in := tradeContentFixture(t)
			mutate(&in)
			original := slices.Clone(in.Rows)
			payload, hash, err := data.EncodeTradeCandleContent(in)
			if payload != nil || hash != "" || !errors.Is(err, data.ErrTradeCandleContent) || !reflect.DeepEqual(in.Rows, original) {
				t.Fatalf("partial or mutated %s %s %v", payload, hash, err)
			}
		})
	}
	one := tradeContentAmount(t, "1")
	third, err := one.Quo(tradeContentAmount(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	overscale, err := tradeContentAmount(t, "0."+strings.Repeat("0", 63)+"1").Quo(tradeContentAmount(t, "10"))
	if err != nil {
		t.Fatal(err)
	}
	for field := 0; field < 5; field++ {
		for _, bad := range []model.Decimal{{}, tradeContentAmount(t, "-1"), third, overscale} {
			in := tradeContentFixture(t)
			fields := []*model.Decimal{&in.Rows[1].Open, &in.Rows[1].High, &in.Rows[1].Low, &in.Rows[1].Close, &in.Rows[1].Volume}
			*fields[field] = bad
			payload, hash, err := data.EncodeTradeCandleContent(in)
			if payload != nil || hash != "" || !errors.Is(err, data.ErrTradeCandleContent) {
				t.Fatalf("field %d accepted invalid decimal", field)
			}
		}
		if field < 4 {
			in := tradeContentFixture(t)
			fields := []*model.Decimal{&in.Rows[1].Open, &in.Rows[1].High, &in.Rows[1].Low, &in.Rows[1].Close}
			*fields[field] = model.Zero()
			payload, hash, err := data.EncodeTradeCandleContent(in)
			if payload != nil || hash != "" || !errors.Is(err, data.ErrTradeCandleContent) {
				t.Fatalf("field %d accepted zero price", field)
			}
		}
	}
}
