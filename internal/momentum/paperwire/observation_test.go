package paperwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// observationFixture создаёт полный публичный DTO без внешних источников.
func observationFixture(t *testing.T) Observation {
	t.Helper()
	at := time.Date(2026, 11, 22, 12, 0, 0, 123000000, time.FixedZone("offset", 10800))
	one := model.Zero()
	third, err := observationRatio(t, "1", "3")
	if err != nil {
		t.Fatal(err)
	}
	negative, err := observationRatio(t, "-2", "7")
	if err != nil {
		t.Fatal(err)
	}
	row := model.Candle{Symbol: "row", IntervalCode: "D", OpenTime: at, Open: third, High: third, Low: one, Close: third, Volume: third, Turnover: third, Confirmed: true}
	return Observation{CapturedAt: at, SourceClock: at.Add(time.Nanosecond), DailyBars: 7,
		TradeWindows: []TradeWindow{{Symbol: "Z", Interval: "D", StartMS: 1, EndMS: 2, Page: model.CandlePage{Environment: "testnet", Source: "public", Category: "linear", Endpoint: "trade", ServerTime: at, Candles: []model.Candle{row, row}}}},
		MarkPages:    []model.MarkPricePage{{Environment: "other", Source: "marksource", Category: "markcat", Endpoint: "mark", Symbol: "Z", IntervalCode: "60", StartTime: at, EndTime: at.Add(time.Millisecond), ServerTime: at, Candles: []model.MarkPriceCandle{{OpenTime: at, Open: third, High: third, Low: one, Close: third, Confirmed: true}}}},
		FundingPages: []model.FundingPage{{Environment: "testnet", Source: "fundingsource", Category: "fundcat", Endpoint: "funding", Symbol: "Z", StartTime: at, EndTime: at, ServerTime: at, Events: []model.FundingEvent{{FundingTime: at, Rate: negative}}}},
	}
}

// observationRoundtrip требует canonical стабильность и полную typed эквивалентность.
func observationRoundtrip(t *testing.T, o Observation) []byte {
	t.Helper()
	raw, err := EncodeObservation(o)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeObservation(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := EncodeObservation(decoded)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("unstable: %s %v", again, err)
	}
	// UTC-нормализация применяется к каждому моменту через тот же explicit wire.
	expected := observationOut(o)
	actual := observationOut(decoded)
	expectedRaw, _ := json.Marshal(expected)
	actualRaw, _ := json.Marshal(actual)
	if !bytes.Equal(expectedRaw, actualRaw) {
		t.Fatal("lost fields")
	}
	return raw
}

// TestObservationRoundtrip проверяет provenance, OHLCV, exact ratio, UTC и независимость.
func TestObservationRoundtrip(t *testing.T) {
	o := observationFixture(t)
	o.TradeWindows[0].Page.Candles[1].Symbol = "raw-first-before-second"
	raw := observationRoundtrip(t, o)
	if !bytes.Contains(raw, []byte(`"numerator":"1","denominator":"3"`)) || !bytes.Contains(raw, []byte(`"numerator":"-2","denominator":"7"`)) || bytes.Contains(raw, []byte("+03:00")) {
		t.Fatal(string(raw))
	}
	decoded, err := DecodeObservation(raw)
	if err != nil {
		t.Fatal(err)
	}
	other, err := DecodeObservation(raw)
	if err != nil {
		t.Fatal(err)
	}
	decoded.TradeWindows[0].Page.Candles[0].Symbol = "mutated"
	decoded.MarkPages[0].Candles[0].Confirmed = false
	decoded.FundingPages[0].Events[0].FundingTime = time.Time{}
	if reflect.DeepEqual(decoded, other) || other.TradeWindows[0].Page.Candles[0].Symbol != "row" {
		t.Fatal("decode alias")
	}
	saved := bytes.Clone(raw)
	o.TradeWindows[0].Page.Candles[0].Symbol = "input mutation"
	if !bytes.Equal(raw, saved) {
		t.Fatal("input alias")
	}
	raw[0] = '['
	fresh, err := EncodeObservation(other)
	if err != nil || !bytes.Equal(fresh, saved) {
		t.Fatal("bytes alias")
	}
}

