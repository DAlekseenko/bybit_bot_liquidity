package planner

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"bybit_bot_liquidity/internal/momentum/model"
)

// deltaTradePlan проверяет результат источника целей и формирует неизменяемый план.
func deltaTradePlan(t *testing.T, in model.SnapshotInput, targets []model.Target, profile string, targetErr error) model.TradePlan {
	t.Helper()
	m := model.PlanMetadata{StrategyID: "s", RunID: "r", ProfileID: profile, ModelID: "m", FormulaID: "D-01", ConfigHash: strings.Repeat("a", 64), ScheduledAt: in.CapturedAt, EquityCutoffAt: in.CapturedAt, Revision: 1}
	plan, err := model.NewTradePlan(model.PlanInput{Snapshot: longSnapshot(t, in), Metadata: m, Targets: targets})
	if err != nil || targetErr != nil {
		t.Fatalf("plan error=%v, target error=%v", err, targetErr)
	}
	return plan
}

// deltaRules создаёт точные правила одного market-кандидата.
func deltaRules(t *testing.T, step, min, max, notional string) MarketDeltaRules {
	t.Helper()
	return MarketDeltaRules{QtyStep: sizingDecimal(t, step), MinQty: sizingDecimal(t, min), MaxMarketQty: sizingDecimal(t, max), MinIncreaseNotional: sizingDecimal(t, notional)}
}

// requireDeltaValues сравнивает три точных количества без округления.
func requireDeltaValues(t *testing.T, d PositionDelta, raw, candidate, remaining string) {
	t.Helper()
	for i, got := range []model.Decimal{d.RequestedQty, d.CandidateQty, d.UnplannedQty} {
		if want := []string{raw, candidate, remaining}[i]; sizingRat(t, got).Cmp(sizingRat(t, sizingDecimal(t, want))) != 0 {
			t.Fatalf("component %d: got %v, want %s", i, sizingRat(t, got), want)
		}
	}
}

// TestProjectBaselineDeltas проверяет совместимость с D-01 и очередность по символам.
func TestProjectBaselineDeltas(t *testing.T) {
	symbols, in, steps := configuredLongFixture(t)
	active, rules := map[string]bool{}, map[string]MarketDeltaRules{}
	for i, symbol := range symbols {
		in.Prices[symbol], in.Positions[symbol], steps[symbol] = sizingDecimal(t, "100"), model.Zero(), sizingDecimal(t, "0.001")
		active[symbol], rules[symbol] = i != 2, deltaRules(t, "0.001", "0.001", "100", "2000")
	}
	in.Equity, in.Positions[symbols[1]] = sizingDecimal(t, "9000"), sizingDecimal(t, "40")
	targets, err := BaselineTargets(symbols, longSnapshot(t, in), active, steps, sizingDecimal(t, "0.001"))
	plan := deltaTradePlan(t, in, targets, "baseline_delta_1x", err)
	reverse := []string{symbols[2], symbols[1], symbols[0]}
	got, err := ProjectBaselineDeltas(reverse, plan, rules)
	deltas := got.Deltas()
	if err != nil || got.PlanHash() != plan.Hash() || len(deltas) != 2 || !got.RequiresReductionConfirmation() {
		t.Fatalf("unexpected projection: %#v, %v", got, err)
	}
	if deltas[0].Symbol != symbols[1] || !deltas[0].ReduceOnly || deltas[1].Symbol != symbols[0] || deltas[1].ReduceOnly {
		t.Fatalf("wrong delta order: %#v", deltas)
	}
	requireDeltaValues(t, deltas[0], "10.04", "10.04", "0")
	requireDeltaValues(t, deltas[1], "29.94", "29.94", "0")
	deltas[0] = PositionDelta{}
	again, err := ProjectBaselineDeltas(symbols, plan, rules)
	if err != nil || !reflect.DeepEqual(got, again) || !reflect.DeepEqual(reverse, []string{symbols[2], symbols[1], symbols[0]}) || !reflect.DeepEqual(plan.Targets(), targets) {
		t.Fatal("projection was not deterministic or input/getter aliased state")
	}
}

