package planner

import (
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
)

// BaselinePlanReason обозначает устойчивую причину отказа полного baseline.
type BaselinePlanReason string

const (
	ReasonInvalidBaselineInput BaselinePlanReason = "invalid baseline input"
	ReasonNonPositiveEquity    BaselinePlanReason = "nonpositive baseline equity"
)

// Error возвращает строковое значение причины.
func (r BaselinePlanReason) Error() string { return string(r) }

// BaselineLongTargets вычисляет полные LONG-цели до применения сигналов.
func BaselineLongTargets(symbols []string, snapshot model.PortfolioSnapshot, qtySteps map[string]model.Decimal, feeRate model.Decimal) ([]model.Target, error) {
	canonical, err := config.CanonicalSymbols(symbols)
	if err != nil {
		return nil, ReasonInvalidBaselineInput
	}
	if snapshot.Hash() == "" {
		return nil, ReasonInvalidBaselineInput
	}
	prices, positions := snapshot.Prices(), snapshot.Positions()
	for _, values := range []map[string]model.Decimal{prices, positions, qtySteps} {
		if len(values) != len(canonical) {
			return nil, ReasonInvalidBaselineInput
		}
		for _, symbol := range canonical {
			if _, ok := values[symbol]; !ok {
				return nil, ReasonInvalidBaselineInput
			}
		}
	}
	cmp, err := snapshot.Equity().Cmp(model.Zero())
	if err != nil {
		return nil, ReasonInvalidBaselineInput
	}
	if cmp <= 0 {
		return nil, ReasonNonPositiveEquity
	}
	three, err := model.ParseAmount("3")
	if err != nil {
		return nil, err
	}
	budget, err := snapshot.Equity().Quo(three)
	if err != nil {
		return nil, err
	}
	targets := make([]model.Target, 0, len(canonical))
	for _, symbol := range canonical {
		qty, err := BaselineQuantity(budget, prices[symbol], positions[symbol], feeRate, qtySteps[symbol])
		if err != nil {
			return nil, err
		}
		targets = append(targets, model.Target{Symbol: symbol, Quantity: qty})
	}
	return targets, nil
}
