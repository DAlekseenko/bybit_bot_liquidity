package planner

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// longFixture создаёт разные цены, позиции и шаги в обратном порядке вставки.
func longFixture(t *testing.T) (model.SnapshotInput, map[string]model.Decimal) {
	t.Helper()
	in := model.SnapshotInput{Environment: "paper", AccountID: "test", ConfigVersion: "v1", CapturedAt: time.Unix(1, 0), Equity: sizingDecimal(t, "9000"), Prices: map[string]model.Decimal{}, Positions: map[string]model.Decimal{}}
	steps := map[string]model.Decimal{}
	for i, symbol := range []string{"HYPEUSDT", "ETHUSDT", "BTCUSDT"} {
		in.Prices[symbol], in.Positions[symbol], steps[symbol] = sizingDecimal(t, []string{"10", "200", "100"}[i]), sizingDecimal(t, []string{"400", "10", "0"}[i]), sizingDecimal(t, []string{"0.01", "0.005", "0.001"}[i])
	}
	return in, steps
}

// longSnapshot строит только публично допустимые снимки.
func longSnapshot(t *testing.T, in model.SnapshotInput) model.PortfolioSnapshot {
	t.Helper()
	s, err := model.NewPortfolioSnapshot(in)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestBaselineLongTargets проверяет полные цели, порядок, dust и независимость.
func TestBaselineLongTargets(t *testing.T) {
	for _, scenario := range []string{"distinct", "rational", "dust"} {
		in, steps := longFixture(t)
		fee, want := sizingDecimal(t, "0.001"), []string{"29.940", "14.980", "299.600"}
		if scenario != "distinct" {
			in.Equity, fee, want = sizingDecimal(t, "1"), model.Zero(), map[string][]string{"rational": {"0.333", "0.333", "0.333"}, "dust": {"0", "0", "0"}}[scenario]
			step := map[string]string{"rational": "0.001", "dust": "1"}[scenario]
			for symbol := range steps {
				in.Prices[symbol], in.Positions[symbol], steps[symbol] = sizingDecimal(t, "1"), model.Zero(), sizingDecimal(t, step)
			}
		}
		s := longSnapshot(t, in)
		before, beforeErr := json.Marshal([]any{in, s.Equity(), s.Prices(), s.Positions(), steps, fee})
		got, err := BaselineLongTargets([]string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}, s, steps, fee)
		repeat, repeatErr := BaselineLongTargets([]string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}, s, steps, fee)
		if beforeErr != nil || err != nil || repeatErr != nil || len(got) != 3 || !reflect.DeepEqual(got, repeat) {
			t.Fatalf("%s: %v / %v", scenario, err, repeatErr)
		}
		for i, symbol := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
			if got[i].Symbol != symbol || sizingRat(t, got[i].Quantity).Cmp(sizingRat(t, sizingDecimal(t, want[i]))) != 0 {
				t.Fatalf("%s: target %d", scenario, i)
			}
		}
		after, afterErr := json.Marshal([]any{in, s.Equity(), s.Prices(), s.Positions(), steps, fee})
		if afterErr != nil || string(after) != string(before) {
			t.Fatal("mutated inputs")
		}
		in.Prices["BTCUSDT"], in.Positions["BTCUSDT"], steps["BTCUSDT"] = model.Zero(), sizingDecimal(t, "99"), sizingDecimal(t, "99")
		got[0] = model.Target{}
		if repeat[0].Symbol != "BTCUSDT" || sizingRat(t, repeat[0].Quantity).Cmp(sizingRat(t, sizingDecimal(t, want[0]))) != 0 || reflect.DeepEqual(s.Prices(), in.Prices) || reflect.DeepEqual(s.Positions(), in.Positions) {
			t.Fatal("aliased caller state")
		}
	}
}

