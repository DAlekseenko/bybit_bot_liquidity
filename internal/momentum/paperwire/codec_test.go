package paperwire

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// decimalFixture создаёт точный тестовый вход без неявного округления.
func decimalFixture(t *testing.T, text string) model.Decimal {
	t.Helper()
	value, err := model.ParseAmount(text)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// profileFixture задаёт все параметры явно и проверяет независимость от числа3.
func profileFixture(t *testing.T) Profile {
	t.Helper()
	return Profile{Parameters: backtest.SimulationParameters{FeeRate: model.Zero(), SlippageRate: model.Zero(), MarkMaxAge: time.Minute,
		Rules: map[string]planner.MarketDeltaRules{"ALT123USDT": {QtyStep: decimalFixture(t, "0.1"), MinQty: decimalFixture(t, "0.1"), MaxMarketQty: decimalFixture(t, "10"), MinIncreaseNotional: model.Zero()}}},
		SourceMaxAge: 2 * time.Minute, DataDeadline: time.Hour, Poll: time.Second, RPCTimeout: 3 * time.Second, ExecutionReference: "closed-hourly-trade-close/v1",
		FundingSchedules: map[string]FundingSchedule{"ALT123USDT": {Anchor: time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC), Interval: 8 * time.Hour}}}
}

// profileBytes требует успешного публичного codec на явной фикстуре.
func profileBytes(t *testing.T, p Profile) []byte {
	t.Helper()
	raw, hash, err := EncodeProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hash != hex.EncodeToString(sum[:]) {
		t.Fatal("hash differs from exact bytes")
	}
	return raw
}

// TestProfileCanonicalRoundTrip проверяет все поля, один символ и отсутствие aliases.
func TestProfileCanonicalRoundTrip(t *testing.T) {
	p := profileFixture(t)
	raw := profileBytes(t, p)
	expected := `{"format":"momentum/paper-model/v1","fee_rate":{"numerator":"0","denominator":"1"},"slippage_rate":{"numerator":"0","denominator":"1"},"mark_max_age_ns":"60000000000","source_max_age_ns":"120000000000","data_deadline_ns":"3600000000000","poll_ns":"1000000000","rpc_timeout_ns":"3000000000","execution_reference":"closed-hourly-trade-close/v1","execution_time_policy":"plan-scheduled-at/v1","mark_reference":"previous-closed-hour-mark/v1","funding_mode":"observed","rules":[{"symbol":"ALT123USDT","qty_step":{"numerator":"1","denominator":"10"},"min_qty":{"numerator":"1","denominator":"10"},"max_market_qty":{"numerator":"10","denominator":"1"},"min_increase_notional":{"numerator":"0","denominator":"1"}}],"funding_schedules":[{"symbol":"ALT123USDT","anchor":"2025-01-06T00:00:00Z","interval_ns":"28800000000000"}]}`
	if string(raw) != expected {
		t.Fatalf("unexpected canonical profile: %s", raw)
	}
	decoded, err := DecodeProfile(raw)
	if err != nil || !bytes.Equal(raw, profileBytes(t, decoded)) {
		t.Fatal("roundtrip changed values", err)
	}
	second, err := DecodeProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	p.Parameters.Rules["ALT123USDT"] = planner.MarketDeltaRules{}
	delete(p.FundingSchedules, "ALT123USDT")
	decoded.Parameters.Rules["ALT123USDT"] = planner.MarketDeltaRules{}
	delete(decoded.FundingSchedules, "ALT123USDT")
	if !bytes.Equal(raw, profileBytes(t, second)) {
		t.Fatal("caller mutation reached decoded profile or bytes")
	}
	raw[0] = '['
	if !bytes.Equal([]byte(expected), profileBytes(t, second)) {
		t.Fatal("output bytes were retained")
	}
}

// TestProfilePositiveRates сохраняет явно заданные положительные fee и slippage.
func TestProfilePositiveRates(t *testing.T) {
	p := profileFixture(t)
	p.Parameters.FeeRate = decimalFixture(t, "0.001")
	p.Parameters.SlippageRate = decimalFixture(t, "0.15")
	raw := profileBytes(t, p)
	decoded, err := DecodeProfile(raw)
	if err != nil || !bytes.Equal(raw, profileBytes(t, decoded)) {
		t.Fatal("positive explicit rates changed", err)
	}
}

