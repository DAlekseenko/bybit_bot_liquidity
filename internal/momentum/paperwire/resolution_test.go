package paperwire

import (
	"bytes"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// resolutionFixture задаёт partial abort с фактическим состоянием, отличным от expected.
func resolutionFixture(t *testing.T) (ResolutionDocument, InputRecord) {
	t.Helper()
	input := inputRecord(t, inputFixture(t))
	v := input.Values()
	d := ResolutionDocument{InputHash: input.Hash(), RunID: v.Run.RunID, PlanHash: v.Plan.Hash(), Kind: "ABORTED",
		ResolvedAt: v.Run.ScheduledAt.Add(time.Second), ControlRevision: 1, CommandID: "halt-1", Fills: v.Fills,
		Remaining: []IntentRemainder{{0, "ALT123USDT", model.FillBuy, decimalFixture(t, "2"), decimalFixture(t, "1"), decimalFixture(t, "1")}}, ActualAfter: v.ExpectedAfter}
	d.ActualAfter.WalletUSDT = decimalFixture(t, "42")
	return d, input
}

// resolutionRecord требует успешного публичного конструктора и exact hash.
func resolutionRecord(t *testing.T, d ResolutionDocument, input InputRecord) ResolutionRecord {
	t.Helper()
	r, err := EncodeResolutionRecord(d, input)
	if err != nil {
		t.Fatal(err)
	}
	if r.Hash() != payloadHash(r.Payload()) || r.Header() != input.Header() {
		t.Fatal("identity")
	}
	return r
}

// TestResolutionRoundtrip проверяет полный receipt и отсутствие финансовой интерпретации.
func TestResolutionRoundtrip(t *testing.T) {
	for _, kind := range []string{"COMPLETED", "ABORTED"} {
		d, input := resolutionFixture(t)
		d.Kind = kind
		if kind == "COMPLETED" {
			d.CommandID, d.ControlRevision, d.Remaining[0].Remaining = "", 0, model.Zero()
			d.ResolvedAt = input.Header().ScheduledAt
		}
		r := resolutionRecord(t, d, input)
		out, err := DecodeResolutionRecord(r.Payload(), input)
		if err != nil || !bytes.Equal(r.Payload(), out.Payload()) || !bytes.Equal(r.Payload(), resolutionRecord(t, out.Values(), input).Payload()) {
			t.Fatal("roundtrip", err)
		}
		if sameDecimal(out.Values().ActualAfter.WalletUSDT, input.Values().ExpectedAfter.WalletUSDT) {
			t.Fatal("actual replaced by expected")
		}
		want := []string{"format", "input_sha256", "run_id", "plan_sha256", "disposition", "resolved_at", "control_revision", "command_id", "fills", "remaining", "actual_after"}
		previous := -1
		for _, key := range want {
			index := bytes.Index(r.Payload(), []byte(`"`+key+`":`))
			if index <= previous {
				t.Fatal("root order", key)
			}
			previous = index
		}
	}
	for _, empty := range []bool{false, true} {
		d, input := resolutionFixture(t)
		d.Fills, d.Remaining = nil, nil
		if empty {
			d.Fills, d.Remaining = []model.ConfirmedFill{}, []IntentRemainder{}
		}
		r := resolutionRecord(t, d, input)
		out, err := DecodeResolutionRecord(r.Payload(), input)
		if err != nil || !bytes.Equal(r.Payload(), resolutionRecord(t, out.Values(), input).Payload()) {
			t.Fatal("nil/empty", err)
		}
	}
	var zero ResolutionRecord
	if zero.Hash() != "" || zero.Payload() != nil || zero.Header() != (CycleBinding{}) {
		t.Fatal("zero receipt")
	}
}

// TestResolutionIsolation проверяет constructor/accessor aliases и decoder source bytes.
func TestResolutionIsolation(t *testing.T) {
	d, input := resolutionFixture(t)
	r := resolutionRecord(t, d, input)
	before, hash, header := r.Payload(), r.Hash(), r.Header()
	mutate := func(v *ResolutionDocument) {
		v.Fills[0].ExecutionID = "mutated"
		v.Remaining[0].Symbol = "mutated"
		v.ActualAfter.Positions["ALT123USDT"] = model.LongPosition{}
		v.ActualAfter.WalletUSDT = model.Zero()
	}
	mutate(&d)
	values := r.Values()
	mutate(&values)
	payload := r.Payload()
	payload[0] = '!'
	h := r.Header()
	h.RunID = "mutated"
	out, err := DecodeResolutionRecord(before, input)
	if err != nil {
		t.Fatal(err)
	}
	before[0] = '!'
	if r.Hash() != hash || r.Header() != header || out.Hash() != hash || !bytes.Equal(r.Payload(), out.Payload()) {
		t.Fatal("alias escaped")
	}
	if r.Values().Fills[0].ExecutionID == "mutated" || r.Values().Remaining[0].Symbol == "mutated" {
		t.Fatal("slice alias")
	}
	if !bytes.Equal(r.Payload(), resolutionRecord(t, r.Values(), input).Payload()) {
		t.Fatal("state alias")
	}
}

// TestResolutionInvalid проверяет bindings, disposition, время, вселенную и остатки.
func TestResolutionInvalid(t *testing.T) {
	cases := map[string]func(*ResolutionDocument){
		"input":              func(d *ResolutionDocument) { d.InputHash = strings.Repeat("0", 64) },
		"run":                func(d *ResolutionDocument) { d.RunID += "x" },
		"plan":               func(d *ResolutionDocument) { d.PlanHash = strings.Repeat("0", 64) },
		"kind":               func(d *ResolutionDocument) { d.Kind = "CANCELLED" },
		"command":            func(d *ResolutionDocument) { d.CommandID = " " },
		"revision":           func(d *ResolutionDocument) { d.ControlRevision = 0 },
		"completed-command":  func(d *ResolutionDocument) { d.Kind = "COMPLETED" },
		"completed-residual": func(d *ResolutionDocument) { d.Kind, d.CommandID, d.ControlRevision = "COMPLETED", "", 0 },
		"before":             func(d *ResolutionDocument) { d.ResolvedAt = d.ResolvedAt.Add(-time.Hour) },
		"submicro":           func(d *ResolutionDocument) { d.ResolvedAt = d.ResolvedAt.Add(time.Nanosecond) },
		"time-range":         func(d *ResolutionDocument) { d.ResolvedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"universe":           func(d *ResolutionDocument) { delete(d.ActualAfter.Positions, "BTCUSDT") },
		"extra-universe":     func(d *ResolutionDocument) { d.ActualAfter.Positions["X"] = d.ActualAfter.Positions["BTCUSDT"] },
		"duplicate":          func(d *ResolutionDocument) { d.Remaining = append(d.Remaining, d.Remaining[0]) },
		"negative-index":     func(d *ResolutionDocument) { d.Remaining[0].IntentIndex = -1 },
		"symbol":             func(d *ResolutionDocument) { d.Remaining[0].Symbol = "UNKNOWN" },
		"side":               func(d *ResolutionDocument) { d.Remaining[0].Side = "HOLD" },
		"requested":          func(d *ResolutionDocument) { d.Remaining[0].Requested = decimalFixture(t, "-1") },
		"confirmed":          func(d *ResolutionDocument) { d.Remaining[0].Confirmed = decimalFixture(t, "-1") },
		"remaining":          func(d *ResolutionDocument) { d.Remaining[0].Remaining = decimalFixture(t, "-1") },
		"fill":               func(d *ResolutionDocument) { d.Fills[0].ExecutionID = "" },
		"state-time":         func(d *ResolutionDocument) { d.ActualAfter.AsOf = time.Time{} },
		"fill-time":          func(d *ResolutionDocument) { d.Fills[0].ExecutedAt = time.Time{} },
		"position":           func(d *ResolutionDocument) { d.ActualAfter.Positions["BTCUSDT"] = model.LongPosition{} },
	}
	for name, mutate := range cases {
		d, input := resolutionFixture(t)
		mutate(&d)
		if _, err := EncodeResolutionRecord(d, input); !errors.Is(err, ErrPaperWire) {
			t.Fatal(name, err)
		}
	}
	d, input := resolutionFixture(t)
	r := resolutionRecord(t, d, input)
	if _, err := EncodeResolutionRecord(d, InputRecord{}); !errors.Is(err, ErrPaperWire) {
		t.Fatal("zero encode", err)
	}
	if _, err := DecodeResolutionRecord(r.Payload(), InputRecord{}); !errors.Is(err, ErrPaperWire) {
		t.Fatal("zero decode", err)
	}
	other := inputFixture(t)
	other.Run.OnboardingID += "different"
	if _, err := DecodeResolutionRecord(r.Payload(), inputRecord(t, other)); !errors.Is(err, ErrPaperWire) {
		t.Fatal("supplied input", err)
	}
}

// TestResolutionStrict отклоняет все неканонические формы, включая вложенные поля.
func TestResolutionStrict(t *testing.T) {
	d, input := resolutionFixture(t)
	raw := string(resolutionRecord(t, d, input).Payload())
	cases := []string{
		raw + " ", raw + "{}", " " + raw,
		strings.Replace(raw, `"format":`, `"unknown":0,"format":`, 1),
		strings.Replace(raw, `"command_id":"halt-1",`, "", 1),
		strings.Replace(raw, `"command_id":"halt-1"`, `"command_id":"halt-1","command_id":"halt-1"`, 1),
		strings.Replace(raw, `"control_revision":"1"`, `"control_revision":"01"`, 1),
		strings.Replace(raw, `"control_revision":"1"`, `"control_revision":"+1"`, 1),
		strings.Replace(raw, `"control_revision":"1"`, `"control_revision":"18446744073709551616"`, 1),
		strings.Replace(raw, `"disposition":"ABORTED"`, `"disposition":"aborted"`, 1),
		strings.Replace(raw, `"IntentIndex":0,`, "", 1),
		strings.Replace(raw, `"IntentIndex":0`, `"IntentIndex":0,"unknown":0`, 1),
		strings.Replace(raw, `"IntentIndex":0`, `"IntentIndex":0,"IntentIndex":0`, 1),
		strings.Replace(raw, `"IntentIndex":0,"Symbol":"ALT123USDT"`, `"Symbol":"ALT123USDT","IntentIndex":0`, 1),
		strings.Replace(raw, `"WalletUSDT":`, `"walletusdt":`, 1),
		strings.Replace(raw, `"FundingThrough":`, `"Extra":0,"FundingThrough":`, 1),
		strings.Replace(raw, `"resolved_at":"2025-01-06T00:00:01.000123Z"`, `"resolved_at":"2025-01-06T00:00:01.000123+00:00"`, 1),
		strings.Replace(raw, `"format":"momentum/paper-cycle-resolution/v1"`, `"format":"momentum/paper-cycle-input/v1"`, 1),
	}
	for i, bad := range cases {
		if bad == raw {
			t.Fatal("ineffective mutation", i)
		}
		if _, err := DecodeResolutionRecord([]byte(bad), input); !errors.Is(err, ErrPaperWire) {
			t.Fatal("strict", i, err)
		}
	}
}

// TestResolutionRatios сохраняет repeating и полный 4096-bit диапазон без округления.
func TestResolutionRatios(t *testing.T) {
	d, input := resolutionFixture(t)
	third, err := decimalFixture(t, "1").Quo(decimalFixture(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 4096), big.NewInt(1))
	huge, err := reducedAmount(max, big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	tiny, err := reducedAmount(big.NewInt(1), max)
	if err != nil {
		t.Fatal(err)
	}
	d.ActualAfter.WalletUSDT, d.ActualAfter.EquityUSDT = huge, tiny
	d.Remaining[0].Requested, d.Remaining[0].Remaining = third, third
	d.Fills[0].ExecutionID = strings.Repeat("f", 11000)
	r := resolutionRecord(t, d, input)
	out, err := DecodeResolutionRecord(r.Payload(), input)
	if err != nil || len(r.Payload()) <= 10000 || !bytes.Equal(r.Payload(), resolutionRecord(t, out.Values(), input).Payload()) {
		t.Fatal("full Rat roundtrip", err)
	}
	raw := string(r.Payload())
	for _, bad := range []string{`{"numerator":"2","denominator":"6"}`, `{"numerator":"01","denominator":"3"}`, `{"numerator":"1","denominator":"0"}`, `{"numerator":"1","denominator":"-3"}`, `{"numerator":"-0","denominator":"1"}`, `{"numerator":"1","denominator":"` + new(big.Int).Lsh(big.NewInt(1), 4096).String() + `"}`} {
		mutated := strings.Replace(raw, `{"numerator":"1","denominator":"3"}`, bad, 1)
		if _, err := DecodeResolutionRecord([]byte(mutated), input); !errors.Is(err, ErrPaperWire) {
			t.Fatal("ratio", bad, err)
		}
	}
}

// TestResolutionExactBytes проверяет ASCII/escaping cap и cap+1 до bulk copy.
func TestResolutionExactBytes(t *testing.T) {
	for _, escaped := range []bool{false, true} {
		d, input := resolutionFixture(t)
		d.CommandID = "x"
		base := resolutionRecord(t, d, input)
		room := MaxCanonicalBytes - len(base.Payload())
		if escaped {
			d.CommandID += strings.Repeat("<", room/6) + strings.Repeat("x", room%6)
		} else {
			d.CommandID += strings.Repeat("x", room)
		}
		r := resolutionRecord(t, d, input)
		if len(r.Payload()) != MaxCanonicalBytes {
			t.Fatal("cap fixture")
		}
		out, err := DecodeResolutionRecord(r.Payload(), input)
		if err != nil || !bytes.Equal(r.Payload(), out.Payload()) {
			t.Fatal("cap roundtrip", err)
		}
		d.CommandID += "x"
		if _, err := EncodeResolutionRecord(d, input); !errors.Is(err, ErrPaperResourceLimit) {
			t.Fatal("cap+1", err)
		}
		if _, err := DecodeResolutionRecord(append(r.Payload(), ' '), input); !errors.Is(err, ErrPaperResourceLimit) {
			t.Fatal("decode cap+1", err)
		}
	}
	for _, s := range []string{"\x00\b\t\n\r\f\"\\<>&\u2028\u2029", "Русский 😀"} {
		d, input := resolutionFixture(t)
		d.CommandID = s
		resolutionRecord(t, d, input)
	}
}

// TestResolutionResourceArrays проверяет guards до обхода invalid DTO и typed decode.
func TestResolutionResourceArrays(t *testing.T) {
	d, input := resolutionFixture(t)
	one := d.Fills[0]
	d.Fills = make([]model.ConfirmedFill, MaxActualFillRecords)
	for i := range d.Fills {
		d.Fills[i] = one
	}
	r := resolutionRecord(t, d, input)
	if _, err := DecodeResolutionRecord(r.Payload(), input); err != nil {
		t.Fatal(err)
	}
	d.Fills = append(d.Fills, model.ConfirmedFill{})
	if _, err := EncodeResolutionRecord(d, input); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("fills guard", err)
	}
	d, input = resolutionFixture(t)
	d.Remaining = make([]IntentRemainder, MaxArrayItems+1)
	if _, err := EncodeResolutionRecord(d, input); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("remaining guard", err)
	}
	d, input = resolutionFixture(t)
	d.ActualAfter.Positions = make(map[string]model.LongPosition, MaxArrayItems+1)
	for i := 0; i <= MaxArrayItems; i++ {
		d.ActualAfter.Positions[strings.Repeat("x", i/1000)+string(rune(i%1000+32))] = model.LongPosition{}
	}
	if _, err := EncodeResolutionRecord(d, input); !errors.Is(err, ErrPaperResourceLimit) {
		t.Fatal("state guard", err)
	}
	for key, limit := range map[string]int{"fills": MaxActualFillRecords, "remaining": MaxArrayItems} {
		for _, n := range []int{limit, limit + 1} {
			raw := []byte(`{"` + key + `":[` + strings.Repeat(`{},`, n-1) + `{}]}`)
			err := resolutionPreflight(raw)
			if n == limit && err != nil || n > limit && !errors.Is(err, ErrPaperResourceLimit) {
				t.Fatal(key, n, err)
			}
			if n > limit {
				if _, err := DecodeResolutionRecord(raw, input); !errors.Is(err, ErrPaperResourceLimit) {
					t.Fatal("before typed", err)
				}
			}
		}
	}
}

// TestResolutionGenericCaps проверяет независимые exact tokens/depth/array guards.
func TestResolutionGenericCaps(t *testing.T) {
	for _, extra := range []int{0, 1} {
		var arrays []string
		for i := 0; i < 4; i++ {
			n := 65532
			if i == 3 {
				n += 2 + extra
			}
			arrays = append(arrays, `"`+string(rune('a'+i))+` ":[`+strings.Repeat("0,", n-1)+"0]")
		}
		raw := []byte("{" + strings.Join(arrays, ",") + "}")
		err := resolutionPreflight(raw)
		if extra == 0 && err != nil || extra == 1 && !errors.Is(err, ErrPaperResourceLimit) {
			t.Fatal("token boundary", extra, err)
		}
	}
	for _, n := range []int{MaxJSONDepth, MaxJSONDepth + 1} {
		err := resolutionPreflight([]byte(strings.Repeat("[", n) + "0" + strings.Repeat("]", n)))
		if n == MaxJSONDepth && err != nil || n > MaxJSONDepth && !errors.Is(err, ErrPaperResourceLimit) {
			t.Fatal("depth", n, err)
		}
	}
	for _, n := range []int{MaxArrayItems, MaxArrayItems + 1} {
		err := resolutionPreflight([]byte("[" + strings.Repeat("0,", n-1) + "0]"))
		if n == MaxArrayItems && err != nil || n > MaxArrayItems && !errors.Is(err, ErrPaperResourceLimit) {
			t.Fatal("array", n, err)
		}
	}
}