// TestObservationNilEmpty сохраняет nil и empty для каждого stream и row slice.
func TestObservationNilEmpty(t *testing.T) {
	at := time.Unix(1, 0).UTC()
	for mask := 0; mask < 8; mask++ {
		o := Observation{CapturedAt: at, SourceClock: at}
		if mask&1 != 0 {
			o.TradeWindows = []TradeWindow{}
		}
		if mask&2 != 0 {
			o.MarkPages = []model.MarkPricePage{}
		}
		if mask&4 != 0 {
			o.FundingPages = []model.FundingPage{}
		}
		raw := observationRoundtrip(t, o)
		got, err := DecodeObservation(raw)
		if err != nil || !reflect.DeepEqual(o, got) {
			t.Fatalf("mask %d: %v", mask, err)
		}
	}
	for mask := 0; mask < 8; mask++ {
		o := observationFixture(t)
		o.TradeWindows[0].Page.Candles = nil
		o.MarkPages[0].Candles = nil
		o.FundingPages[0].Events = nil
		if mask&1 != 0 {
			o.TradeWindows[0].Page.Candles = []model.Candle{}
		}
		if mask&2 != 0 {
			o.MarkPages[0].Candles = []model.MarkPriceCandle{}
		}
		if mask&4 != 0 {
			o.FundingPages[0].Events = []model.FundingEvent{}
		}
		raw := observationRoundtrip(t, o)
		got, err := DecodeObservation(raw)
		if err != nil || (got.TradeWindows[0].Page.Candles == nil) != (mask&1 == 0) || (got.MarkPages[0].Candles == nil) != (mask&2 == 0) || (got.FundingPages[0].Events == nil) != (mask&4 == 0) {
			t.Fatalf("rows mask %d", mask)
		}
	}
}

// TestObservationRequestOrdering сортирует только копии requests и сохраняет raw rows.
func TestObservationRequestOrdering(t *testing.T) {
	o := observationFixture(t)
	o.TradeWindows = append(o.TradeWindows, o.TradeWindows[0])
	o.TradeWindows[1].Symbol = "A"
	o.MarkPages = append(o.MarkPages, o.MarkPages[0])
	o.MarkPages[1].Symbol = "A"
	o.FundingPages = append(o.FundingPages, o.FundingPages[0])
	o.FundingPages[1].Symbol = "A"
	raw, err := EncodeObservation(o)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeObservation(raw)
	if err != nil || decoded.TradeWindows[0].Symbol != "A" || decoded.MarkPages[0].Symbol != "A" || decoded.FundingPages[0].Symbol != "A" || o.TradeWindows[0].Symbol != "Z" || o.MarkPages[0].Symbol != "Z" || o.FundingPages[0].Symbol != "Z" {
		t.Fatal("ordering", err)
	}
	for _, kind := range []string{"trade", "mark", "funding"} {
		bad := observationFixture(t)
		switch kind {
		case "trade":
			bad.TradeWindows = append(bad.TradeWindows, bad.TradeWindows[0])
		case "mark":
			bad.MarkPages = append(bad.MarkPages, bad.MarkPages[0])
		case "funding":
			bad.FundingPages = append(bad.FundingPages, bad.FundingPages[0])
		}
		if _, err := MeasureObservation(bad); !errors.Is(err, ErrPaperWire) {
			t.Fatal("duplicate", kind, err)
		}
	}
	// Каждый следующий компонент tuple является самостоятельным sorting tie-breaker.
	for field := 0; field < 3; field++ {
		bad := observationFixture(t)
		a := bad.TradeWindows[0]
		a.Symbol = "Z"
		switch field {
		case 0:
			a.Interval = "A"
		case 1:
			a.StartMS = 0
		case 2:
			a.EndMS = 1
		}
		bad.TradeWindows = append(bad.TradeWindows, a)
		raw, err := EncodeObservation(bad)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeObservation(raw)
		if err != nil || got.TradeWindows[0].Interval != a.Interval || got.TradeWindows[0].StartMS != a.StartMS || got.TradeWindows[0].EndMS != a.EndMS {
			t.Fatal("tie", field, err)
		}
	}
}

