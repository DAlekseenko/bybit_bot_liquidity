package planner

import (
	"strconv"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
)

// ResetReason обозначает устойчивую причину отказа weekly reset.
type ResetReason string

const (
	// ReasonInvalidResetInput — несогласованные символы, цены, шаги, комиссия или плечо.
	ReasonInvalidResetInput ResetReason = "invalid weekly reset input"
	// ReasonResetNotFlat — opening plan до подтверждённого закрытия всех позиций.
	ReasonResetNotFlat ResetReason = "weekly reset opening requires a flat post-close snapshot"
	// ReasonResetNonPositiveEquity — post-close equity <= 0 запрещает входы.
	ReasonResetNonPositiveEquity ResetReason = "nonpositive weekly reset equity"
)

// Error возвращает строковое значение причины.
func (r ResetReason) Error() string { return string(r) }

// resetLeverages — плечи, разрешённые профилю isolated_weekly_reset.
var resetLeverages = map[int]bool{1: true, 2: true, 3: true, 5: true}

// ResetCloseTargets строит close-фазу reset: нулевая цель для каждого символа
// universe, даже если сигнал не изменился, — позиции закрываются всегда.
func ResetCloseTargets(symbols []string, snapshot model.PortfolioSnapshot) ([]model.Target, error) {
	canonical, err := resetUniverse(symbols, snapshot)
	if err != nil {
		return nil, err
	}
	targets := make([]model.Target, 0, len(canonical))
	for _, symbol := range canonical {
		targets = append(targets, model.Target{Symbol: symbol, Quantity: model.Zero()})
	}
	return targets, nil
}

// ResetOpeningTargets строит opening-ревизию по post-close снимку: все позиции
// нулевые, equity снимка — база после расходов закрытия (открытое D-02 задаёт
// вызывающий выбором снимка). Для активного символа:
//
//	budget   = equity / N
//	margin   = budget / (1 + 2 * leverage * feeRate)
//	notional = margin * leverage
//	qty      = floor(notional / price, qtyStep)
//
// Неактивная доля остаётся USDT (цель 0), остатки и dust не перераспределяются.
func ResetOpeningTargets(symbols []string, postClose model.PortfolioSnapshot, active map[string]bool,
	qtySteps map[string]model.Decimal, feeRate model.Decimal, leverage int) ([]model.Target, error) {
	canonical, err := resetUniverse(symbols, postClose)
	if err != nil {
		return nil, err
	}
	fee, feeErr := feeRate.Cmp(model.Zero())
	if feeErr != nil || fee < 0 || !resetLeverages[leverage] || len(active) != len(canonical) || len(qtySteps) != len(canonical) {
		return nil, ReasonInvalidResetInput
	}
	positions := postClose.Positions()
	for _, symbol := range canonical {
		if _, ok := active[symbol]; !ok {
			return nil, ReasonInvalidResetInput
		}
		if c, err := positions[symbol].Cmp(model.Zero()); err != nil || c != 0 {
			return nil, ReasonResetNotFlat
		}
	}
	if c, err := postClose.Equity().Cmp(model.Zero()); err != nil {
		return nil, ReasonInvalidResetInput
	} else if c <= 0 {
		return nil, ReasonResetNonPositiveEquity
	}
	lev, _ := model.ParseAmount(strconv.Itoa(leverage))
	count, _ := model.ParseAmount(strconv.Itoa(len(canonical)))
	budget, err := postClose.Equity().Quo(count)
	if err != nil {
		return nil, err
	}
	reserve, err := resetReserve(lev, feeRate)
	if err != nil {
		return nil, err
	}
	prices := postClose.Prices()
	targets := make([]model.Target, 0, len(canonical))
	for _, symbol := range canonical {
		qty := model.Zero()
		if active[symbol] {
			if qty, err = resetQuantity(budget, reserve, lev, prices[symbol], qtySteps[symbol]); err != nil {
				return nil, err
			}
		}
		targets = append(targets, model.Target{Symbol: symbol, Quantity: qty})
	}
	return targets, nil
}

// resetUniverse проверяет канонический universe и полноту цен и позиций снимка.
func resetUniverse(symbols []string, snapshot model.PortfolioSnapshot) ([]string, error) {
	canonical, err := config.CanonicalSymbols(symbols)
	if err != nil || snapshot.Hash() == "" {
		return nil, ReasonInvalidResetInput
	}
	prices, positions := snapshot.Prices(), snapshot.Positions()
	if len(prices) != len(canonical) || len(positions) != len(canonical) {
		return nil, ReasonInvalidResetInput
	}
	for _, symbol := range canonical {
		price, ok := prices[symbol]
		if c, err := price.Cmp(model.Zero()); !ok || err != nil || c <= 0 {
			return nil, ReasonInvalidResetInput
		}
		if _, ok := positions[symbol]; !ok {
			return nil, ReasonInvalidResetInput
		}
	}
	return canonical, nil
}

// resetReserve возвращает знаменатель 1 + 2 * leverage * feeRate.
func resetReserve(leverage, feeRate model.Decimal) (model.Decimal, error) {
	one, _ := model.ParseAmount("1")
	two, _ := model.ParseAmount("2")
	scaled, err := two.Mul(leverage)
	if err == nil {
		scaled, err = scaled.Mul(feeRate)
	}
	if err != nil {
		return model.Decimal{}, err
	}
	return one.Add(scaled)
}

// resetQuantity вычисляет Qty одной доли по формуле reset с округлением вниз.
func resetQuantity(budget, reserve, leverage, price, step model.Decimal) (model.Decimal, error) {
	if c, err := step.Cmp(model.Zero()); err != nil || c <= 0 {
		return model.Decimal{}, ReasonInvalidResetInput
	}
	if _, err := step.WireDecimal(); err != nil {
		return model.Decimal{}, ReasonInvalidResetInput
	}
	margin, err := budget.Quo(reserve)
	if err != nil {
		return model.Decimal{}, err
	}
	notional, err := margin.Mul(leverage)
	if err != nil {
		return model.Decimal{}, err
	}
	raw, err := notional.Quo(price)
	if err != nil {
		return model.Decimal{}, err
	}
	return model.FloorQty(raw, step)
}
