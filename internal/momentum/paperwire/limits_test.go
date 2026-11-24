package paperwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"bybit_bot_liquidity/internal/momentum/planner"
)

// assertResource требует обе категории отказа и отсутствие panic.
func assertResource(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrPaperResourceLimit) || !errors.Is(err, ErrPaperWire) {
		t.Fatal("resource refusal lost category", err)
	}
}

// tokenObject строит syntactically valid JSON ровно на границе числа tokens.
func tokenObject(extra bool) []byte {
	var out strings.Builder
	out.WriteByte('{')
	for i := 0; i < (MaxJSONTokens-2)/2; i++ {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(`"k` + strconv.Itoa(i) + `":`)
		if extra && i == (MaxJSONTokens-2)/2-1 {
			out.WriteString(`[]`)
		} else {
			out.WriteByte('0')
		}
	}
	out.WriteByte('}')
	return []byte(out.String())
}

// TestJSONResourceBoundaries проверяет точный cap и cap+1 каждого общего лимита.
func TestJSONResourceBoundaries(t *testing.T) {
	exactBytes := []byte(`"` + strings.Repeat("a", MaxCanonicalBytes-2) + `"`)
	if err := preflight(exactBytes); err != nil {
		t.Fatal("exact byte cap rejected", err)
	}
	assertResource(t, preflight(append(exactBytes, ' ')))
	exactDepth := []byte(strings.Repeat("[", MaxJSONDepth) + "0" + strings.Repeat("]", MaxJSONDepth))
	if err := preflight(exactDepth); err != nil {
		t.Fatal("exact depth cap rejected", err)
	}
	assertResource(t, preflight(append(append([]byte("["), exactDepth...), ']')))
	exactArray := []byte("[" + strings.Repeat("0,", MaxArrayItems-1) + "0]")
	if err := preflight(exactArray); err != nil {
		t.Fatal("exact array cap rejected", err)
	}
	assertResource(t, preflight([]byte("["+strings.Repeat("0,", MaxArrayItems)+"0]")))
	if err := preflight(tokenObject(false)); err != nil {
		t.Fatal("exact token cap rejected", err)
	}
	assertResource(t, preflight(tokenObject(true)))
}

// TestJSONMalformed не принимает незакрытые, лишние или повреждённые документы.
func TestJSONMalformed(t *testing.T) {
	for _, raw := range []string{"", " ", "[", "{", `{"a":`, `{"a":]}`, `[]]`, `{} {}`, `[1,]`, "\xff"} {
		if err := preflight([]byte(raw)); !errors.Is(err, ErrPaperWire) {
			t.Fatal("malformed JSON accepted", raw, err)
		}
	}
	for _, raw := range []string{`{"текст":"Привет \\\"мир\\\""}`, `[{},[],null,true,"value",1]`, `null`} {
		if err := preflight([]byte(raw)); err != nil {
			t.Fatal("valid JSON rejected", err)
		}
	}
}

type allocationSpy struct{ calls int }

// UnmarshalJSON отмечает достижение typed decode после общего preflight.
func (s *allocationSpy) UnmarshalJSON([]byte) error { s.calls++; return nil }

// TestPreflightBeforeTypedDecode доказывает, что oversized arrays не попадают в typed target.
func TestPreflightBeforeTypedDecode(t *testing.T) {
	spy := new(allocationSpy)
	raw := []byte(`{"value":[` + strings.Repeat("0,", MaxArrayItems) + `0]}`)
	assertResource(t, decodeFields(raw, []field{{"value", spy}}))
	if spy.calls != 0 {
		t.Fatal("typed decoder ran before resource refusal")
	}
	if err := decodeFields([]byte(`{"value":[]}`), []field{{"value", spy}}); err != nil || spy.calls != 1 {
		t.Fatal("positive decoder path absent", err)
	}
}

// TestFieldCodecBoundaries проверяет полный byte cap и безопасный отказ encoder/decoder.
func TestFieldCodecBoundaries(t *testing.T) {
	value := strings.Repeat("a", MaxCanonicalBytes-len(`{"value":""}`))
	raw, err := encodeFields([]field{{"value", value}})
	if err != nil || len(raw) != MaxCanonicalBytes {
		t.Fatal("exact encoder byte cap", err)
	}
	var decoded string
	if err = decodeFields(raw, []field{{"value", &decoded}}); err != nil || decoded != value {
		t.Fatal("bounded decode lost data", err)
	}
	_, err = encodeFields([]field{{"value", value + "a"}})
	assertResource(t, err)
	out, err := encodeFields([]field{{"value", make(chan int)}})
	if !errors.Is(err, ErrPaperWire) || out != nil {
		t.Fatal("unsupported value accepted", err)
	}
	for _, bad := range []string{`null`, `[]`, `{}`, `{"other":"x"}`, `{"value":1}`, `{"value":"x","extra":0}`} {
		if err = decodeFields([]byte(bad), []field{{"value", &decoded}}); !errors.Is(err, ErrPaperWire) {
			t.Fatal("invalid fields accepted", bad, err)
		}
	}
}