// TestBaselineLongErrors проверяет структурные и scalar причины без частичных целей.
func TestBaselineLongErrors(t *testing.T) {
	in, steps := longFixture(t)
	fee := sizingDecimal(t, "0.001")
	// setup изменяет только названный аргумент текущего независимого случая.
	cases := []struct {
		name  string
		setup func()
		want  error
	}{
		{"zero snapshot", func() {}, ReasonInvalidBaselineInput},
		{"zero equity", func() { in.Equity = model.Zero() }, ReasonNonPositiveEquity},
		{"negative equity", func() { in.Equity = sizingDecimal(t, "-1") }, ReasonNonPositiveEquity},
		{"missing price", func() { delete(in.Prices, "HYPEUSDT") }, ReasonInvalidBaselineInput},
		{"extra price", func() { in.Prices["EXTRA"] = sizingDecimal(t, "1") }, ReasonInvalidBaselineInput},
		{"missing position", func() { delete(in.Positions, "HYPEUSDT") }, ReasonInvalidBaselineInput},
		{"extra position", func() { in.Positions["EXTRA"] = model.Zero() }, ReasonInvalidBaselineInput},
		{"missing step", func() { delete(steps, "HYPEUSDT") }, ReasonInvalidBaselineInput},
		{"extra step", func() { steps["EXTRA"] = sizingDecimal(t, "1") }, ReasonInvalidBaselineInput},
		{"lowercase", func() {
			steps = map[string]model.Decimal{"BTCUSDT": steps["BTCUSDT"], "ETHUSDT": steps["ETHUSDT"], "hypeusdt": steps["HYPEUSDT"]}
		}, ReasonInvalidBaselineInput},
		{"short HYPE", func() { in.Positions["HYPEUSDT"] = sizingDecimal(t, "-1") }, ReasonInvalidSizingInput},
		{"invalid fee", func() { fee = model.Decimal{} }, ReasonInvalidSizingInput},
		{"negative fee", func() { fee = sizingDecimal(t, "-1") }, ReasonInvalidSizingInput},
		{"invalid HYPE step", func() { steps["HYPEUSDT"] = model.Decimal{} }, ReasonInvalidSizingInput},
		{"zero HYPE step", func() { steps["HYPEUSDT"] = model.Zero() }, ReasonInvalidSizingInput},
		{"negative HYPE step", func() { steps["HYPEUSDT"] = sizingDecimal(t, "-1") }, ReasonInvalidSizingInput},
		{"rational HYPE step", func() { steps["HYPEUSDT"] = sizingDecimal(t, "1/3") }, ReasonInvalidSizingInput},
		{"infeasible HYPE reserve", func() { in.Positions["HYPEUSDT"] = sizingDecimal(t, "300001") }, ReasonInfeasibleReserve},
	}
	for _, tc := range cases {
		in, steps = longFixture(t)
		fee = sizingDecimal(t, "0.001")
		tc.setup()
		s := longSnapshot(t, in)
		if tc.name == "zero snapshot" {
			s = model.PortfolioSnapshot{}
		}
		got, err := BaselineLongTargets([]string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}, s, steps, fee)
		var plan BaselinePlanReason
		var sizing BaselineSizingReason
		_, isPlan := tc.want.(BaselinePlanReason)
		if got != nil || err != tc.want || !errors.Is(err, tc.want) || (isPlan && (!errors.As(err, &plan) || plan != tc.want || err.Error() != string(plan))) || (!isPlan && (!errors.As(err, &sizing) || sizing != tc.want || err.Error() != string(sizing))) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

// TestBaselineLongOverflow подтверждает ранние доли, поздний overflow и входы.
func TestBaselineLongOverflow(t *testing.T) {
	in, steps := longFixture(t)
	factor := sizingDecimal(t, "1"+strings.Repeat("0", 100))
	for i := 0; i < 12; i++ {
		var err error
		if in.Equity, err = in.Equity.Mul(factor); err != nil {
			t.Fatal(err)
		}
	}
	in.Prices["HYPEUSDT"] = sizingDecimal(t, "0."+strings.Repeat("0", 63)+"1")
	s, fee := longSnapshot(t, in), model.Zero()
	before, beforeErr := json.Marshal([]any{in, s.Equity(), s.Prices(), s.Positions(), steps, fee})
	budget, budgetErr := in.Equity.Quo(sizingDecimal(t, "3"))
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		if _, err := BaselineQuantity(budget, in.Prices[symbol], in.Positions[symbol], fee, steps[symbol]); budgetErr != nil || err != nil {
			t.Fatalf("budget=%v scalar=%v", budgetErr, err)
		}
	}
	_, overflow := BaselineQuantity(budget, in.Prices["HYPEUSDT"], in.Positions["HYPEUSDT"], fee, steps["HYPEUSDT"])
	got, err := BaselineLongTargets([]string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}, s, steps, fee)
	if beforeErr != nil || overflow == nil || got != nil || err != overflow || !errors.Is(err, overflow) {
		t.Fatalf("overflow=%v err=%v", overflow, err)
	}
	after, afterErr := json.Marshal([]any{in, s.Equity(), s.Prices(), s.Positions(), steps, fee})
	if afterErr != nil || string(after) != string(before) {
		t.Fatal("overflow mutated inputs")
	}
}
