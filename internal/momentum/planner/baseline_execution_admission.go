package planner

import (
	"errors"
	"slices"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
)

// ErrInvalidBaselineExecutionPrices обозначает некорректные карты execution price.
var ErrInvalidBaselineExecutionPrices = errors.New("invalid baseline execution prices")

// AdmitBaselineExecution применяет резерв D-01 к ценам исполнения по направлению цели.
func AdmitBaselineExecution(
	plan model.TradePlan,
	rules map[string]MarketDeltaRules,
	feeRate model.Decimal,
	buyPrices, sellPrices map[string]model.Decimal,
) error {
	positions := plan.Snapshot().Positions()
	symbols := make([]string, 0, len(positions))
	for symbol := range positions {
		symbols = append(symbols, symbol)
	}
	slices.Sort(symbols)
	canonical, err := config.CanonicalSymbols(symbols)
	if err != nil {
		return ErrInvalidDeltaPlan
	}
	if !exactExecutionPriceKeys(canonical, buyPrices) || !exactExecutionPriceKeys(canonical, sellPrices) {
		return ErrInvalidBaselineExecutionPrices
	}
	for _, symbol := range canonical {
		if err := validExecutionPrice(buyPrices[symbol]); err != nil {
			return err
		}
		if err := validExecutionPrice(sellPrices[symbol]); err != nil {
			return err
		}
	}
	if _, err := ProjectBaselineDeltas(canonical, plan, rules); err != nil {
		return err
	}
	three, err := model.ParseAmount("3")
	if err != nil {
		return err
	}
	budget, err := plan.Snapshot().Equity().Quo(three)
	if err != nil {
		return err
	}
	targets := plan.Targets()
	for _, symbol := range canonical {
		current := positions[symbol]
		target := targetQuantity(targets, symbol)
		price := buyPrices[symbol]
		if cmp, err := target.Cmp(current); err != nil {
			return ReasonInvalidBaselineInput
		} else if cmp < 0 {
			price = sellPrices[symbol]
		}
		maximum, err := BaselineQuantity(budget, price, current, feeRate, rules[symbol].QtyStep)
		if err != nil {
			return err
		}
		if cmp, err := target.Cmp(maximum); err != nil || cmp > 0 {
			return ReasonInfeasibleReserve
		}
	}
	return nil
}

// exactExecutionPriceKeys проверяет точное совпадение карт с canonical universe.
func exactExecutionPriceKeys(symbols []string, prices map[string]model.Decimal) bool {
	if len(symbols) != len(prices) {
		return false
	}
	for _, symbol := range symbols {
		if _, ok := prices[symbol]; !ok {
			return false
		}
	}
	return true
}

// validExecutionPrice требует явно заданную строго положительную точную цену.
func validExecutionPrice(price model.Decimal) error {
	if cmp, err := price.Cmp(model.Zero()); err != nil || cmp <= 0 {
		return ErrInvalidBaselineExecutionPrices
	}
	return nil
}

// targetQuantity возвращает цель символа из уже проверенного immutable плана.
func targetQuantity(targets []model.Target, symbol string) model.Decimal {
	for _, target := range targets {
		if target.Symbol == symbol {
			return target.Quantity
		}
	}
	return model.Decimal{}
}