// TestNativeProfileResource отказует до clone/sort/encode чрезмерных данных caller.
func TestNativeProfileResource(t *testing.T) {
	p := profileFixture(t)
	rule, schedule := p.Parameters.Rules["ALT123USDT"], p.FundingSchedules["ALT123USDT"]
	p.Parameters.Rules = make(map[string]planner.MarketDeltaRules, MaxArrayItems+1)
	p.FundingSchedules = make(map[string]FundingSchedule, MaxArrayItems+1)
	for i := 0; i <= MaxArrayItems; i++ {
		symbol := "A" + strconv.Itoa(i) + "USDT"
		p.Parameters.Rules[symbol] = rule
		p.FundingSchedules[symbol] = schedule
	}
	raw, hash, err := EncodeProfile(p)
	assertResource(t, err)
	if raw != nil || hash != "" {
		t.Fatal("resource error returned accepted profile")
	}
	symbol := strings.Repeat("A", MaxCanonicalBytes/2) + "USDT"
	p.Parameters.Rules = map[string]planner.MarketDeltaRules{symbol: rule}
	p.FundingSchedules = map[string]FundingSchedule{symbol: schedule}
	_, _, err = EncodeProfile(p)
	assertResource(t, err)
	_, err = ParseRecordedAmount(strings.Repeat("0", MaxCanonicalBytes+1))
	assertResource(t, err)
}

// TestProfileNestedStrictness проверяет дополнительные, повторные и пропущенные nested fields.
func TestProfileNestedStrictness(t *testing.T) {
	raw := profileBytes(t, profileFixture(t))
	for _, tc := range [][2]string{
		{`"qty_step":{"numerator":"1","denominator":"10"}`, `"qty_step":{"numerator":"1","denominator":"10","extra":0}`},
		{`"qty_step":{"numerator":"1","denominator":"10"}`, `"qty_step":{"numerator":"1","numerator":"1","denominator":"10"}`},
		{`"min_qty":{"numerator":"1","denominator":"10"},`, ""},
		{`"interval_ns":"28800000000000"`, `"interval_ns":"28800000000000","extra":0`},
		{`"symbol":"ALT123USDT","anchor"`, `"symbol":"OTHERUSDT","anchor"`},
	} {
		changed := replaceWire(t, raw, tc[0], tc[1])
		if _, err := DecodeProfile(changed); !errors.Is(err, ErrPaperWire) {
			t.Fatal("nested noncanonical profile accepted", err)
		}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	var schedules []json.RawMessage
	if err := json.Unmarshal(object["funding_schedules"], &schedules); err != nil {
		t.Fatal(err)
	}
	duplicated := append(append([]byte("["), schedules[0]...), append(append([]byte(","), schedules[0]...), ']')...)
	changed := bytes.Replace(raw, object["funding_schedules"], duplicated, 1)
	if _, err := DecodeProfile(changed); !errors.Is(err, ErrPaperWire) {
		t.Fatal("duplicate schedule accepted", err)
	}
}

// TestProfileTokenBudget проверяет последний читаемый профиль и первый отказ encoder.
func TestProfileTokenBudget(t *testing.T) {
	p := profileFixture(t)
	rule, schedule := p.Parameters.Rules["ALT123USDT"], p.FundingSchedules["ALT123USDT"]
	p.Parameters.Rules = make(map[string]planner.MarketDeltaRules, 6553)
	p.FundingSchedules = make(map[string]FundingSchedule, 6553)
	for i := 0; i < 6552; i++ {
		symbol := "A" + strconv.Itoa(i) + "USDT"
		p.Parameters.Rules[symbol] = rule
		p.FundingSchedules[symbol] = schedule
	}
	raw, hash, err := EncodeProfile(p)
	if err != nil || raw == nil || hash == "" {
		t.Fatal("6552-symbol profile rejected", err)
	}
	decoded, err := DecodeProfile(raw)
	if err != nil || len(decoded.Parameters.Rules) != 6552 {
		t.Fatal("issued profile cannot be decoded", err)
	}
	again, againHash, err := EncodeProfile(decoded)
	if err != nil || !bytes.Equal(raw, again) || hash != againHash {
		t.Fatal("boundary profile identity changed", err)
	}
	p.Parameters.Rules["A6552USDT"] = rule
	p.FundingSchedules["A6552USDT"] = schedule
	raw, hash, err = EncodeProfile(p)
	assertResource(t, err)
	if raw != nil || hash != "" {
		t.Fatal("6553-symbol refusal returned bytes or identity")
	}
}
