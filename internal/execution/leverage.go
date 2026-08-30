package execution

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"bybit_bot_liquidity/internal/exchange"
)

var (
	ErrLeverageSourceRequired = errors.New("leverage source is required")
	ErrInvalidLeverageInfo    = errors.New("invalid leverage info")
	ErrLeverageOutOfRange     = errors.New("leverage is outside instrument limits")
	ErrLeverageStepMismatch   = errors.New("leverage does not match instrument step")
)

// LeverageSource предоставляет read-only данные для проверки текущего плеча.
type LeverageSource interface {
	GetLeverage(ctx context.Context, symbol string) (exchange.LeverageInfo, error)
	GetInstrumentRules(ctx context.Context, symbol string) (exchange.InstrumentRules, error)
}

// LeveragePreflight проверяет текущее плечо без изменения настроек биржи.
type LeveragePreflight struct {
	source LeverageSource
}

// NewLeveragePreflight создаёт fail-closed проверку текущего плеча.
func NewLeveragePreflight(source LeverageSource) *LeveragePreflight {
	return &LeveragePreflight{source: source}
}

// Check сверяет текущее плечо с актуальными ограничениями linear-инструмента.
func (p *LeveragePreflight) Check(ctx context.Context, symbol string) (exchange.LeverageInfo, error) {
	if p == nil || p.source == nil {
		return exchange.LeverageInfo{}, ErrLeverageSourceRequired
	}
	if symbol == "" {
		return exchange.LeverageInfo{}, fmt.Errorf("%w: symbol is required", ErrInvalidLeverageInfo)
	}

	info, err := p.source.GetLeverage(ctx, symbol)
	if err != nil {
		return exchange.LeverageInfo{}, fmt.Errorf("get leverage: %w", err)
	}
	if info.Symbol != symbol || !positiveFinite(info.Leverage) || info.Time.IsZero() {
		return exchange.LeverageInfo{}, ErrInvalidLeverageInfo
	}

	rules, err := p.source.GetInstrumentRules(ctx, symbol)
	if err != nil {
		return exchange.LeverageInfo{}, fmt.Errorf("get instrument rules for leverage: %w", err)
	}
	if rules.Symbol != symbol {
		return exchange.LeverageInfo{}, fmt.Errorf("%w: leverage and rules symbols must match", ErrInvalidInstrumentRules)
	}
	if rules.Status != "Trading" {
		return exchange.LeverageInfo{}, ErrInstrumentNotTrading
	}
	if !positiveFinite(rules.MinLeverage) || !positiveFinite(rules.MaxLeverage) ||
		!positiveFinite(rules.LeverageStep) || rules.MinLeverage > rules.MaxLeverage {
		return exchange.LeverageInfo{}, ErrInvalidInstrumentRules
	}
	if info.Leverage < rules.MinLeverage || info.Leverage > rules.MaxLeverage {
		return exchange.LeverageInfo{}, ErrLeverageOutOfRange
	}
	if !leverageStepAligned(info.Leverage, rules.MinLeverage, rules.LeverageStep) {
		return exchange.LeverageInfo{}, ErrLeverageStepMismatch
	}

	return info, nil
}

func leverageStepAligned(value, minimum, step float64) bool {
	delta := new(big.Rat).Sub(decimalRat(value), decimalRat(minimum))
	if delta.Sign() < 0 {
		return false
	}

	units := new(big.Rat).Quo(delta, decimalRat(step))
	return units.Denom().Cmp(big.NewInt(1)) == 0
}
