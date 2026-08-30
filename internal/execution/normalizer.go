package execution

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"bybit_bot_liquidity/internal/exchange"
)

var (
	ErrInvalidInstrumentRules = errors.New("invalid instrument rules")
	ErrInstrumentNotTrading   = errors.New("instrument is not trading")
	ErrOrderBelowMinimum      = errors.New("order is below instrument minimum")
	ErrOrderAboveMaximum      = errors.New("order is above instrument maximum")
	ErrInvalidProtectionLevel = errors.New("invalid protection level after normalization")
)

// NormalizeOrder приводит параметры будущего market-ордера к ограничениям биржи.
func NormalizeOrder(order exchange.OrderRequest, entryPrice float64, rules exchange.InstrumentRules) (exchange.OrderRequest, error) {
	if err := validateNormalizationInput(order, entryPrice, rules); err != nil {
		return exchange.OrderRequest{}, err
	}

	normalized := order
	normalized.Qty = quantizeDown(order.Qty, rules.QtyStep)
	normalized.StopLoss = quantizeNearest(order.StopLoss, rules.TickSize)
	normalized.TakeProfit = quantizeNearest(order.TakeProfit, rules.TickSize)

	if entryPrice < rules.MinPrice {
		return exchange.OrderRequest{}, ErrOrderBelowMinimum
	}
	if entryPrice > rules.MaxPrice {
		return exchange.OrderRequest{}, ErrOrderAboveMaximum
	}
	if normalized.Qty < rules.MinOrderQty || decimalProductLess(normalized.Qty, entryPrice, rules.MinNotionalValue) {
		return exchange.OrderRequest{}, ErrOrderBelowMinimum
	}
	if normalized.Qty > rules.MaxMarketOrderQty {
		return exchange.OrderRequest{}, ErrOrderAboveMaximum
	}
	if normalized.StopLoss < rules.MinPrice || normalized.TakeProfit < rules.MinPrice {
		return exchange.OrderRequest{}, ErrOrderBelowMinimum
	}
	if normalized.StopLoss > rules.MaxPrice || normalized.TakeProfit > rules.MaxPrice {
		return exchange.OrderRequest{}, ErrOrderAboveMaximum
	}
	if !validProtectionLevels(normalized, entryPrice) {
		return exchange.OrderRequest{}, ErrInvalidProtectionLevel
	}

	return normalized, nil
}

func validateNormalizationInput(order exchange.OrderRequest, entryPrice float64, rules exchange.InstrumentRules) error {
	if order.Symbol == "" || order.Symbol != rules.Symbol {
		return fmt.Errorf("%w: order and rules symbols must match", ErrInvalidInstrumentRules)
	}
	if rules.Status != "Trading" {
		return ErrInstrumentNotTrading
	}
	if !positiveFinite(entryPrice) || !positiveFinite(order.Qty) ||
		!positiveFinite(order.StopLoss) || !positiveFinite(order.TakeProfit) {
		return fmt.Errorf("%w: order values must be positive and finite", ErrInvalidInstrumentRules)
	}
	if !positiveFinite(rules.TickSize) || !positiveFinite(rules.MinPrice) ||
		!positiveFinite(rules.MaxPrice) || !positiveFinite(rules.QtyStep) ||
		!positiveFinite(rules.MinOrderQty) || !positiveFinite(rules.MaxOrderQty) ||
		!positiveFinite(rules.MaxMarketOrderQty) ||
		!positiveFinite(rules.MinNotionalValue) ||
		rules.MinPrice > rules.MaxPrice || rules.MinOrderQty > rules.MaxOrderQty ||
		rules.MinOrderQty > rules.MaxMarketOrderQty {
		return ErrInvalidInstrumentRules
	}
	if order.Side != exchange.SideLong && order.Side != exchange.SideShort {
		return fmt.Errorf("%w: unsupported side %q", ErrInvalidInstrumentRules, order.Side)
	}
	return nil
}

func validProtectionLevels(order exchange.OrderRequest, entryPrice float64) bool {
	switch order.Side {
	case exchange.SideLong:
		return order.StopLoss < entryPrice && order.TakeProfit > entryPrice
	case exchange.SideShort:
		return order.StopLoss > entryPrice && order.TakeProfit < entryPrice
	default:
		return false
	}
}

func quantizeDown(value, step float64) float64 {
	return quantize(value, step, false)
}

func quantizeNearest(value, step float64) float64 {
	return quantize(value, step, true)
}

func quantize(value, step float64, nearest bool) float64 {
	valueRat := decimalRat(value)
	stepRat := decimalRat(step)
	quotient := new(big.Rat).Quo(valueRat, stepRat)
	units := new(big.Int).Quo(quotient.Num(), quotient.Denom())

	if nearest {
		remainder := new(big.Int).Rem(quotient.Num(), quotient.Denom())
		doubleRemainder := new(big.Int).Lsh(remainder, 1)
		if doubleRemainder.Cmp(quotient.Denom()) >= 0 {
			units.Add(units, big.NewInt(1))
		}
	}

	resultRat := new(big.Rat).Mul(new(big.Rat).SetInt(units), stepRat)
	result, _ := resultRat.Float64()
	return cleanDecimal(result, step)
}

func decimalProductLess(left, right, minimum float64) bool {
	product := new(big.Rat).Mul(decimalRat(left), decimalRat(right))
	return product.Cmp(decimalRat(minimum)) < 0
}

func decimalRat(value float64) *big.Rat {
	result, _ := new(big.Rat).SetString(strconv.FormatFloat(value, 'f', -1, 64))
	return result
}

func cleanDecimal(value, step float64) float64 {
	text := strconv.FormatFloat(step, 'f', -1, 64)
	point := strings.IndexByte(text, '.')
	if point < 0 {
		return math.Round(value)
	}
	places := len(strings.TrimRight(text[point+1:], "0"))
	scale := math.Pow10(places)
	return math.Round(value*scale) / scale
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
