package planner

import "bybit_bot_liquidity/internal/momentum/model"

// BaselineTargets применяет active-маску к полным LONG-целям.
func BaselineTargets(snapshot model.PortfolioSnapshot, active map[string]bool, qtySteps map[string]model.Decimal, feeRate model.Decimal) ([]model.Target, error) {
	symbols := [...]string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}
	if len(active) != len(symbols) {
		return nil, ReasonInvalidBaselineInput
	}
	for _, symbol := range symbols {
		if _, ok := active[symbol]; !ok {
			return nil, ReasonInvalidBaselineInput
		}
	}
	targets, err := BaselineLongTargets(snapshot, qtySteps, feeRate)
	if err != nil {
		return nil, err
	}
	for i, symbol := range symbols {
		if !active[symbol] {
			targets[i].Quantity = model.Zero()
		}
	}
	return targets, nil
}
