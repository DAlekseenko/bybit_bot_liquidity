package data

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

	"bybit_bot_liquidity/internal/momentum/model"
)

const decodeFundingGolden = `{"format":"momentum/funding-content/v1","environment":"testnet","dataset_id":"funding-A","source":"bybit_v5","category":"linear","symbol":"BTCUSDT","endpoint":"https://api-testnet.bybit.com/v5/market/funding/history","start_unix_ms":0,"end_unix_ms":14400000,"events":[{"funding_time_unix_ms":0,"rate":"-0.000031"},{"funding_time_unix_ms":3600000,"rate":"0"},{"funding_time_unix_ms":10800000,"rate":"0.000004"}]}`
const decodeTradeGolden = `{"format":"momentum/trade-candle-content/v1","environment":"testnet","dataset_id":"trade-A","source":"bybit_v5","category":"linear","symbol":"BTCUSDT","interval_code":"60","endpoint":"https://api-testnet.bybit.com/v5/market/kline","start_unix_ms":0,"end_unix_ms":10800000,"candles":[{"open_time_unix_ms":0,"open":"100","high":"110","low":"90","close":"101","volume":"2.5","confirmed":true},{"open_time_unix_ms":7200000,"open":"102","high":"112","low":"92","close":"103","volume":"0","confirmed":true}]}`
const decodeMarkGolden = `{"format":"momentum/mark-price-content/v1","environment":"testnet","dataset_id":"mark-A","source":"bybit_v5","category":"linear","symbol":"BTCUSDT","interval_code":"60","endpoint":"https://api-testnet.bybit.com/v5/market/mark-price-kline","start_unix_ms":0,"end_unix_ms":10800000,"candles":[{"open_time_unix_ms":0,"open":"100","high":"110","low":"90","close":"101","confirmed":true},{"open_time_unix_ms":7200000,"open":"102","high":"112","low":"92","close":"103","confirmed":true}]}`

// decodeRoundTrip проверяет zero-on-error и отсутствие ссылок decoded значения на raw bytes.
func decodeRoundTrip[T any](decode func([]byte) (T, error), encode func(T) ([]byte, string, error)) func([]byte) ([]byte, error) {
	return func(raw []byte) ([]byte, error) {
		input, err := decode(raw)
		if err != nil {
			var zero T
			if !reflect.DeepEqual(input, zero) {
				return nil, errors.New("nonzero rejected content")
			}
			return nil, err
		}
		encoded, _, err := encode(input)
		if err == nil && len(raw) != 0 {
			saved := bytes.Clone(raw)
			for i := range raw {
				raw[i] = 'x'
			}
			again, _, secondErr := encode(input)
			copy(raw, saved)
			if secondErr != nil || !bytes.Equal(encoded, again) {
				return nil, errors.New("decoded content aliases raw bytes")
			}
		}
		return encoded, err
	}
}

// decodeFixture связывает три независимых формата без нового production union type.
type decodeFixture struct {
	name, golden, digest, list, stamp string
	failure                           error
	roundTrip                         func([]byte) ([]byte, error)
}

// decodeFixtures задаёт независимые golden и существующие typed content errors.
func decodeFixtures() []decodeFixture {
	return []decodeFixture{
		{"funding", decodeFundingGolden, "0d48873010535ed0ce14ccb15d10b609f0492e58e1b9eadc09a0a39343d16556", "events", "funding_time_unix_ms", ErrFundingContent, decodeRoundTrip(DecodeFundingContent, EncodeFundingContent)},
		{"trade", decodeTradeGolden, "0ac58d8bafec8caf8cee4086c8a72a707bf8dd324a077c3b904ae914f339df8b", "candles", "open_time_unix_ms", ErrTradeCandleContent, decodeRoundTrip(DecodeTradeCandleContent, EncodeTradeCandleContent)},
		{"mark", decodeMarkGolden, "4288d7d241f411b9b70fb0c18846a364f571ba3254953816eabdd6dc8fd23121", "candles", "open_time_unix_ms", ErrMarkPriceContent, decodeRoundTrip(DecodeMarkPriceContent, EncodeMarkPriceContent)},
	}
}

// mutateContentRows изменяет только row array, сохраняя canonical header и порядок row fields.
func mutateContentRows(t *testing.T, raw []byte, mutate func([]json.RawMessage) []json.RawMessage) []byte {
	t.Helper()
	start := bytes.IndexByte(raw, '[')
	var rows []json.RawMessage
	if err := json.Unmarshal(raw[start:len(raw)-1], &rows); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(mutate(rows))
	if err != nil {
		t.Fatal(err)
	}
	return append(append(bytes.Clone(raw[:start]), encoded...), '}')
}

