package paperwire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"bybit_bot_liquidity/internal/momentum/model"
)

// TestInputExactBytes проверяет cap/cap+1 и regression прежней строки /6.
func TestInputExactBytes(t *testing.T) {
	d := inputFixture(t)
	d.Run.OnboardingID = ""
	base := inputRecord(t, d)
	d.Run.OnboardingID = strings.Repeat("x", MaxCanonicalBytes-len(base.Payload()))
	r := inputRecord(t, d)
	if len(r.Payload()) != MaxCanonicalBytes || len(d.Run.OnboardingID) <= MaxCanonicalBytes/6 {
		t.Fatal("boundary fixture")
	}
	decoded, err := DecodeInputRecord(r.Payload())
	if err != nil || !bytes.Equal(r.Payload(), decoded.Payload()) {
		t.Fatal("exact boundary", err)
	}
	d.Run.OnboardingID += "x"
	if _, err := EncodeInputRecord(d); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("bytes +1", err)
	}
	if _, err := DecodeInputRecord(append(r.Payload(), ' ')); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("decode bytes +1", err)
	}
	d = inputFixture(t)
	d.Run.OnboardingID = strings.Repeat("<", MaxCanonicalBytes/6+1)
	if _, err := EncodeInputRecord(d); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("escaped overflow", err)
	}
}

// TestInputMeterEscapes сверяет counting pass с реальным encoding/json и вложенной глубиной.
func TestInputMeterEscapes(t *testing.T) {
	for _, s := range []string{"plain", "\x00\x01\b\t\n\r\f\"\\<>&\u2028\u2029", "Русский 😀"} {
		m := recordMeter{}
		m.text(s)
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if m.err != nil || m.bytes != len(raw) || m.tokens != 1 {
			t.Fatal("escape measurement", m, raw)
		}
		d := inputFixture(t)
		d.Run.OnboardingID = s
		inputRecord(t, d)
	}
	d := inputFixture(t)
	r := inputRecord(t, d)
	actual := observationActualMeasure(t, r.Payload())
	if actual.MaxDepth != 8 {
		t.Fatalf("enclosing source depth=%d", actual.MaxDepth)
	}
	m := recordMeter{depth: MaxJSONDepth - 1}
	m.object(field{"nested", func() { m.object() }})
	if !errors.Is(m.err, ErrPaperResourceLimit) {
		t.Fatal("nested depth +1", m)
	}
}

// TestInputMeterCaps проверяет точные tokens/depth/array границы независимо от более строгих caps.
func TestInputMeterCaps(t *testing.T) {
	m := recordMeter{}
	m.add(0, MaxJSONTokens)
	if m.err != nil {
		t.Fatal(m.err)
	}
	m.add(0, 1)
	if !errors.Is(m.err, ErrPaperResourceLimit) {
		t.Fatal("tokens +1")
	}
	m = recordMeter{depth: MaxJSONDepth - 1}
	m.object()
	if m.err != nil || m.maxDepth != MaxJSONDepth {
		t.Fatal("depth cap", m)
	}
	m = recordMeter{}
	m.array(MaxArrayItems, func(int) { m.add(1, 1) })
	if m.err != nil {
		t.Fatal("array cap", m.err)
	}
	m = recordMeter{}
	m.array(MaxArrayItems+1, func(int) { t.Fatal("visited oversized array") })
	if !errors.Is(m.err, ErrPaperResourceLimit) {
		t.Fatal("array +1")
	}
	m = recordMeter{bytes: MaxCanonicalBytes}
	m.add(1, 0)
	if !errors.Is(m.err, ErrPaperResourceLimit) {
		t.Fatal("meter bytes +1")
	}
	for _, n := range []int{MaxJSONDepth, MaxJSONDepth + 1} {
		raw := []byte(strings.Repeat("[", n) + "0" + strings.Repeat("]", n))
		err := inputPreflight(raw)
		if n == MaxJSONDepth && err != nil || n > MaxJSONDepth && !errors.Is(err, ErrPaperResourceLimit) {
			t.Fatal("decode depth", n, err)
		}
	}
	for _, n := range []int{MaxArrayItems, MaxArrayItems + 1} {
		raw := []byte("[" + strings.Repeat("0,", n-1) + "0]")
		err := inputPreflight(raw)
		if n == MaxArrayItems && err != nil || n > MaxArrayItems && !errors.Is(err, ErrPaperResourceLimit) {
			t.Fatal("decode array", n, err)
		}
	}
}