// TestObservationInvalidTyped отвергает только структурные ошибки без market proof.
func TestObservationInvalidTyped(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Observation)
	}{
		{"capture", func(o *Observation) { o.CapturedAt = time.Time{} }},
		{"sourceclock", func(o *Observation) { o.SourceClock = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{"decimal", func(o *Observation) { o.TradeWindows[0].Page.Candles[0].Open = model.Decimal{} }},
		{"utf8", func(o *Observation) { o.MarkPages[0].Source = "\xff" }},
		{"trade negative", func(o *Observation) { o.TradeWindows[0].StartMS = -1 }},
		{"trade reverse", func(o *Observation) { o.TradeWindows[0].EndMS = 0 }},
		{"mark reverse", func(o *Observation) { o.MarkPages[0].EndTime = o.MarkPages[0].StartTime.Add(-time.Millisecond) }},
		{"mark fractional", func(o *Observation) { o.MarkPages[0].StartTime = o.MarkPages[0].StartTime.Add(time.Nanosecond) }},
		{"funding reverse", func(o *Observation) { o.FundingPages[0].EndTime = o.FundingPages[0].StartTime.Add(-time.Millisecond) }},
		{"funding fractional", func(o *Observation) { o.FundingPages[0].EndTime = o.FundingPages[0].EndTime.Add(time.Nanosecond) }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			o := observationFixture(t)
			tt.edit(&o)
			m, err := MeasureObservation(o)
			if !errors.Is(err, ErrPaperWire) || m != (ObservationMeasure{}) {
				t.Fatal(m, err)
			}
			if _, err := EncodeObservation(o); !errors.Is(err, ErrPaperWire) {
				t.Fatal(err)
			}
		})
	}
	o := observationFixture(t)
	o.DailyBars = -19
	o.TradeWindows[0].Page.Source = ""
	o.TradeWindows[0].Page.Candles[1].OpenTime = o.TradeWindows[0].Page.Candles[0].OpenTime.Add(-time.Hour)
	observationRoundtrip(t, o)
}

// TestObservationStrictDecode отвергает alternate spelling, неизвестные поля и ratios.
func TestObservationStrictDecode(t *testing.T) {
	raw := string(observationRoundtrip(t, observationFixture(t)))
	cases := []string{
		" " + raw, raw + " ", raw + raw, strings.Replace(raw, `"DailyBars":7`, `"DailyBars":7,"Extra":1`, 1),
		strings.Replace(raw, `"DailyBars":7,`, "", 1), strings.Replace(raw, `"DailyBars":7`, `"DailyBars":7,"DailyBars":7`, 1),
		strings.Replace(raw, `"DailyBars"`, `"dailybars"`, 1), strings.Replace(raw, `"DailyBars":7`, `"DailyBars":7.0`, 1),
		strings.Replace(raw, `"CapturedAt":`, `"SourceClock":`, 1), strings.Replace(raw, `"Source":"public"`, `"Source":"public","extra":0`, 1),
		strings.Replace(raw, `"Confirmed":true`, `"Confirmed":true,"Confirmed":true`, 1),
		strings.Replace(raw, `"numerator":"1","denominator":"3"`, `"numerator":"2","denominator":"6"`, 1),
		strings.Replace(raw, `"denominator":"3"`, `"denominator":"0"`, 1),
		strings.Replace(raw, `"numerator":"1"`, `"numerator":"01"`, 1),
		strings.Replace(raw, `"numerator":"1"`, `"numerator":"`+strings.Repeat("9", 1234)+`"`, 1),
		strings.Replace(raw, `Z"`, `+00:00"`, 1), strings.Replace(raw, `"Source":"public"`, `"Source":"\u0070ublic"`, 1),
		strings.Replace(raw, `"Source":"public"`, "\"Source\":\"\xff\"", 1),
	}
	for i, bad := range cases {
		if _, err := DecodeObservation([]byte(bad)); !errors.Is(err, ErrPaperWire) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}

// TestObservationFullRat сохраняет крайние 4096-bit coefficients без finite marshal.
func TestObservationFullRat(t *testing.T) {
	n := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 4096), big.NewInt(1))
	value, err := observationRatio(t, n.String(), "1")
	if err != nil {
		t.Fatal(err)
	}
	o := observationFixture(t)
	o.TradeWindows[0].Page.Candles[0].Open = value
	raw := observationRoundtrip(t, o)
	if !bytes.Contains(raw, []byte(n.String())) {
		t.Fatal("lost fullRat")
	}
}

// observationActualMeasure считает JSON tokens/depth независимо от typed visitor.
func observationActualMeasure(t *testing.T, raw []byte) ObservationMeasure {
	t.Helper()
	m := ObservationMeasure{Bytes: len(raw)}
	depth := 0
	d := json.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		m.Tokens++
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
				if depth > m.MaxDepth {
					m.MaxDepth = depth
				}
			case '}', ']':
				depth--
			}
		}
	}
	return m
}