// TestDecodeContentGolden проверяет exact bytes/hash, UTC, пустые ряды и независимость памяти.
func TestDecodeContentGolden(t *testing.T) {
	for _, fixture := range decodeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			raw := []byte(fixture.golden)
			digest := sha256.Sum256(raw)
			got, err := fixture.roundTrip(raw)
			if err != nil || !bytes.Equal(got, raw) || string(raw) != fixture.golden || hex.EncodeToString(digest[:]) != fixture.digest {
				t.Fatalf("golden/roundtrip: %v", err)
			}
			empty := mutateContentRows(t, raw, func([]json.RawMessage) []json.RawMessage { return []json.RawMessage{} })
			if got, err := fixture.roundTrip(empty); err != nil || !bytes.Equal(got, empty) {
				t.Fatal("canonical empty", err)
			}
		})
	}
	funding, _ := DecodeFundingContent([]byte(decodeFundingGolden))
	trade, _ := DecodeTradeCandleContent([]byte(decodeTradeGolden))
	mark, _ := DecodeMarkPriceContent([]byte(decodeMarkGolden))
	if funding.Events[0].FundingTime.Location() != time.UTC || trade.Rows[0].OpenTime.Location() != time.UTC || mark.Rows[0].OpenTime.Location() != time.UTC {
		t.Fatal("decoded times are not UTC")
	}
}

// TestDecodeContentSourceAndTimezone проверяет оба endpoint и нормализацию исходных offset times.
func TestDecodeContentSourceAndTimezone(t *testing.T) {
	for _, fixture := range decodeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			raw := []byte(fixture.golden)
			var encoded []byte
			switch fixture.name {
			case "funding":
				input, _ := DecodeFundingContent(raw)
				input.Environment, input.Endpoint = "mainnet", strings.Replace(input.Endpoint, "api-testnet.bybit.com", "api.bybit.com", 1)
				input.Start, input.End = input.Start.In(time.FixedZone("offset", 3*3600)), input.End.In(time.FixedZone("offset", 3*3600))
				input.Events[0].FundingTime = input.Events[0].FundingTime.In(time.FixedZone("offset", 3*3600))
				encoded, _, _ = EncodeFundingContent(input)
			case "trade":
				input, _ := DecodeTradeCandleContent(raw)
				input.Environment, input.Endpoint = "mainnet", strings.Replace(input.Endpoint, "api-testnet.bybit.com", "api.bybit.com", 1)
				input.Start, input.End = input.Start.In(time.FixedZone("offset", -5*3600)), input.End.In(time.FixedZone("offset", -5*3600))
				input.Rows[0].OpenTime = input.Rows[0].OpenTime.In(time.FixedZone("offset", -5*3600))
				encoded, _, _ = EncodeTradeCandleContent(input)
			case "mark":
				input, _ := DecodeMarkPriceContent(raw)
				input.Environment, input.Endpoint = "mainnet", strings.Replace(input.Endpoint, "api-testnet.bybit.com", "api.bybit.com", 1)
				input.Start, input.End = input.Start.In(time.FixedZone("offset", 9*3600)), input.End.In(time.FixedZone("offset", 9*3600))
				input.Rows[0].OpenTime = input.Rows[0].OpenTime.In(time.FixedZone("offset", 9*3600))
				encoded, _, _ = EncodeMarkPriceContent(input)
			}
			got, err := fixture.roundTrip(encoded)
			if err != nil || !bytes.Equal(got, encoded) || !bytes.Contains(got, []byte(`"environment":"mainnet"`)) {
				t.Fatalf("mainnet/offset roundtrip: %v", err)
			}
		})
	}
}

