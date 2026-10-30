package planner

import "bybit_bot_liquidity/internal/momentum/model"

// BaselineSizingReason обозначает устойчивую причину отказа расчёта доли.
type BaselineSizingReason string

const (
	ReasonInvalidSizingInput BaselineSizingReason = "invalid baseline sizing input"
	ReasonInfeasibleReserve  BaselineSizingReason = "baseline reserve exceeds budget"
)

// Error возвращает строковое значение причины.
func (r BaselineSizingReason) Error() string { return string(r) }

// BaselineQuantity вычисляет максимальную Qty одной доли с резервом в бюджете USDT.
func BaselineQuantity(budgetUSDT, price, currentQty, feeRate, qtyStep model.Decimal) (model.Decimal, error) {
	zero := model.Zero()
	for i, value := range []model.Decimal{budgetUSDT, price, currentQty, feeRate, qtyStep} {
		cmp, err := value.Cmp(zero)
		if err != nil || cmp < 0 || ((i == 1 || i == 4) && cmp == 0) {
			return model.Decimal{}, ReasonInvalidSizingInput
		}
	}
	if _, err := qtyStep.WireDecimal(); err != nil {
		return model.Decimal{}, ReasonInvalidSizingInput
	}
	b, err := budgetUSDT.Quo(price)
	if err != nil {
		return model.Decimal{}, err
	}
	c, err := feeRate.Mul(currentQty)
	if err != nil {
		return model.Decimal{}, err
	}
	if cmp, err := b.Cmp(c); err != nil {
		return model.Decimal{}, err
	} else if cmp < 0 {
		return model.Decimal{}, ReasonInfeasibleReserve
	}
	one, err := price.Quo(price)
	if err != nil {
		return model.Decimal{}, err
	}
	onePlusFee, err := one.Add(feeRate)
	if err != nil {
		return model.Decimal{}, err
	}
	threshold, err := onePlusFee.Mul(currentQty)
	if err != nil {
		return model.Decimal{}, err
	}
	cmp, err := b.Cmp(threshold)
	if err != nil {
		return model.Decimal{}, err
	}
	var raw model.Decimal
	if cmp < 0 {
		raw, err = b.Sub(c)
	} else {
		var numerator, denominator model.Decimal
		numerator, err = b.Add(c)
		if err == nil {
			denominator, err = onePlusFee.Add(feeRate)
		}
		if err == nil {
			raw, err = numerator.Quo(denominator)
		}
	}
	if err != nil {
		return model.Decimal{}, err
	}
	qty, err := model.FloorQty(raw, qtyStep)
	if err != nil {
		return model.Decimal{}, err
	}
	return qty, nil
}
