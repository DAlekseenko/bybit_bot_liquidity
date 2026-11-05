package planner

import (
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
)

// BaselineTargets применяет active-маску к полным LONG-целям выбранного universe.
func BaselineTargets(symbols []string, snapshot model.PortfolioSnapshot, active map[string]bool, qtySteps map[string]model.Decimal, feeRate model.Decimal) ([]model.Target, error) {
	canonical, err := config.CanonicalSymbols(symbols)
	if err != nil || len(active) != len(canonical) {
		return nil, ReasonInvalidBaselineInput
	}
	for _, symbol := range canonical {
		if _, ok := active[symbol]; !ok {
			return nil, ReasonInvalidBaselineInput
		}
	}
	targets, err := BaselineLongTargets(symbols, snapshot, qtySteps, feeRate)
	if err != nil {
		return nil, err
	}
	for i := range targets {
		if !active[targets[i].Symbol] {
			targets[i].Quantity = model.Zero()
		}
	}
	return targets, nil
}