// TestDecodeContentCorruption отклоняет JSON ambiguity, повреждение identity/window и неканонические rows.
func TestDecodeContentCorruption(t *testing.T) {
	for _, fixture := range decodeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			raw := []byte(fixture.golden)
			for cut := range len(raw) {
				got, err := fixture.roundTrip(raw[:cut])
				if !errors.Is(err, fixture.failure) || got != nil {
					t.Fatalf("prefix %d accepted: %v", cut, err)
				}
			}
			for _, tc := range []struct{ from, to string }{
				{"", ""}, {"null", "null"}, {"[]", "[]"}, {"{}", "{}"},
				{"v1", "v2"}, {`"environment":"testnet"`, `"environment":"live"`},
				{`"dataset_id":"`, `"dataset_id":" `}, {`"source":"bybit_v5"`, `"source":"secret-marker"`},
				{`"category":"linear"`, `"category":"spot"`}, {`"symbol":"BTCUSDT"`, `"symbol":"invalid"`},
				{`"symbol":"BTCUSDT"`, `"symbol":"BTCUSDT","symbol":"BTCUSDT"`},
				{`"symbol":`, `"Symbol":`}, {"BTCUSDT", `BTC\u0055SDT`},
				{"/v5/market/", "/v5/private/"}, {`"start_unix_ms":0`, `"start_unix_ms":-1`}, {`"start_unix_ms":0,`, ""},
				{`"start_unix_ms":0`, `"start_unix_ms":-0`}, {`"start_unix_ms":0`, `"start_unix_ms":0.0`},
				{`"start_unix_ms":0`, `"start_unix_ms":9223372036854775808`},
				{`"start_unix_ms":0`, `"start_unix_ms":9223372036854775807`},
				{`"` + fixture.list + `":[`, `"unknown":1,"` + fixture.list + `":[`},
			} {
				candidate := []byte(tc.to)
				if tc.from != tc.to && tc.from != "" {
					candidate = bytes.Replace(raw, []byte(tc.from), []byte(tc.to), 1)
				}
				checkContentRejected(t, fixture, candidate)
			}
			invalidUTF8 := bytes.Clone(raw)
			invalidUTF8[bytes.Index(raw, []byte(`"dataset_id":"`))+len(`"dataset_id":"`)] = 0xff
			checkContentRejected(t, fixture, invalidUTF8)
			for _, candidate := range [][]byte{append(bytes.Clone(raw), '\n'), append([]byte(" "), raw...), append(bytes.Clone(raw), raw...)} {
				checkContentRejected(t, fixture, candidate)
			}
			rowField, rowValue, secondStamp := "open", "102", "7200000"
			if fixture.name == "funding" {
				rowField, rowValue, secondStamp = "rate", "0", "3600000"
			}
			candidates := [][]byte{
				bytes.Replace(raw, []byte(`,"dataset_id":`), []byte(`, "dataset_id":`), 1),
				bytes.Replace(raw, []byte(`"format":"momentum/`), []byte(`"environment":"testnet","format":"momentum/`), 1),
				mutateContentRows(t, raw, func(rows []json.RawMessage) []json.RawMessage {
					field := `"` + rowField + `":"` + rowValue + `"`
					if fixture.name == "funding" {
						field = "," + field
					} else {
						field += ","
					}
					rows[1] = bytes.Replace(rows[1], []byte(field), nil, 1)
					return rows
				}),
				mutateContentRows(t, raw, func(rows []json.RawMessage) []json.RawMessage {
					field := `"` + fixture.stamp + `":` + secondStamp
					rows[1] = bytes.Replace(rows[1], []byte(field), []byte(`"`+fixture.stamp+`":14400000`), 1)
					return rows
				}),
			}
			for _, candidate := range candidates {
				checkContentRejected(t, fixture, candidate)
			}
			for _, mutate := range []func([]json.RawMessage) []json.RawMessage{
				func([]json.RawMessage) []json.RawMessage { return nil },
				func([]json.RawMessage) []json.RawMessage { return []json.RawMessage{json.RawMessage("null")} },
				func(rows []json.RawMessage) []json.RawMessage {
					slices.Reverse(rows)
					return rows
				},
				func(rows []json.RawMessage) []json.RawMessage { return append(rows, rows[0]) },
				func(rows []json.RawMessage) []json.RawMessage {
					rows[0] = bytes.Replace(rows[0], []byte(`"`+fixture.stamp+`":0`), []byte(`"`+fixture.stamp+`":-1`), 1)
					return rows
				},
			} {
				checkContentRejected(t, fixture, mutateContentRows(t, raw, mutate))
			}
			fields := []string{"open", "high", "low", "close", "volume"}
			switch fixture.name {
			case "funding":
				fields = []string{"rate"}
			case "mark":
				fields = fields[:4]
			}
			for _, field := range fields {
				for _, bad := range []string{"", "NaN", "1/3", "0." + strings.Repeat("0", 64) + "1", "01", "100.00"} {
					candidate := mutateContentRows(t, raw, func(rows []json.RawMessage) []json.RawMessage {
						var values map[string]json.RawMessage
						_ = json.Unmarshal(rows[1], &values)
						old := `"` + field + `":` + string(values[field])
						replacement, _ := json.Marshal(bad)
						rows[1] = bytes.Replace(rows[1], []byte(old), []byte(`"`+field+`":`+string(replacement)), 1)
						return rows
					})
					checkContentRejected(t, fixture, candidate)
				}
			}
			if fixture.name != "funding" {
				for _, pair := range [][2]string{{`"confirmed":true`, `"confirmed":false`}, {`"high":"110"`, `"high":"99"`}, {`"low":"90"`, `"low":"102"`}} {
					checkContentRejected(t, fixture, bytes.Replace(raw, []byte(pair[0]), []byte(pair[1]), 1))
				}
			}
		})
	}
}

