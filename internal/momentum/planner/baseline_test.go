package planner

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"bybit_bot_liquidity/internal/momentum/model"
)

// baselineState сохраняет фактические значения снимка и переданных объектов.
func baselineState(t *testing.T, s model.PortfolioSnapshot, values ...any) string {
	t.Helper()
	b, err := json.Marshal(append(values, s.Equity(), s.Prices(), s.Positions()))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestBaselineTargets проверяет masks, реинвестирование, расходы и замороженный план.
func TestBaselineTargets(t *testing.T) {
	cases := []struct {
		mask                    int
		equity, fee, uniformQty string
	}{
		{7, "9000", "0.001", "29.940"},
		{7, "18000", "0.001", "59.880"},
		{7, "4500", "0.001", "14.970"},
		{7, "9000", "0", "30"},
		{0, "9000", "0.001", ""},
		{1, "9000", "0.001", ""},
		{2, "9000", "0.001", ""},
		{3, "9000", "0.001", ""},
		{4, "9000", "0.001", ""},
		{5, "9000", "0.001", ""},
		{6, "9000", "0.001", ""},
		{7, "9000", "0.001", ""},
	}
	var saved model.TradePlan
	var original []model.Target
	var frozen string
	for n, tc := range cases {
		syms, in, steps := configuredLongFixture(t)
		want := []string{"29.940", "14.980", "299.600"}
		in.Equity = sizingDecimal(t, tc.equity)
		if tc.uniformQty != "" {
			for i, symbol := range syms {
				in.Prices[symbol], in.Positions[symbol], steps[symbol] = sizingDecimal(t, "100"), model.Zero(), sizingDecimal(t, "0.001")
				want[i] = tc.uniformQty
			}
		}
		active := map[string]bool{syms[2]: tc.mask&4 != 0, syms[1]: tc.mask&2 != 0, syms[0]: tc.mask&1 != 0}
		s := longSnapshot(t, in)
		forward := map[string]bool{syms[0]: active[syms[0]], syms[1]: active[syms[1]], syms[2]: active[syms[2]]}
		before := baselineState(t, s, active, forward, steps)
		got, err := BaselineTargets(syms, s, active, steps, sizingDecimal(t, tc.fee))
		repeat, again := BaselineTargets(syms, s, forward, steps, sizingDecimal(t, tc.fee))
		if err != nil || again != nil || len(got) != 3 || !reflect.DeepEqual(got, repeat) {
			t.Fatalf("case %d: %v / %v", n, err, again)
		}
		for i, symbol := range syms {
			q := model.Zero()
			if active[symbol] {
				q = sizingDecimal(t, want[i])
			}
			if got[i].Symbol != symbol || sizingRat(t, got[i].Quantity).Cmp(sizingRat(t, q)) != 0 {
				t.Fatalf("case %d target %d: %#v", n, i, got[i])
			}
		}
		if n == 0 {
			full, fullErr := BaselineLongTargets(syms, s, steps, sizingDecimal(t, tc.fee))
			if fullErr != nil || !reflect.DeepEqual(full, []model.Target{{Symbol: syms[0], Quantity: sizingDecimal(t, want[0])}, {Symbol: syms[1], Quantity: sizingDecimal(t, want[1])}, {Symbol: syms[2], Quantity: sizingDecimal(t, want[2])}}) {
				t.Fatalf("configured full targets=%v error=%v", full, fullErr)
			}
			metadata := model.PlanMetadata{StrategyID: "s", RunID: "r", ProfileID: "baseline_delta_1x", ModelID: "m", FormulaID: "D-01", ConfigHash: strings.Repeat("a", 64), ScheduledAt: in.CapturedAt, EquityCutoffAt: in.CapturedAt, Revision: 1}
			saved, err = model.NewTradePlan(model.PlanInput{Snapshot: s, Metadata: metadata, Targets: got})
			if err != nil {
				t.Fatal(err)
			}
			original = append([]model.Target(nil), got...)
			frozen = baselineState(t, saved.Snapshot(), saved.Targets(), original, saved.Hash())
		}
		got[0], saved.Targets()[0] = model.Target{}, model.Target{}
		fresh, freshErr := BaselineTargets(syms, s, active, steps, sizingDecimal(t, tc.fee))
		if freshErr != nil || !reflect.DeepEqual(repeat, fresh) || baselineState(t, s, active, forward, steps) != before || baselineState(t, saved.Snapshot(), saved.Targets(), original, saved.Hash()) != frozen {
			t.Fatalf("case %d changed inputs, targets or saved plan", n)
		}
	}
}

// TestBaselineFailures проверяет shape, typed причины и поздний overflow при обеих масках.
func TestBaselineFailures(t *testing.T) {
	for _, kind := range []string{"missing", "extra", "lowercase", "zero", "equity0", "equityNegative", "step", "reserve", "overflow"} {
		symbols, in, steps := configuredLongFixture(t)
		active := map[string]bool{symbols[0]: false, symbols[1]: false, symbols[2]: false}
		fee := sizingDecimal(t, "0.001")
		switch kind {
		case "missing":
			delete(active, symbols[0])
		case "extra":
			active["DOGEUSDT"] = false
		case "lowercase":
			delete(active, symbols[0])
			active[strings.ToLower(symbols[0])] = false
		case "equity0", "equityNegative":
			in.Equity = sizingDecimal(t, map[string]string{"equity0": "0", "equityNegative": "-1"}[kind])
		case "step":
			steps[symbols[2]] = model.Decimal{}
		case "reserve":
			in.Positions[symbols[2]] = sizingDecimal(t, "300001")
		case "overflow":
			fee = model.Zero()
			factor := sizingDecimal(t, "1"+strings.Repeat("0", 100))
			for i := 0; i < 12; i++ {
				var err error
				in.Equity, err = in.Equity.Mul(factor)
				if err != nil {
					t.Fatal(err)
				}
			}
			early, earlyErr := BaselineLongTargets(symbols, longSnapshot(t, in), steps, fee)
			if earlyErr != nil || len(early) != 3 {
				t.Fatalf("ordinary prices must size successfully: %v", earlyErr)
			}
			in.Prices[symbols[2]] = sizingDecimal(t, "0."+strings.Repeat("0", 63)+"1")
		}
		s := longSnapshot(t, in)
		if kind == "zero" {
			s = model.PortfolioSnapshot{}
		}
		want := error(ReasonInvalidBaselineInput)
		if kind != "missing" && kind != "extra" && kind != "lowercase" {
			_, want = BaselineLongTargets(symbols, s, steps, fee)
		}
		for _, flag := range []bool{true, false} {
			active[symbols[2]] = flag
			got, err := BaselineTargets(symbols, s, active, steps, fee)
			var plan BaselinePlanReason
			var sizing BaselineSizingReason
			typed := errors.As(err, &plan) && plan == want || errors.As(err, &sizing) && sizing == want
			if want == nil || got != nil || err != want || !errors.Is(err, want) || (!typed && kind != "overflow") {
				t.Fatalf("%s active=%v error=%v want=%v", kind, flag, err, want)
			}
		}
	}
}