// TestObservationMeasureExact сравнивает все counters с настоящим JSON encoder/decoder.
func TestObservationMeasureExact(t *testing.T) {
	for _, text := range []string{"ascii", "\"\\\b\f\n\r\t\x00\x1f<>&\u2028\u2029Привет\ufffd"} {
		o := observationFixture(t)
		o.FundingPages[0].Source = text
		for i := 0; i < 3; i++ {
			if i == 1 {
				o.MarkPages[0].Candles = nil
			}
			if i == 2 {
				o.FundingPages = []model.FundingPage{}
			}
			m, err := MeasureObservation(o)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := EncodeObservation(o)
			if err != nil {
				t.Fatal(err)
			}
			if got := observationActualMeasure(t, raw); got != m {
				t.Fatalf("want %+v got %+v", got, m)
			}
		}
	}
}

// observationRatio создаёт exact тестовый Decimal через foundation amount parser.
func observationRatio(t *testing.T, n, d string) (model.Decimal, error) {
	t.Helper()
	var a amount
	err := json.Unmarshal([]byte(`{"numerator":"`+n+`","denominator":"`+d+`"}`), &a)
	return a.value, err
}

// TestObservationPageTieBreakers проверяет все компоненты canonical request ordering.
func TestObservationPageTieBreakers(t *testing.T) {
	for _, kind := range []string{"mark", "funding"} {
		count := 3
		if kind == "funding" {
			count = 2
		}
		for field := 0; field < count; field++ {
			o := observationFixture(t)
			if kind == "mark" {
				p := o.MarkPages[0]
				switch field {
				case 0:
					p.IntervalCode = "1"
				case 1:
					p.StartTime = p.StartTime.Add(-time.Millisecond)
				case 2:
					p.EndTime = p.StartTime
				}
				o.MarkPages = append(o.MarkPages, p)
			} else {
				p := o.FundingPages[0]
				// Для end tie исходный end сначала сдвигается вперёд.
				o.FundingPages[0].EndTime = o.FundingPages[0].EndTime.Add(time.Millisecond)
				if field == 0 {
					p.StartTime = p.StartTime.Add(-time.Millisecond)
				}
				o.FundingPages = append(o.FundingPages, p)
			}
			raw, err := EncodeObservation(o)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeObservation(raw)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "mark" {
				want := o.MarkPages[1]
				p := got.MarkPages[0]
				if p.IntervalCode != want.IntervalCode || !p.StartTime.Equal(want.StartTime) || !p.EndTime.Equal(want.EndTime) {
					t.Fatal("mark", field)
				}
			} else {
				want := o.FundingPages[1]
				p := got.FundingPages[0]
				if !p.StartTime.Equal(want.StartTime) || !p.EndTime.Equal(want.EndTime) {
					t.Fatal("funding", field)
				}
			}
		}
	}
}

// TestObservationRatioCanonicality проверяет denominator boundary и strict grammar.
func TestObservationRatioCanonicality(t *testing.T) {
	denominator := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 4096), big.NewInt(1))
	value, err := observationRatio(t, "1", denominator.String())
	if err != nil {
		t.Fatal(err)
	}
	o := observationFixture(t)
	o.FundingPages[0].Events[0].Rate = value
	observationRoundtrip(t, o)
	raw := string(observationRoundtrip(t, observationFixture(t)))
	for _, replacement := range []string{
		`"denominator":"-3"`, `"denominator":"03"`, `"denominator":"+3"`,
		`"denominator":3`, `"denominator":null`, `"denominator":"3","extra":0`,
	} {
		bad := strings.Replace(raw, `"denominator":"3"`, replacement, 1)
		if _, err := DecodeObservation([]byte(bad)); !errors.Is(err, ErrPaperWire) {
			t.Fatal(replacement, err)
		}
	}
	o.FundingPages[0].Events[0].Rate = model.Zero()
	raw = string(observationRoundtrip(t, o))
	bad := strings.Replace(raw, `"numerator":"0"`, `"numerator":"-0"`, 1)
	if _, err := DecodeObservation([]byte(bad)); !errors.Is(err, ErrPaperWire) {
		t.Fatal(err)
	}
}
