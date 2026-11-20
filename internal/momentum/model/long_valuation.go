package model

import (
	"errors"
	"strings"
	"time"
)

var (
	// ErrInvalidLongValuation указывает на неверные входные данные переоценки.
	ErrInvalidLongValuation = errors.New("invalid long valuation")
	// ErrLongMarkStale указывает, что наблюдаемая mark цена устарела.
	ErrLongMarkStale = errors.New("long mark observation is stale")
	// ErrLongValuationArithmetic указывает на превышение предела Decimal.
	ErrLongValuationArithmetic = errors.New("long valuation decimal limit")
)

// LongMarkObservation содержит цену mark и время её наблюдения.
type LongMarkObservation struct {
	Symbol     string
	ObservedAt time.Time
	Price      Decimal
}

// LongValuation содержит точную оценку LONG позиции по mark цене.
type LongValuation struct {
	Position          LongPosition
	Mark              LongMarkObservation
	AsOf              time.Time
	NotionalUSDT      Decimal
	UnrealizedPnLUSDT Decimal
}

// ProjectLongValuation рассчитывает точный notional и нереализованный P&L позиции.
func ProjectLongValuation(position LongPosition, mark LongMarkObservation, asOf time.Time, maxAge time.Duration) (LongValuation, error) {
	if !validLongPosition(position) {
		return LongValuation{}, ErrInvalidLedgerPosition
	}
	if !validLongMark(mark) || asOf.IsZero() || maxAge <= 0 {
		return LongValuation{}, ErrInvalidLongValuation
	}
	observedAt := mark.ObservedAt.Round(0)
	asOf = asOf.Round(0)
	if observedAt.After(asOf) {
		return LongValuation{}, ErrInvalidLongValuation
	}
	if asOf.After(observedAt.Add(maxAge)) {
		return LongValuation{}, ErrLongMarkStale
	}
	mark.ObservedAt = observedAt.UTC()
	if position.Quantity.value.Sign() == 0 {
		return LongValuation{
			Position: position, Mark: mark, AsOf: asOf.UTC(),
			NotionalUSDT: Zero(), UnrealizedPnLUSDT: Zero(),
		}, nil
	}
	notional, err := position.Quantity.Mul(mark.Price)
	if err != nil {
		return LongValuation{}, ErrLongValuationArithmetic
	}
	unrealized, err := notional.Sub(position.CostBasisUSDT)
	if err != nil {
		return LongValuation{}, ErrLongValuationArithmetic
	}
	return LongValuation{
		Position: position, Mark: mark, AsOf: asOf.UTC(),
		NotionalUSDT: notional, UnrealizedPnLUSDT: unrealized,
	}, nil
}

// validLongPosition проверяет точный инвариант количества и оставшейся базы.
func validLongPosition(position LongPosition) bool {
	if !position.Quantity.valid() || !position.CostBasisUSDT.valid() {
		return false
	}
	quantitySign, basisSign := position.Quantity.value.Sign(), position.CostBasisUSDT.value.Sign()
	return (quantitySign == 0 && basisSign == 0) || (quantitySign > 0 && basisSign > 0)
}

// validLongMark проверяет символ, время наблюдения и положительную mark цену.
func validLongMark(mark LongMarkObservation) bool {
	return mark.Symbol != "" && strings.TrimSpace(mark.Symbol) == mark.Symbol &&
		!mark.ObservedAt.IsZero() && mark.Price.valid() && mark.Price.value.Sign() > 0
}