// TestDeltaResiduals проверяет точные шаги, минимумы и ограничение одного кандидата.
func TestDeltaResiduals(t *testing.T) {
	rule := deltaRules(t, "0.1", "0.2", "0.95", "5")
	for _, tc := range []struct {
		name, desired, price, candidate, residual string
		reduce                                    bool
		reason                                    DeltaResidual
	}{
		{"dust", "0.05", "100", "0", "0.05", true, DeltaResidualStep},
		{"below min qty", "0.19", "100", "0", "0.19", true, DeltaResidualMinQty},
		{"opening below notional", "0.23", "10", "0", "0.23", false, DeltaResidualMinNotional},
		{"closing exempt from notional", "0.23", "10", "0.2", "0.03", true, DeltaResidualStep},
		{"exact minima", "0.2", "25", "0.2", "0", false, ""},
		{"market cap rounded down", "2", "100", "0.9", "1.1", false, DeltaResidualMarketMax},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := calculateDelta("BTCUSDT", tc.reduce, sizingDecimal(t, tc.desired), sizingDecimal(t, tc.price), rule)
			if err != nil || got.Residual != tc.reason || got.ReduceOnly != tc.reduce {
				t.Fatalf("wrong delta: %#v, %v", got, err)
			}
			requireDeltaValues(t, got, tc.desired, tc.candidate, tc.residual)
		})
	}
}

// TestDeltaInvalidInput проверяет fail-closed при несовместимых планах и правилах.
func TestDeltaInvalidInput(t *testing.T) {
	for _, kind := range []string{"zero plan", "other profile", "missing pos", "short", "equity=0", "missing target", "wrong rules key", "zero step", "fractional step", "impossible min", "negative notional", "all unchanged", "blocked reduction still gates", "arithmetic overflow"} {
		t.Run(kind, func(t *testing.T) {
			symbols, in, _ := configuredLongFixture(t)
			targets, rules := make([]model.Target, 0, 3), map[string]MarketDeltaRules{}
			profile, want := "baseline_delta_1x", ErrInvalidDeltaPlan
			wantCount, wantGate := 0, false
			for _, symbol := range symbols {
				targets = append(targets, model.Target{Symbol: symbol, Quantity: in.Positions[symbol]})
				rules[symbol] = deltaRules(t, "0.1", "0.2", "1", "5")
			}
			switch kind {
			case "other profile":
				profile = "isolated_weekly_reset"
			case "missing pos":
				delete(in.Positions, symbols[1])
			case "short":
				in.Positions[symbols[1]] = sizingDecimal(t, "-1")
			case "equity=0":
				in.Equity = model.Zero()
			case "missing target":
				targets = targets[:2]
			case "wrong rules key":
				rules["XXXUSDT"], want = rules[symbols[1]], ErrInvalidDeltaRules
				delete(rules, symbols[1])
				targets[0].Quantity = sizingDecimal(t, "1")
			case "zero step", "fractional step", "impossible min", "negative notional":
				bad := map[string][4]string{"zero step": {"0", "0.2", "1", "5"}, "fractional step": {"1/3", "0.2", "1", "5"}, "impossible min": {"0.3", "0.31", "0.4", "5"}, "negative notional": {"0.1", "0.2", "1", "-1"}}[kind]
				rules[symbols[1]], want = deltaRules(t, bad[0], bad[1], bad[2], bad[3]), ErrInvalidDeltaRules
			case "arithmetic overflow":
				max := sizingDecimal(t, "1"+strings.Repeat("0", 100))
				for range 3 {
					max, _ = max.Mul(max)
				}
				step, _ := sizingDecimal(t, "3").Quo(max)
				rules[symbols[2]], want = MarketDeltaRules{QtyStep: step, MinQty: step, MaxMarketQty: max, MinIncreaseNotional: model.Zero()}, ErrDeltaArithmetic
			case "all unchanged":
				want = nil
			case "blocked reduction still gates":
				want, wantCount, wantGate = nil, 3, true
				in.Positions[symbols[1]] = sizingDecimal(t, "0.01")
				targets[0].Quantity, targets[1].Quantity, targets[2].Quantity = sizingDecimal(t, "1"), model.Zero(), model.Zero()
			}
			plan := deltaTradePlan(t, in, targets, profile, nil)
			if kind == "zero plan" {
				plan = model.TradePlan{}
			}
			got, err := ProjectBaselineDeltas(symbols, plan, rules)
			if !errors.Is(err, want) || len(got.Deltas()) != wantCount || got.RequiresReductionConfirmation() != wantGate || (want != nil && got.PlanHash() != "") {
				t.Fatalf("expected %v, count %d, gate %v; got %#v %v", want, wantCount, wantGate, got, err)
			}
			if wantGate && (got.Deltas()[0].Residual != DeltaResidualStep || got.Deltas()[1].Symbol != symbols[2]) {
				t.Fatal("dust skipped gate", got, err)
			}
		})
	}
}