// checkContentRejected проверяет typed corruption, zero result, safe diagnostics и отсутствие мутаций raw.
func checkContentRejected(t *testing.T, fixture decodeFixture, candidate []byte) {
	t.Helper()
	before := bytes.Clone(candidate)
	got, err := fixture.roundTrip(candidate)
	if !errors.Is(err, fixture.failure) || got != nil || !bytes.Equal(candidate, before) || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("corruption accepted or leaked: %v", err)
	}
}

// TestDecodeContentEdges проверяет full-size content, exact Decimal/upper times и opaque identity.
func TestDecodeContentEdges(t *testing.T) {
	funding, _ := DecodeFundingContent([]byte(decodeFundingGolden))
	trade, _ := DecodeTradeCandleContent([]byte(decodeTradeGolden))
	mark, _ := DecodeMarkPriceContent([]byte(decodeMarkGolden))
	start := time.Date(294276, 1, 1, 0, 0, 0, 0, time.UTC)
	funding.Start, trade.Start, mark.Start = start, start, start
	funding.End, trade.End, mark.End = start.Add(24*time.Hour), start.Add(24*time.Hour), start.Add(24*time.Hour)
	funding.DatasetID, trade.DatasetID, mark.DatasetID = strings.Repeat("я", 128), "quoted\"<&\n", "replacement�"
	funding.Environment, trade.Environment, mark.Environment = "mainnet", "mainnet", "mainnet"
	funding.Endpoint = "https://api.bybit.com/v5/market/funding/history"
	trade.Endpoint = "https://api.bybit.com/v5/market/kline"
	mark.Endpoint = "https://api.bybit.com/v5/market/mark-price-kline"
	price, _ := model.ParsePrice("9007199254740993." + strings.Repeat("0", 63) + "1")
	rate, _ := model.ParseRate("-0." + strings.Repeat("0", 63) + "1")
	funding.Events = []model.FundingEvent{{FundingTime: start, Rate: rate}}
	trade.Rows = []TradeCandleContentRow{{OpenTime: start, Open: price, High: price, Low: price, Close: price, Volume: model.Zero(), Confirmed: true}}
	mark.Rows = []model.MarkPriceCandle{{OpenTime: start, Open: price, High: price, Low: price, Close: price, Confirmed: true}}
	for i := 1; i < 1080; i++ {
		funding.Events = append(funding.Events, funding.Events[0])
		funding.Events[i].FundingTime = start.Add(time.Duration(i) * time.Millisecond)
		trade.Rows = append(trade.Rows, trade.Rows[0])
		trade.Rows[i].OpenTime = start.Add(time.Duration(i) * time.Hour)
		mark.Rows = append(mark.Rows, mark.Rows[0])
		mark.Rows[i].OpenTime = trade.Rows[i].OpenTime
	}
	trade.End, mark.End = start.Add(1080*time.Hour), start.Add(1080*time.Hour)
	for _, tc := range []struct {
		encode func() ([]byte, string, error)
		decode func([]byte) ([]byte, error)
	}{
		{func() ([]byte, string, error) { return EncodeFundingContent(funding) }, decodeFixtures()[0].roundTrip},
		{func() ([]byte, string, error) { return EncodeTradeCandleContent(trade) }, decodeFixtures()[1].roundTrip},
		{func() ([]byte, string, error) { return EncodeMarkPriceContent(mark) }, decodeFixtures()[2].roundTrip},
	} {
		raw, _, err := tc.encode()
		if err != nil {
			t.Fatal(err)
		}
		got, err := tc.decode(raw)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatal("large exact roundtrip", err)
		}
	}
}
