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
)

// markContentAmount разбирает точное значение поля mark OHLC для теста.
func markContentAmount(t *testing.T, text string) model.Decimal {
	t.Helper()
	d, err := model.ParseAmount(text)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// markContentFixture создаёт фактическую проекцию двух свечей с часовым пропуском.
func markContentFixture(t *testing.T) data.MarkPriceContentInput {
	t.Helper()
	input := data.MarkPriceContentInput{Environment: "testnet", DatasetID: "mark-A", Source: "bybit_v5", Category: "linear", Symbol: "BTCUSDT", IntervalCode: "60", Endpoint: "https://api-testnet.bybit.com/v5/market/mark-price-kline", Start: time.UnixMilli(0).UTC(), End: time.UnixMilli(10800000).UTC()}
	for i, raw := range [][4]string{{"100", "110", "90", "101"}, {"102", "112", "92", "103"}} {
		input.Rows = append(input.Rows, model.MarkPriceCandle{OpenTime: time.UnixMilli(int64(i) * 7200000).UTC(), Open: markContentAmount(t, raw[0]), High: markContentAmount(t, raw[1]), Low: markContentAmount(t, raw[2]), Close: markContentAmount(t, raw[3]), Confirmed: true})
	}
	return input
}

// TestEncodeMarkPriceContentGolden проверяет независимые канонические байты и digest.
func TestEncodeMarkPriceContentGolden(t *testing.T) {
	const golden = `{"format":"momentum/mark-price-content/v1","environment":"testnet","dataset_id":"mark-A","source":"bybit_v5","category":"linear","symbol":"BTCUSDT","interval_code":"60","endpoint":"https://api-testnet.bybit.com/v5/market/mark-price-kline","start_unix_ms":0,"end_unix_ms":10800000,"candles":[{"open_time_unix_ms":0,"open":"100","high":"110","low":"90","close":"101","confirmed":true},{"open_time_unix_ms":7200000,"open":"102","high":"112","low":"92","close":"103","confirmed":true}]}`
	const digest = "4288d7d241f411b9b70fb0c18846a364f571ba3254953816eabdd6dc8fd23121"
	in := markContentFixture(t)
	original := slices.Clone(in.Rows)
	got, hash, err := data.EncodeMarkPriceContent(in)
	if err != nil || string(got) != golden || len(got) != 482 || hash != digest || !reflect.DeepEqual(in.Rows, original) {
		t.Fatalf("golden %s %s %v", got, hash, err)
	}
	got[0] = 0
	slices.Reverse(in.Rows)
	zone := time.FixedZone("west", -25200)
	in.Start, in.End = in.Start.In(zone), in.End.In(zone)
	for i := range in.Rows {
		in.Rows[i].OpenTime = in.Rows[i].OpenTime.In(zone)
	}
	in.Rows[1].Open = markContentAmount(t, "100.0000")
	original = slices.Clone(in.Rows)
	again, hash, err := data.EncodeMarkPriceContent(in)
	if err != nil || string(again) != golden || hash != digest || !reflect.DeepEqual(in.Rows, original) {
		t.Fatalf("order/time/scale changed %s %s %v", again, hash, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(again, &decoded); err != nil {
		t.Fatal(err)
	}
	if (bytes.Contains(again, []byte("turnover")) || bytes.Contains(again, []byte("volume"))) || len(decoded["candles"].([]any)) != 2 {
		t.Fatal("invented volume/turnover or missing hour was filled")
	}
}

// TestEncodeMarkPriceContentIdentity проверяет связывание namespace, окна и всех цен.
func TestEncodeMarkPriceContentIdentity(t *testing.T) {
	base := markContentFixture(t)
	_, original, _ := data.EncodeMarkPriceContent(base)
	for name, mutate := range map[string]func(*data.MarkPriceContentInput){
		"mainnet": func(in *data.MarkPriceContentInput) {
			in.Environment = "mainnet"
			in.Endpoint = "https://api.bybit.com/v5/market/mark-price-kline"
		},
		"dataset": func(in *data.MarkPriceContentInput) { in.DatasetID = "opaque/α\t\"" },
		"symbol":  func(in *data.MarkPriceContentInput) { in.Symbol = "ETHUSDT" },
		"end":     func(in *data.MarkPriceContentInput) { in.End = in.End.Add(time.Hour) },
		"rows":    func(in *data.MarkPriceContentInput) { in.Rows = in.Rows[:1] },
		"time":    func(in *data.MarkPriceContentInput) { in.Rows[1].OpenTime = in.Rows[1].OpenTime.Add(-time.Hour) },
		"open":    func(in *data.MarkPriceContentInput) { in.Rows[0].Open = markContentAmount(t, "99") },
		"high":    func(in *data.MarkPriceContentInput) { in.Rows[0].High = markContentAmount(t, "111") },
		"low":     func(in *data.MarkPriceContentInput) { in.Rows[0].Low = markContentAmount(t, "89") },
		"close":   func(in *data.MarkPriceContentInput) { in.Rows[0].Close = markContentAmount(t, "102") },
	} {
		t.Run(name, func(t *testing.T) {
			in := markContentFixture(t)
			mutate(&in)
			_, hash, err := data.EncodeMarkPriceContent(in)
			if err != nil || hash == original {
				t.Fatalf("digest did not bind %s: %v", name, err)
			}
		})
	}
	var intervalHashes []string
	for _, interval := range []string{"D", "60"} {
		in := markContentFixture(t)
		in.IntervalCode = interval
		in.Rows = in.Rows[:1]
		in.End = time.UnixMilli(86400000).UTC()
		_, hash, err := data.EncodeMarkPriceContent(in)
		if err != nil {
			t.Fatal(err)
		}
		intervalHashes = append(intervalHashes, hash)
	}
	if intervalHashes[0] == intervalHashes[1] {
		t.Fatal("digest did not bind interval")
	}
	in := markContentFixture(t)
	in.Rows = in.Rows[1:]
	_, first, err := data.EncodeMarkPriceContent(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Start = time.UnixMilli(3600000)
	_, later, err := data.EncodeMarkPriceContent(in)
	if err != nil || first == later {
		t.Fatal("digest did not bind lower boundary")
	}
}

// TestEncodeMarkPriceContentEdges проверяет пустой ряд, точность и крайние сутки.
func TestEncodeMarkPriceContentEdges(t *testing.T) {
	in := markContentFixture(t)
	in.Rows = nil
	a, ha, err := data.EncodeMarkPriceContent(in)
	if err != nil || !bytes.Contains(a, []byte(`"candles":[]`)) {
		t.Fatal("invalid empty content")
	}
	in.Rows = []model.MarkPriceCandle{}
	b, hb, err := data.EncodeMarkPriceContent(in)
	if err != nil || !bytes.Equal(a, b) || ha != hb {
		t.Fatal("nil/empty changed content")
	}
	in = markContentFixture(t)
	in.Rows = in.Rows[:1]
	price := markContentAmount(t, "9007199254740993.00000000000000000001")
	in.Rows[0].Open, in.Rows[0].High, in.Rows[0].Low, in.Rows[0].Close = price, price, price, price
	encoded, _, err := data.EncodeMarkPriceContent(in)
	if err != nil || !bytes.Contains(encoded, []byte("9007199254740993.00000000000000000001")) {
		t.Fatalf("precision lost %v", err)
	}
	price = markContentAmount(t, "0."+strings.Repeat("0", 63)+"1")
	in.Rows[0].Open, in.Rows[0].High, in.Rows[0].Low, in.Rows[0].Close = price, price, price, price
	if _, _, err := data.EncodeMarkPriceContent(in); err != nil {
		t.Fatal(err)
	}
	in.IntervalCode = "D"
	in.Start = time.Date(294276, 12, 29, 0, 0, 0, 0, time.UTC)
	in.End = in.Start.Add(24 * time.Hour)
	in.Rows[0].OpenTime = in.Start
	if _, _, err := data.EncodeMarkPriceContent(in); err != nil {
		t.Fatal(err)
	}
	in.DatasetID = strings.Repeat("я", 128)
	if _, _, err := data.EncodeMarkPriceContent(in); err != nil {
		t.Fatal(err)
	}
}

// TestEncodeMarkPriceContentInvalid проверяет безопасные отказы без частичного payload.
func TestEncodeMarkPriceContentInvalid(t *testing.T) {
	for name, mutate := range map[string]func(*data.MarkPriceContentInput){
		"environment":        func(in *data.MarkPriceContentInput) { in.Environment = "paper" },
		"source":             func(in *data.MarkPriceContentInput) { in.Source = "other" },
		"category":           func(in *data.MarkPriceContentInput) { in.Category = "spot" },
		"symbol":             func(in *data.MarkPriceContentInput) { in.Symbol = "BTCusdt" },
		"symbol-space":       func(in *data.MarkPriceContentInput) { in.Symbol = " BTCUSDT" },
		"interval":           func(in *data.MarkPriceContentInput) { in.IntervalCode = "1" },
		"empty-id":           func(in *data.MarkPriceContentInput) { in.DatasetID = "" },
		"space-id":           func(in *data.MarkPriceContentInput) { in.DatasetID = "id " },
		"nul-id":             func(in *data.MarkPriceContentInput) { in.DatasetID = "x\x00y" },
		"utf8-id":            func(in *data.MarkPriceContentInput) { in.DatasetID = string([]byte{0xff}) },
		"long-id":            func(in *data.MarkPriceContentInput) { in.DatasetID = strings.Repeat("я", 129) },
		"wrong-env-endpoint": func(in *data.MarkPriceContentInput) { in.Endpoint = "https://api.bybit.com/v5/market/mark-price-kline" },
		"trade-endpoint": func(in *data.MarkPriceContentInput) {
			in.Endpoint = "https://api-testnet.bybit.com/v5/market/kline"
		},
		"endpoint-query":    func(in *data.MarkPriceContentInput) { in.Endpoint += "?x=1" },
		"endpoint-fragment": func(in *data.MarkPriceContentInput) { in.Endpoint += "#x" },
		"endpoint-foreign":  func(in *data.MarkPriceContentInput) { in.Endpoint = "https://example.org/v5/market/mark-price-kline" },
		"endpoint-auth": func(in *data.MarkPriceContentInput) {
			in.Endpoint = "https://user@api-testnet.bybit.com/v5/market/mark-price-kline"
		},
		"endpoint-case":    func(in *data.MarkPriceContentInput) { in.Endpoint = strings.ToUpper(in.Endpoint) },
		"start-zero":       func(in *data.MarkPriceContentInput) { in.Start = time.Time{} },
		"end-grid":         func(in *data.MarkPriceContentInput) { in.End = in.End.Add(time.Millisecond) },
		"start-precision":  func(in *data.MarkPriceContentInput) { in.Start = in.Start.Add(time.Nanosecond) },
		"ohlc-open-low":    func(in *data.MarkPriceContentInput) { in.Rows[1].Open = markContentAmount(t, "91") },
		"ohlc-close-high":  func(in *data.MarkPriceContentInput) { in.Rows[1].Close = markContentAmount(t, "113") },
		"zero-bounds":      func(in *data.MarkPriceContentInput) { in.End = in.Start },
		"reverse":          func(in *data.MarkPriceContentInput) { in.Start = in.End.Add(time.Hour) },
		"before-epoch":     func(in *data.MarkPriceContentInput) { in.Start = time.UnixMilli(-3600000) },
		"unsupported-year": func(in *data.MarkPriceContentInput) { in.End = time.Date(294277, 1, 1, 0, 0, 0, 0, time.UTC) },
		"start-grid":       func(in *data.MarkPriceContentInput) { in.Start = in.Start.Add(time.Millisecond) },
		"end-precision":    func(in *data.MarkPriceContentInput) { in.End = in.End.Add(time.Nanosecond) },
		"row-before":       func(in *data.MarkPriceContentInput) { in.Start = time.UnixMilli(3600000) },
		"row-end":          func(in *data.MarkPriceContentInput) { in.Rows[1].OpenTime = in.End },
		"row-precision":    func(in *data.MarkPriceContentInput) { in.Rows[0].OpenTime = in.Start.Add(time.Nanosecond) },
		"row-grid":         func(in *data.MarkPriceContentInput) { in.Rows[0].OpenTime = in.Start.Add(time.Millisecond) },
		"day-grid": func(in *data.MarkPriceContentInput) {
			in.IntervalCode, in.End = "D", time.UnixMilli(86400000)
		},
		"unconfirmed": func(in *data.MarkPriceContentInput) { in.Rows[1].Confirmed = false },
		"duplicate":   func(in *data.MarkPriceContentInput) { in.Rows = append(in.Rows, in.Rows[0]) },
		"changed-duplicate": func(in *data.MarkPriceContentInput) {
			row := in.Rows[0]
			row.Close = markContentAmount(t, "102")
			in.Rows = append(in.Rows, row)
		},
		"ohlc-open":  func(in *data.MarkPriceContentInput) { in.Rows[1].Open = markContentAmount(t, "113") },
		"ohlc-close": func(in *data.MarkPriceContentInput) { in.Rows[1].Close = markContentAmount(t, "91") },
	} {
		t.Run(name, func(t *testing.T) {
			in := markContentFixture(t)
			mutate(&in)
			original := slices.Clone(in.Rows)
			payload, hash, err := data.EncodeMarkPriceContent(in)
			if payload != nil || hash != "" || !errors.Is(err, data.ErrMarkPriceContent) || !reflect.DeepEqual(in.Rows, original) {
				t.Fatalf("partial or mutated %s %s %v", payload, hash, err)
			}
		})
	}
	one := markContentAmount(t, "1")
	third, err := one.Quo(markContentAmount(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	overscale, err := markContentAmount(t, "0."+strings.Repeat("0", 63)+"1").Quo(markContentAmount(t, "10"))
	if err != nil {
		t.Fatal(err)
	}
	for field := 0; field < 4; field++ {
		for _, bad := range []model.Decimal{{}, markContentAmount(t, "-1"), third, overscale} {
			in := markContentFixture(t)
			fields := []*model.Decimal{&in.Rows[1].Open, &in.Rows[1].High, &in.Rows[1].Low, &in.Rows[1].Close}
			*fields[field] = bad
			original := slices.Clone(in.Rows)
			payload, hash, err := data.EncodeMarkPriceContent(in)
			if payload != nil || hash != "" || err != data.ErrMarkPriceContent || !reflect.DeepEqual(in.Rows, original) {
				t.Fatalf("field %d accepted invalid decimal", field)
			}
		}
		{
			in := markContentFixture(t)
			fields := []*model.Decimal{&in.Rows[1].Open, &in.Rows[1].High, &in.Rows[1].Low, &in.Rows[1].Close}
			*fields[field] = model.Zero()
			original := slices.Clone(in.Rows)
			payload, hash, err := data.EncodeMarkPriceContent(in)
			if payload != nil || hash != "" || err != data.ErrMarkPriceContent || !reflect.DeepEqual(in.Rows, original) {
				t.Fatalf("field %d accepted zero price", field)
			}
		}
	}
}

// TestEncodeMarkPriceContentLargeAndEscaped проверяет отсутствие page cap и штатный JSON escaping.
func TestEncodeMarkPriceContentLargeAndEscaped(t *testing.T) {
	in := markContentFixture(t)
	in.DatasetID = "<>&\u2028\u2029\t\""
	in.End = in.Start.Add(1001 * time.Hour)
	row := in.Rows[0]
	in.Rows = make([]model.MarkPriceCandle, 1001)
	for i := range in.Rows {
		in.Rows[i] = row
		in.Rows[i].OpenTime = in.Start.Add(time.Duration(i) * time.Hour)
	}
	slices.Reverse(in.Rows)
	original := slices.Clone(in.Rows)
	payload, _, err := data.EncodeMarkPriceContent(in)
	wantID, _ := json.Marshal(in.DatasetID)
	if err != nil || !bytes.Contains(payload, wantID) || bytes.HasSuffix(payload, []byte("\n")) || !reflect.DeepEqual(in.Rows, original) {
		t.Fatalf("page cap, escaping or input mutation: %v", err)
	}
	var decoded struct {
		Candles []struct {
			OpenTime int64 `json:"open_time_unix_ms"`
		} `json:"candles"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil || len(decoded.Candles) != 1001 || decoded.Candles[1000].OpenTime != 3600000000 {
		t.Fatalf("invalid sorted large content: %v", err)
	}
}
