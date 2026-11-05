package planner

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
)

// configuredLongFixture получает universe из публичного Parse без I/O.
func configuredLongFixture(t *testing.T) ([]string, model.SnapshotInput, map[string]model.Decimal) {
	t.Helper()
	c, err := config.Parse([]byte(`schema_version: 1
strategy_id: test
profile_version: v1
model_id: roc30
start_policy: now
symbols: [XRPUSDT, ADAUSDT, SOLUSDT]
leverage: 1
margin_mode: ISOLATED_MARGIN
auto_add_margin: false
position_mode: one_way
data_source:
  kind: bybit_testnet_public
  environment: testnet
`))
	if err != nil {
		t.Fatal(err)
	}
	symbols := c.Values().Symbols
	in, oldSteps := longFixture(t)
	prices, positions := in.Prices, in.Positions
	in.Prices, in.Positions = map[string]model.Decimal{}, map[string]model.Decimal{}
	steps := map[string]model.Decimal{}
	for i, old := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		in.Prices[symbols[i]], in.Positions[symbols[i]], steps[symbols[i]] = prices[old], positions[old], oldSteps[old]
	}
	return symbols, in, steps
}

// assertConfiguredLongReason проверяет точный typed отказ без частичных целей.
func assertConfiguredLongReason(t *testing.T, symbols []string, in model.SnapshotInput, steps map[string]model.Decimal) {
	t.Helper()
	got, err := BaselineLongTargets(symbols, longSnapshot(t, in), steps, sizingDecimal(t, "0.001"))
	var reason BaselinePlanReason
	if got != nil || err != ReasonInvalidBaselineInput || !errors.Is(err, ReasonInvalidBaselineInput) || !errors.As(err, &reason) || reason != ReasonInvalidBaselineInput {
		t.Fatalf("symbols=%v result=%v error=%v", symbols, got, err)
	}
}

// TestConfiguredLongTargets проверяет Parse→Values→цели, порядок и независимость.
func TestConfiguredLongTargets(t *testing.T) {
	symbols, in, steps := configuredLongFixture(t)
	s := longSnapshot(t, in)
	before, beforeErr := json.Marshal([]any{symbols, in, s.Equity(), s.Prices(), s.Positions(), steps})
	got, err := BaselineLongTargets(symbols, s, steps, sizingDecimal(t, "0.001"))
	permuted := []string{"XRPUSDT", "ADAUSDT", "SOLUSDT"}
	repeat, again := BaselineLongTargets(permuted, s, steps, sizingDecimal(t, "0.001"))
	if beforeErr != nil || err != nil || again != nil || len(got) != 3 || !reflect.DeepEqual(got, repeat) {
		t.Fatalf("targets=%v errors=%v/%v", got, err, again)
	}
	for i, symbol := range []string{"ADAUSDT", "SOLUSDT", "XRPUSDT"} {
		if got[i].Symbol != symbol || sizingRat(t, got[i].Quantity).Cmp(sizingRat(t, sizingDecimal(t, []string{"29.940", "14.980", "299.600"}[i]))) != 0 {
			t.Fatal("unexpected configured target", got[i])
		}
	}
	after, afterErr := json.Marshal([]any{symbols, in, s.Equity(), s.Prices(), s.Positions(), steps})
	if afterErr != nil || string(before) != string(after) || !reflect.DeepEqual(permuted, []string{"XRPUSDT", "ADAUSDT", "SOLUSDT"}) {
		t.Fatal("mutated universe, snapshot or steps")
	}
	symbols[0], got[0] = "MUTATED", model.Target{}
	fresh, freshErr := BaselineLongTargets(permuted, s, steps, sizingDecimal(t, "0.001"))
	if freshErr != nil || repeat[0].Symbol != "ADAUSDT" || !reflect.DeepEqual(fresh, repeat) {
		t.Fatal("universe or targets alias another result")
	}
}

// TestConfiguredLongInputs проверяет universe и exact keys всех финансовых карт.
func TestConfiguredLongInputs(t *testing.T) {
	for _, symbols := range [][]string{
		{"ADAUSDT", "SOLUSDT"},
		{"ADAUSDT", "SOLUSDT", "XRPUSDT", "DOGEUSDT"},
		{"ADAUSDT", "ADAUSDT", "XRPUSDT"},
		{"adausdt", "SOLUSDT", "XRPUSDT"},
	} {
		_, in, steps := configuredLongFixture(t)
		assertConfiguredLongReason(t, symbols, in, steps)
	}
	for field := 0; field < 3; field++ {
		for _, edit := range []string{"missing", "extra", "old whitelist"} {
			symbols, in, steps := configuredLongFixture(t)
			values := []map[string]model.Decimal{in.Prices, in.Positions, steps}[field]
			value := values["XRPUSDT"]
			if edit != "extra" {
				delete(values, "XRPUSDT")
			}
			if edit != "missing" {
				values["BTCUSDT"] = value
			}
			assertConfiguredLongReason(t, symbols, in, steps)
		}
	}
}

// TestConfiguredLongLateErrors проверяет последнюю configured долю и identity ошибок.
func TestConfiguredLongLateErrors(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		symbols, in, steps := configuredLongFixture(t)
		fee := sizingDecimal(t, "0.001")
		if overflow {
			fee = model.Zero()
			factor := sizingDecimal(t, "1"+strings.Repeat("0", 100))
			for i := 0; i < 12; i++ {
				var err error
				if in.Equity, err = in.Equity.Mul(factor); err != nil {
					t.Fatal(err)
				}
			}
			in.Prices["XRPUSDT"] = sizingDecimal(t, "0."+strings.Repeat("0", 63)+"1")
		} else {
			in.Positions["XRPUSDT"] = sizingDecimal(t, "300001")
		}
		budget, budgetErr := in.Equity.Quo(sizingDecimal(t, "3"))
		for _, symbol := range symbols[:2] {
			if _, err := BaselineQuantity(budget, in.Prices[symbol], in.Positions[symbol], fee, steps[symbol]); budgetErr != nil || err != nil {
				t.Fatalf("early %s: budget=%v sizing=%v", symbol, budgetErr, err)
			}
		}
		_, want := BaselineQuantity(budget, in.Prices["XRPUSDT"], in.Positions["XRPUSDT"], fee, steps["XRPUSDT"])
		got, err := BaselineLongTargets(symbols, longSnapshot(t, in), steps, fee)
		var reason BaselineSizingReason
		if want == nil || got != nil || err != want || !errors.Is(err, want) || (!overflow && (!errors.As(err, &reason) || reason != ReasonInfeasibleReserve)) {
			t.Fatalf("overflow=%v result=%v error=%v want=%v", overflow, got, err, want)
		}
	}
}
