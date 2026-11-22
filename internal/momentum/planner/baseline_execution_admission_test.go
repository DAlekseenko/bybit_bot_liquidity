package planner

import (
	"errors"
	"maps"
	"reflect"
	"testing"

	"bybit_bot_liquidity/internal/momentum/model"
)

// TestAdmitBaselineExecution проверяет направление резерва и неравномерные цели.
func TestAdmitBaselineExecution(t *testing.T) {
	in, _ := longFixture(t)
	in.Equity = sizingDecimal(t, "300")
	in.Positions["BTCUSDT"], in.Positions["ETHUSDT"], in.Positions["HYPEUSDT"] = sizingDecimal(t, "2"), model.Zero(), sizingDecimal(t, "1")
	targets := []model.Target{{Symbol: "BTCUSDT", Quantity: sizingDecimal(t, "1")}, {Symbol: "ETHUSDT", Quantity: model.Zero()}, {Symbol: "HYPEUSDT", Quantity: sizingDecimal(t, "1")}}
	plan := deltaTradePlan(t, in, targets, "baseline_delta_1x", nil)
	rules := map[string]MarketDeltaRules{}
	buy, sell := map[string]model.Decimal{}, map[string]model.Decimal{}
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		rules[symbol] = deltaRules(t, "0.1", "0.1", "10", "0")
		buy[symbol], sell[symbol] = sizingDecimal(t, "110"), sizingDecimal(t, "90")
	}
	buy["HYPEUSDT"], sell["HYPEUSDT"] = sizingDecimal(t, "50"), sizingDecimal(t, "50")
	if err := AdmitBaselineExecution(plan, rules, sizingDecimal(t, "0.01"), buy, sell); err != nil {
		t.Fatalf("SELL reserve should use 90: %v", err)
	}
	in.Positions["BTCUSDT"], in.Positions["ETHUSDT"] = model.Zero(), model.Zero()
	plan = deltaTradePlan(t, in, []model.Target{{Symbol: "BTCUSDT", Quantity: model.Zero()}, {Symbol: "ETHUSDT", Quantity: sizingDecimal(t, "1")}, {Symbol: "HYPEUSDT", Quantity: sizingDecimal(t, "1")}}, "baseline_delta_1x", nil)
	if err := AdmitBaselineExecution(plan, rules, sizingDecimal(t, "0.01"), buy, sell); !errors.Is(err, ReasonInfeasibleReserve) {
		t.Fatalf("BUY reserve incorrectly fit: %v", err)
	}
	in.Equity = sizingDecimal(t, "9000")
	in.Positions["BTCUSDT"] = sizingDecimal(t, "1")
	noop := deltaTradePlan(t, in, []model.Target{{Symbol: "BTCUSDT", Quantity: sizingDecimal(t, "1")}, {Symbol: "ETHUSDT", Quantity: model.Zero()}, {Symbol: "HYPEUSDT", Quantity: sizingDecimal(t, "1")}}, "baseline_delta_1x", nil)
	buy["BTCUSDT"], sell["BTCUSDT"] = sizingDecimal(t, "40"), sizingDecimal(t, "4000")
	if err := AdmitBaselineExecution(noop, rules, sizingDecimal(t, "0.01"), buy, sell); err != nil {
		t.Fatalf("zero delta must use BUY convention: %v", err)
	}
}

// TestAdmitBaselineExecutionInputs проверяет price keys, цены и неизменность аргументов.
func TestAdmitBaselineExecutionInputs(t *testing.T) {
	in, _ := longFixture(t)
	plan := deltaTradePlan(t, in, []model.Target{{Symbol: "BTCUSDT", Quantity: model.Zero()}, {Symbol: "ETHUSDT", Quantity: sizingDecimal(t, "10")}, {Symbol: "HYPEUSDT", Quantity: sizingDecimal(t, "20")}}, "baseline_delta_1x", nil)
	rules, buy, sell := map[string]MarketDeltaRules{}, map[string]model.Decimal{}, map[string]model.Decimal{}
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		rules[symbol] = deltaRules(t, "0.001", "0.001", "1000", "0")
		buy[symbol], sell[symbol] = sizingDecimal(t, "100"), sizingDecimal(t, "100")
	}
	beforePlan, beforeRules, beforeBuy, beforeSell := plan.Hash(), maps.Clone(rules), maps.Clone(buy), maps.Clone(sell)
	if err := AdmitBaselineExecution(plan, rules, sizingDecimal(t, "0.001"), buy, sell); err != nil {
		t.Fatalf("admission: %v", err)
	}
	if plan.Hash() != beforePlan || !reflect.DeepEqual(rules, beforeRules) || !reflect.DeepEqual(buy, beforeBuy) || !reflect.DeepEqual(sell, beforeSell) {
		t.Fatal("input identity changed")
	}
	for _, tc := range []struct {
		name string
		sell bool
	}{
		{"missing", false}, {"foreign", false}, {"malformed", false}, {"nonpositive", false},
		{"missing", true}, {"foreign", true}, {"malformed", true}, {"nonpositive", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh := map[string]model.Decimal{}
			for k, v := range sell {
				fresh[k] = v
			}
			prices := map[string]model.Decimal{}
			for k, v := range buy {
				prices[k] = v
			}
			targetPrices := prices
			if tc.sell {
				targetPrices = fresh
			}
			switch tc.name {
			case "missing":
				delete(targetPrices, "BTCUSDT")
			case "foreign":
				targetPrices["EXTRA"] = sizingDecimal(t, "1")
			case "malformed":
				targetPrices["BTCUSDT"] = model.Decimal{}
			case "nonpositive":
				targetPrices["BTCUSDT"] = model.Zero()
			}
			buyPrices, sellPrices := prices, fresh
			if err := AdmitBaselineExecution(plan, rules, sizingDecimal(t, "0.001"), buyPrices, sellPrices); !errors.Is(err, ErrInvalidBaselineExecutionPrices) {
				t.Fatalf("price error: %v", err)
			}
		})
	}
}
