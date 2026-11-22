package planner

import (
	"slices"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
)

// BaselinePaperDailyBars задаёт окно Momentum ROC30.
const BaselinePaperDailyBars = 31

// BaselinePaperFormulaID фиксирует утверждённую формулу Momentum.
const BaselinePaperFormulaID = "D-01"

// BuildBaselinePaperPlan строит цели Momentum либо явное закрытие всех LONG.
func BuildBaselinePaperPlan(purpose string, metadata model.PlanMetadata, snapshot model.PortfolioSnapshot, series map[string][]data.DailyClose, rules map[string]MarketDeltaRules, feeRate model.Decimal) (model.TradePlan, error) {
	symbols := make([]string, 0, len(rules))
	steps := make(map[string]model.Decimal, len(rules))
	for symbol, rule := range rules {
		symbols = append(symbols, symbol)
		steps[symbol] = rule.QtyStep
	}
	slices.Sort(symbols)
	targets := make([]model.Target, 0, len(symbols))
	switch purpose {
	case "close-all":
		for _, symbol := range symbols {
			targets = append(targets, model.Target{Symbol: symbol, Quantity: model.Zero()})
		}
	case "rebalance":
		for _, symbol := range symbols {
			if len(series[symbol]) != BaselinePaperDailyBars {
				return model.TradePlan{}, ReasonInvalidBaselineInput
			}
		}
		active, err := ROC30(symbols, series, metadata.ScheduledAt)
		if err != nil {
			return model.TradePlan{}, err
		}
		targets, err = BaselineTargets(symbols, snapshot, active, steps, feeRate)
		if err != nil {
			return model.TradePlan{}, err
		}
	default:
		return model.TradePlan{}, ReasonInvalidBaselineInput
	}
	return model.NewTradePlan(model.PlanInput{Snapshot: snapshot, Metadata: metadata, Targets: targets})
}
