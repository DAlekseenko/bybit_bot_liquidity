package planner

import (
	"strings"
	"testing"

	"bybit_bot_liquidity/internal/momentum/model"
)

// TestBaselinePaperCloseAll игнорирует ROC и создаёт явные нулевые цели.
func TestBaselinePaperCloseAll(t *testing.T) {
	one, _ := model.ParseAmount("1")
	series, at := rocFixture(t, BaselinePaperDailyBars, []string{"11", "11", "11"})
	prices := map[string]model.Decimal{"BTCUSDT": one, "ETHUSDT": one, "HYPEUSDT": one}
	snapshot, err := model.NewPortfolioSnapshot(model.SnapshotInput{Environment: "paper", AccountID: "paper/test", ConfigVersion: "v1", CapturedAt: at, Equity: one, Prices: prices, Positions: prices})
	if err != nil {
		t.Fatal(err)
	}
	metadata := model.PlanMetadata{StrategyID: "s", RunID: "r", ProfileID: "baseline_delta_1x", ModelID: "m", FormulaID: BaselinePaperFormulaID, ConfigHash: strings.Repeat("a", 64), ScheduledAt: at, EquityCutoffAt: at, Revision: 1}
	rules := make(map[string]MarketDeltaRules)
	for symbol := range prices {
		rules[symbol] = MarketDeltaRules{QtyStep: one}
	}
	plan, err := BuildBaselinePaperPlan("close-all", metadata, snapshot, series, rules, model.Zero())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Targets()) != 3 {
		t.Fatal("missing targets")
	}
	for _, target := range plan.Targets() {
		if cmp, _ := target.Quantity.Cmp(model.Zero()); cmp != 0 {
			t.Fatal("positive close target")
		}
	}
	for _, purpose := range []string{"rebalance", "unknown"} {
		if _, err = BuildBaselinePaperPlan(purpose, metadata, snapshot, nil, rules, model.Zero()); err == nil {
			t.Fatal("missing bars/purpose accepted")
		}
	}
}