// TestInputEmbeddedCaps проверяет allocation guard, exact decoded cap и padding boundary.
func TestInputEmbeddedCaps(t *testing.T) {
	for _, limit := range []int{MaxConfigBytes, MaxPlanBytes} {
		for _, n := range []int{limit - 1, limit, limit + 1} {
			text := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, n))
			guard := encodedGuard(text, limit)
			raw, err := embedded(text, limit)
			if n <= limit {
				if guard != nil || err != nil || len(raw) != n {
					t.Fatal("valid boundary", limit, n, guard, err)
				}
			} else if !errors.Is(guard, ErrPaperResourceLimit) || !errors.Is(err, ErrPaperResourceLimit) {
				t.Fatal("padding overflow", limit, n, guard, err)
			}
			key := "config_payload"
			prefix := `{"run":{`
			suffix := `}}`
			if limit == MaxPlanBytes {
				key = "plan_payload"
				prefix = `{`
				suffix = `}`
			}
			payload := []byte(prefix + `"` + key + `":"` + text + `"` + suffix)
			e := inputPreflight(payload)
			if n <= limit && e != nil || n > limit && !errors.Is(e, ErrPaperResourceLimit) {
				t.Fatal("stream embedded", limit, n, e)
			}
		}
	}
	for _, s := range []string{"Zh==", "Zg==\n", "Zg=", "?==="} {
		if _, err := embedded(s, 10); !errors.Is(err, ErrPaperWire) {
			t.Fatal("noncanonical base64", s, err)
		}
	}
}

// TestInputFillCaps проверяет до typed allocation суммарный actual fill cap.
func TestInputFillCaps(t *testing.T) {
	d := inputFixture(t)
	one := d.Fills[0]
	d.Fills = make([]model.ConfirmedFill, MaxActualFillRecords)
	for i := range d.Fills {
		d.Fills[i] = one
	}
	r := inputRecord(t, d)
	if _, err := DecodeInputRecord(r.Payload()); err != nil {
		t.Fatal("exact fills", err)
	}
	d.Fills = append(d.Fills, one)
	if _, err := EncodeInputRecord(d); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("typed fills +1", err)
	}
	for _, n := range []int{MaxActualFillRecords, MaxActualFillRecords + 1} {
		raw := []byte(`{"fills":[` + strings.Repeat(`{},`, n-1) + `{}]}`)
		err := inputPreflight(raw)
		if n == MaxActualFillRecords && err != nil || n > MaxActualFillRecords && !errors.Is(err, ErrPaperResourceLimit) {
			t.Fatal("stream fills", n, err)
		}
	}
}

// TestInputObservationResources проверяет composition и независимые page/row guards зависимости.
func TestInputObservationResources(t *testing.T) {
	d := inputFixture(t)
	d.Observation.TradeWindows = make([]TradeWindow, MaxObservationPages+1)
	if _, err := EncodeInputRecord(d); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("pages before clone", err)
	}
	d = inputFixture(t)
	d.Observation.TradeWindows[0].Page.Candles = make([]model.Candle, MaxObservationRows+1)
	if _, err := EncodeInputRecord(d); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("rows before clone", err)
	}
	d = inputFixture(t)
	d.Observation.TradeWindows[0].Page.Source = ""
	base, err := MeasureObservation(d.Observation)
	if err != nil {
		t.Fatal(err)
	}
	d.Observation.TradeWindows[0].Page.Source = strings.Repeat("x", MaxCanonicalBytes-base.Bytes-1024)
	measured, err := MeasureObservation(d.Observation)
	if err != nil {
		t.Fatal("standalone valid observation", err)
	}
	if measured.Bytes >= MaxCanonicalBytes {
		t.Fatal("fixture")
	}
	if _, err := EncodeInputRecord(d); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("enclosing budget before source clone", err)
	}
	for _, fills := range [][]model.ConfirmedFill{nil, {}} {
		d = inputFixture(t)
		d.Fills = fills
		d.Observation.TradeWindows = nil
		d.Observation.MarkPages = []model.MarkPricePage{}
		d.Observation.FundingPages = nil
		r := inputRecord(t, d)
		if !bytes.Equal(r.Payload(), inputRecord(t, r.Values()).Payload()) {
			t.Fatal("nested nil/empty")
		}
	}
}