// TestProfileMapOrder проверяет сортировку без фиксированного размера вселенной.
func TestProfileMapOrder(t *testing.T) {
	a, b := profileFixture(t), profileFixture(t)
	rule, schedule := a.Parameters.Rules["ALT123USDT"], a.FundingSchedules["ALT123USDT"]
	for _, symbol := range []string{"ZZUSDT", "BTCUSDT"} {
		a.Parameters.Rules[symbol] = rule
		a.FundingSchedules[symbol] = schedule
	}
	for _, symbol := range []string{"BTCUSDT", "ZZUSDT"} {
		b.Parameters.Rules[symbol] = rule
		b.FundingSchedules[symbol] = schedule
	}
	if !bytes.Equal(profileBytes(t, a), profileBytes(t, b)) {
		t.Fatal("map insertion order affected identity")
	}
}

// TestProfileInvalidValues отвергает неполные модели до выдачи байтов или hash.
func TestProfileInvalidValues(t *testing.T) {
	third, _ := decimalFixture(t, "1").Quo(decimalFixture(t, "3"))
	cases := map[string]func(*Profile){
		"invalid-fee":      func(p *Profile) { p.Parameters.FeeRate = model.Decimal{} },
		"negative-fee":     func(p *Profile) { p.Parameters.FeeRate = decimalFixture(t, "-1") },
		"rational-fee":     func(p *Profile) { p.Parameters.FeeRate = third },
		"negative-slip":    func(p *Profile) { p.Parameters.SlippageRate = decimalFixture(t, "-0.1") },
		"unit-slip":        func(p *Profile) { p.Parameters.SlippageRate = decimalFixture(t, "1") },
		"invalid-slip":     func(p *Profile) { p.Parameters.SlippageRate = model.Decimal{} },
		"rational-slip":    func(p *Profile) { p.Parameters.SlippageRate = third },
		"nil-rules":        func(p *Profile) { p.Parameters.Rules = nil },
		"nil-schedules":    func(p *Profile) { p.FundingSchedules = nil },
		"extra-schedule":   func(p *Profile) { p.FundingSchedules["BTCUSDT"] = p.FundingSchedules["ALT123USDT"] },
		"missing-schedule": func(p *Profile) { delete(p.FundingSchedules, "ALT123USDT") },
		"mark-age":         func(p *Profile) { p.Parameters.MarkMaxAge = 0 },
		"source-age":       func(p *Profile) { p.SourceMaxAge = -1 },
		"deadline":         func(p *Profile) { p.DataDeadline = 0 },
		"long-deadline":    func(p *Profile) { p.DataDeadline = 7*24*time.Hour + 1 },
		"poll":             func(p *Profile) { p.Poll = 0 },
		"rpc-timeout":      func(p *Profile) { p.RPCTimeout = -1 },
		"revision":         func(p *Profile) { p.ExecutionReference = "other" },
		"zero-anchor":      func(p *Profile) { p.FundingSchedules["ALT123USDT"] = FundingSchedule{Interval: time.Second} },
		"early-anchor": func(p *Profile) {
			p.FundingSchedules["ALT123USDT"] = FundingSchedule{Anchor: time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC), Interval: time.Second}
		},
		"late-anchor": func(p *Profile) {
			p.FundingSchedules["ALT123USDT"] = FundingSchedule{Anchor: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), Interval: time.Second}
		},
		"subms-anchor": func(p *Profile) {
			s := p.FundingSchedules["ALT123USDT"]
			s.Anchor = s.Anchor.Add(1)
			p.FundingSchedules["ALT123USDT"] = s
		},
		"zero-interval": func(p *Profile) {
			s := p.FundingSchedules["ALT123USDT"]
			s.Interval = 0
			p.FundingSchedules["ALT123USDT"] = s
		},
		"negative-interval": func(p *Profile) {
			s := p.FundingSchedules["ALT123USDT"]
			s.Interval = -time.Second
			p.FundingSchedules["ALT123USDT"] = s
		},
		"subms-interval": func(p *Profile) {
			s := p.FundingSchedules["ALT123USDT"]
			s.Interval = time.Millisecond + 1
			p.FundingSchedules["ALT123USDT"] = s
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p := profileFixture(t)
			change(&p)
			raw, hash, err := EncodeProfile(p)
			if !errors.Is(err, ErrPaperWire) || raw != nil || hash != "" {
				t.Fatal("invalid profile accepted", err)
			}
		})
	}
	for _, symbol := range []string{"USDT", "altUSDT", "BTC_USDT", " BTCUSDT", "БТСUSDT", "BTCUSD", ""} {
		t.Run("symbol/"+symbol, func(t *testing.T) {
			p := profileFixture(t)
			r, s := p.Parameters.Rules["ALT123USDT"], p.FundingSchedules["ALT123USDT"]
			p.Parameters.Rules = map[string]planner.MarketDeltaRules{symbol: r}
			p.FundingSchedules = map[string]FundingSchedule{symbol: s}
			if _, _, err := EncodeProfile(p); !errors.Is(err, ErrPaperWire) {
				t.Fatal(err)
			}
		})
	}
	for _, field := range []string{"step", "minimum", "maximum", "notional"} {
		for _, value := range []model.Decimal{{}, decimalFixture(t, "-1"), third, model.Zero()} {
			if field == "notional" && reflect.DeepEqual(value, model.Zero()) {
				continue
			}
			p := profileFixture(t)
			rule := p.Parameters.Rules["ALT123USDT"]
			switch field {
			case "step":
				rule.QtyStep = value
			case "minimum":
				rule.MinQty = value
			case "maximum":
				rule.MaxMarketQty = value
			case "notional":
				rule.MinIncreaseNotional = value
			}
			p.Parameters.Rules["ALT123USDT"] = rule
			if _, _, err := EncodeProfile(p); !errors.Is(err, ErrPaperWire) {
				t.Fatal("invalid rule accepted", field, err)
			}
		}
	}
}

// replaceWire меняет одно точное место, сохраняя остальные canonical keys и порядок.
func replaceWire(t *testing.T, raw []byte, old, new string) []byte {
	t.Helper()
	if bytes.Count(raw, []byte(old)) != 1 {
		t.Fatalf("fixture replacement is ambiguous: %s", old)
	}
	return bytes.Replace(raw, []byte(old), []byte(new), 1)
}

// TestProfileRejectsWire проверяет строгую форму, а не эффект посторонней перестановки.
func TestProfileRejectsWire(t *testing.T) {
	raw := profileBytes(t, profileFixture(t))
	for _, duration := range []string{"mark_max_age_ns", "source_max_age_ns", "data_deadline_ns", "poll_ns", "rpc_timeout_ns", "interval_ns"} {
		var value string
		if duration == "interval_ns" {
			value = "28800000000000"
		} else {
			var object map[string]json.RawMessage
			if json.Unmarshal(raw, &object) != nil || json.Unmarshal(object[duration], &value) != nil {
				t.Fatal("duration fixture")
			}
		}
		for _, replacement := range []string{`"0"`, `"-1"`, `"01"`, `"1.0"`, `"9223372036854775808"`, `null`, `1`} {
			changed := replaceWire(t, raw, `"`+duration+`":"`+value+`"`, `"`+duration+`":`+replacement)
			if _, err := DecodeProfile(changed); !errors.Is(err, ErrPaperWire) {
				t.Fatal("bad duration accepted", duration, replacement, err)
			}
		}
	}
	cases := map[string][]byte{
		"unknown":        append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"other":0}`)...),
		"missing":        replaceWire(t, raw, `"format":"momentum/paper-model/v1",`, ""),
		"duplicate":      replaceWire(t, raw, `"format":`, `"format":"momentum/paper-model/v1","format":`),
		"space":          append([]byte(" "), raw...),
		"trailing":       append(append([]byte(nil), raw...), []byte(`{}`)...),
		"order":          replaceWire(t, raw, `"fee_rate":{"numerator":"0","denominator":"1"},"slippage_rate":{"numerator":"0","denominator":"1"}`, `"slippage_rate":{"numerator":"0","denominator":"1"},"fee_rate":{"numerator":"0","denominator":"1"}`),
		"format":         replaceWire(t, raw, "momentum/paper-model/v1", "momentum/paper-model/v2"),
		"execution":      replaceWire(t, raw, "plan-scheduled-at/v1", "future-price/v1"),
		"mark":           replaceWire(t, raw, "previous-closed-hour-mark/v1", "other/v1"),
		"funding":        replaceWire(t, raw, `"funding_mode":"observed"`, `"funding_mode":"zero"`),
		"timezone":       replaceWire(t, raw, "2025-01-06T00:00:00Z", "2025-01-06T00:00:00+00:00"),
		"timestamp":      replaceWire(t, raw, "2025-01-06T00:00:00Z", "not-time"),
		"ratio-null":     replaceWire(t, raw, `"fee_rate":{"numerator":"0","denominator":"1"}`, `"fee_rate":null`),
		"escaped-symbol": replaceWire(t, raw, `"symbol":"ALT123USDT","qty_step"`, `"symbol":"\u0041LT123USDT","qty_step"`),
	}
	for name, changed := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := DecodeProfile(changed)
			if !errors.Is(err, ErrPaperWire) || p.Parameters.Rules != nil {
				t.Fatal("noncanonical wire accepted", err)
			}
		})
	}
	p := profileFixture(t)
	r, s := p.Parameters.Rules["ALT123USDT"], p.FundingSchedules["ALT123USDT"]
	p.Parameters.Rules["ZZUSDT"], p.FundingSchedules["ZZUSDT"] = r, s
	var w profileWire
	ordered := profileBytes(t, p)
	if err := decodeFields(ordered, profileFields(&w)); err != nil {
		t.Fatal(err)
	}
	w.Rules[0], w.Rules[1] = w.Rules[1], w.Rules[0]
	unordered, err := encodeFields(profileFields(&w))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeProfile(unordered); !errors.Is(err, ErrPaperWire) {
		t.Fatal("unsorted rules accepted", err)
	}
	if err := decodeFields(ordered, profileFields(&w)); err != nil {
		t.Fatal(err)
	}
	w.Schedules[0], w.Schedules[1] = w.Schedules[1], w.Schedules[0]
	unordered, err = encodeFields(profileFields(&w))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeProfile(unordered); !errors.Is(err, ErrPaperWire) {
		t.Fatal("unsorted schedules accepted", err)
	}
	w.Schedules[0], w.Schedules[1] = w.Schedules[1], w.Schedules[0]
	w.Rules[1] = w.Rules[0]
	duplicate, err := encodeFields(profileFields(&w))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeProfile(duplicate); !errors.Is(err, ErrPaperWire) {
		t.Fatal("duplicate rules accepted", err)
	}
}

// TestProfileTimeBoundary проверяет UTC-нормализацию и допустимые точные границы.
func TestProfileTimeBoundary(t *testing.T) {
	p := profileFixture(t)
	p.DataDeadline = 7 * 24 * time.Hour
	p.FundingSchedules["ALT123USDT"] = FundingSchedule{Anchor: time.Date(1969, 12, 31, 23, 0, 0, 0, time.FixedZone("before UTC", -3600)), Interval: time.Millisecond}
	raw := profileBytes(t, p)
	if !bytes.Contains(raw, []byte(`"anchor":"1970-01-01T00:00:00Z"`)) {
		t.Fatal("UTC epoch was shifted")
	}
	if _, err := DecodeProfile(raw); err != nil {
		t.Fatal(err)
	}
}

// TestRatioExact сохраняет repeating ratios и весь model диапазон без ParseAmount больших строк.
func TestRatioExact(t *testing.T) {
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 4096), big.NewInt(1))
	denominator := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 4095), big.NewInt(1))
	numerator := new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 4095), big.NewInt(1))
	for _, pair := range [][2]string{{"0", "1"}, {"-1", "3"}, {"1", "3"}, {max.String(), "1"}, {numerator.String(), denominator.String()}} {
		raw, _ := json.Marshal(ratio{pair[0], pair[1]})
		var a amount
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatal(err)
		}
		n, d, err := a.value.CanonicalRatio()
		if err != nil || n != pair[0] || d != pair[1] {
			t.Fatal("ratio lost precision", err)
		}
		out, err := json.Marshal(a)
		if err != nil || !bytes.Equal(raw, out) {
			t.Fatal("ratio roundtrip", err)
		}
	}
}

// TestRatioRejectsMalformed проверяет canonical grammar до big integer allocation.
func TestRatioRejectsMalformed(t *testing.T) {
	overflow := new(big.Int).Lsh(big.NewInt(1), 4096).String()
	cases := []string{
		`null`,
		`{}`,
		`[]`,
		`{"numerator":"1","denominator":"0"}`,
		`{"numerator":"2","denominator":"6"}`,
		`{"numerator":"-0","denominator":"1"}`,
		`{"numerator":"01","denominator":"1"}`,
		`{"numerator":"+1","denominator":"1"}`,
		`{"numerator":"1e2","denominator":"1"}`,
		`{"numerator":"1","denominator":"-1"}`,
		`{"numerator":"0","denominator":"2"}`,
		`{"numerator":"1","denominator":"1","extra":1}`,
		`{"denominator":"1","numerator":"1"}`,
		`{"numerator":1,"denominator":"1"}`,
		`{"numerator":"1","denominator":1}`,
	}
	for _, pair := range [][2]string{{overflow, "1"}, {"1", overflow}, {strings.Repeat("9", 1235), "1"}, {"1", strings.Repeat("9", 1235)}} {
		raw, _ := json.Marshal(ratio{pair[0], pair[1]})
		cases = append(cases, string(raw))
	}
	for _, raw := range cases {
		a := amount{decimalFixture(t, "7")}
		before := a.value
		if err := json.Unmarshal([]byte(raw), &a); !errors.Is(err, ErrPaperWire) {
			t.Fatal("malformed ratio accepted", raw, err)
		}
		if !reflect.DeepEqual(a.value, before) {
			t.Fatal("failed decode changed receiver")
		}
	}
}

// TestRecordedAmounts сокращает raw SQL коэффициент до bounded model.Decimal.
func TestRecordedAmounts(t *testing.T) {
	bigInteger := new(big.Int).Lsh(big.NewInt(1), 4095)
	tinyDenominator := new(big.Int).Lsh(big.NewInt(1), 4095)
	tiny := new(big.Rat).SetFrac(big.NewInt(1), tinyDenominator).FloatString(4095)
	for _, tc := range []struct{ text, n, d string }{
		{"0", "0", "1"},
		{"-0.000", "0", "1"},
		{"00012.34000", "617", "50"},
		{"-1.25", "-5", "4"},
		{bigInteger.String(), bigInteger.String(), "1"},
		{tiny, "1", tinyDenominator.String()},
		{tiny + "000", "1", tinyDenominator.String()},
	} {
		value, err := ParseRecordedAmount(tc.text)
		if err != nil {
			t.Fatal("valid recorded amount rejected", err)
		}
		n, d, err := value.CanonicalRatio()
		if err != nil || n != tc.n || d != tc.d {
			t.Fatal("recorded amount rounded", err)
		}
	}
	p := profileFixture(t)
	tinyValue, err := ParseRecordedAmount(tiny)
	if err != nil {
		t.Fatal(err)
	}
	p.Parameters.FeeRate = tinyValue
	if _, err = DecodeProfile(profileBytes(t, p)); err != nil {
		t.Fatal("tiny finite profile fee rejected", err)
	}
	overflow := new(big.Int).Lsh(big.NewInt(1), 4096).String()
	for _, bad := range []string{"", "-", "+1", "1.", ".1", "1e2", "NaN", "Infinity", " 1", "1 0", "1.2.3", overflow, "0." + strings.Repeat("0", 4095) + "1"} {
		value, err := ParseRecordedAmount(bad)
		if !errors.Is(err, ErrPaperWire) || errors.Is(err, ErrPaperResourceLimit) || !reflect.DeepEqual(value, model.Decimal{}) {
			t.Fatal("bad SQL amount accepted", bad, err)
		}
	}
}

type poisonTransport struct{ calls atomic.Int64 }

// RoundTrip запрещает любую реальную отправку в тесте чистого foundation.
func (p *poisonTransport) RoundTrip(*http.Request) (*http.Response, error) {
	p.calls.Add(1)
	return nil, errors.New("network forbidden")
}

// TestFoundationNoNetwork проверяет, что fake credentials не дают чистому codec побочных эффектов.
func TestFoundationNoNetwork(t *testing.T) {
	poison := new(poisonTransport)
	old := http.DefaultTransport
	http.DefaultTransport = poison
	t.Cleanup(func() { http.DefaultTransport = old })
	t.Setenv("BYBIT_API_KEY", "fake-key")
	t.Setenv("BYBIT_API_SECRET", "fake-secret")
	raw := profileBytes(t, profileFixture(t))
	if _, err := DecodeProfile(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRecordedAmount("123.4500"); err != nil {
		t.Fatal(err)
	}
	if poison.calls.Load() != 0 {
		t.Fatal("foundation attempted network")
	}
}
