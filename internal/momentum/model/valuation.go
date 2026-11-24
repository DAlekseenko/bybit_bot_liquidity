package model

import (
	"errors"
	"math/big"
	"strings"
	"time"
)

var (
	// ErrInvalidAccountValuation указывает на неверные входные данные оценки счёта.
	ErrInvalidAccountValuation = errors.New("invalid account valuation")
	// ErrAccountMarkMissing указывает, что для открытой позиции нет mark наблюдения.
	ErrAccountMarkMissing = errors.New("account mark observation is missing")
	// ErrAccountMarkStale указывает, что оценка опирается на устаревшую mark цену.
	ErrAccountMarkStale = errors.New("account mark observation is stale")
	// ErrAccountEquityNotPositive указывает, что equity не положителен и sizing запрещён.
	ErrAccountEquityNotPositive = errors.New("account equity is not positive")
	// ErrAccountValuationArithmetic указывает на превышение предела Decimal.
	ErrAccountValuationArithmetic = errors.New("account valuation decimal limit")
)

// AccountPositionInput задаёт LONG позицию символа и её isolated leverage.
type AccountPositionInput struct {
	Symbol   string
	Position LongPosition
	Leverage Decimal
}

// AccountValuationInput содержит wallet, позиции и mark наблюдения на момент AsOf.
//
// WalletUSDT уже включает начальный капитал, реализованный P&L, комиссии,
// funding и явные переводы; открытие позиции wallet не уменьшает.
type AccountValuationInput struct {
	WalletUSDT Decimal
	Positions  []AccountPositionInput
	Marks      []LongMarkObservation
	AsOf       time.Time
	MaxAge     time.Duration
}

// AccountPositionValuation содержит оценку одной позиции и её margin.
type AccountPositionValuation struct {
	Symbol            string
	Position          LongPosition
	Leverage          Decimal
	Mark              LongMarkObservation
	Fresh             bool
	NotionalUSDT      Decimal
	UnrealizedPnLUSDT Decimal
	MarginUSDT        Decimal
}

// AccountValuation содержит точную оценку счёта по mark snapshot.
//
// EquityUSDT = WalletUSDT + UnrealizedPnLUSDT; notional в equity не входит.
// UsedMarginUSDT = Σ CostBasisUSDT / Leverage (isolated margin по цене входа),
// FreeMarginUSDT = EquityUSDT − UsedMarginUSDT; locked margin не расход и не
// прибавляется повторно. Fresh ложен, если хоть одна mark цена устарела.
type AccountValuation struct {
	AsOf              time.Time
	WalletUSDT        Decimal
	UnrealizedPnLUSDT Decimal
	EquityUSDT        Decimal
	UsedMarginUSDT    Decimal
	FreeMarginUSDT    Decimal
	Positions         []AccountPositionValuation
	Fresh             bool
	SizingAllowed     bool
}

// SizingEquity возвращает equity для sizing или причину, по которой sizing запрещён.
func (v AccountValuation) SizingEquity() (Decimal, error) {
	if !v.Fresh {
		return Decimal{}, ErrAccountMarkStale
	}
	if !v.EquityUSDT.valid() || v.EquityUSDT.value.Sign() <= 0 {
		return Decimal{}, ErrAccountEquityNotPositive
	}
	return v.EquityUSDT, nil
}

// ProjectAccountValuation рассчитывает wallet, UPL, equity и margin без I/O.
func ProjectAccountValuation(input AccountValuationInput) (AccountValuation, error) {
	if !input.WalletUSDT.valid() || input.AsOf.IsZero() || input.MaxAge <= 0 {
		return AccountValuation{}, ErrInvalidAccountValuation
	}
	asOf := input.AsOf.Round(0).UTC()
	marks, err := indexAccountMarks(input.Marks, asOf)
	if err != nil {
		return AccountValuation{}, err
	}
	result := AccountValuation{
		AsOf: asOf, WalletUSDT: input.WalletUSDT, UnrealizedPnLUSDT: Zero(),
		UsedMarginUSDT: Zero(), Positions: make([]AccountPositionValuation, 0, len(input.Positions)),
		Fresh: true,
	}
	seen := make(map[string]struct{}, len(input.Positions))
	for _, item := range input.Positions {
		if _, dup := seen[item.Symbol]; dup {
			return AccountValuation{}, ErrInvalidAccountValuation
		}
		seen[item.Symbol] = struct{}{}
		valued, err := valueAccountPosition(item, marks, asOf, input.MaxAge)
		if err != nil {
			return AccountValuation{}, err
		}
		if result.UnrealizedPnLUSDT, err = result.UnrealizedPnLUSDT.Add(valued.UnrealizedPnLUSDT); err != nil {
			return AccountValuation{}, ErrAccountValuationArithmetic
		}
		if result.UsedMarginUSDT, err = result.UsedMarginUSDT.Add(valued.MarginUSDT); err != nil {
			return AccountValuation{}, ErrAccountValuationArithmetic
		}
		result.Fresh = result.Fresh && valued.Fresh
		result.Positions = append(result.Positions, valued)
	}
	if result.EquityUSDT, err = result.WalletUSDT.Add(result.UnrealizedPnLUSDT); err != nil {
		return AccountValuation{}, ErrAccountValuationArithmetic
	}
	if result.FreeMarginUSDT, err = result.EquityUSDT.Sub(result.UsedMarginUSDT); err != nil {
		return AccountValuation{}, ErrAccountValuationArithmetic
	}
	result.SizingAllowed = result.Fresh && result.EquityUSDT.value.Sign() > 0
	return result, nil
}

// indexAccountMarks проверяет наблюдения и индексирует их по символу.
func indexAccountMarks(observations []LongMarkObservation, asOf time.Time) (map[string]LongMarkObservation, error) {
	marks := make(map[string]LongMarkObservation, len(observations))
	for _, mark := range observations {
		if !validLongMark(mark) {
			return nil, ErrInvalidAccountValuation
		}
		mark.ObservedAt = mark.ObservedAt.Round(0).UTC()
		if mark.ObservedAt.After(asOf) {
			return nil, ErrInvalidAccountValuation
		}
		if _, dup := marks[mark.Symbol]; dup {
			return nil, ErrInvalidAccountValuation
		}
		marks[mark.Symbol] = mark
	}
	return marks, nil
}

// valueAccountPosition оценивает одну позицию по mark и считает isolated margin.
func valueAccountPosition(item AccountPositionInput, marks map[string]LongMarkObservation, asOf time.Time, maxAge time.Duration) (AccountPositionValuation, error) {
	if item.Symbol == "" || strings.TrimSpace(item.Symbol) != item.Symbol || !validLongPosition(item.Position) ||
		!item.Leverage.valid() || item.Leverage.value.Cmp(big.NewRat(1, 1)) < 0 {
		return AccountPositionValuation{}, ErrInvalidAccountValuation
	}
	valued := AccountPositionValuation{
		Symbol: item.Symbol, Position: item.Position, Leverage: item.Leverage, Fresh: true,
		NotionalUSDT: Zero(), UnrealizedPnLUSDT: Zero(), MarginUSDT: Zero(),
	}
	if item.Position.Quantity.value.Sign() == 0 {
		return valued, nil
	}
	mark, ok := marks[item.Symbol]
	if !ok {
		return AccountPositionValuation{}, ErrAccountMarkMissing
	}
	valued.Mark = mark
	valued.Fresh = !asOf.After(mark.ObservedAt.Add(maxAge))
	var err error
	if valued.NotionalUSDT, err = item.Position.Quantity.Mul(mark.Price); err != nil {
		return AccountPositionValuation{}, ErrAccountValuationArithmetic
	}
	if valued.UnrealizedPnLUSDT, err = valued.NotionalUSDT.Sub(item.Position.CostBasisUSDT); err != nil {
		return AccountPositionValuation{}, ErrAccountValuationArithmetic
	}
	if valued.MarginUSDT, err = item.Position.CostBasisUSDT.Quo(item.Leverage); err != nil {
		return AccountPositionValuation{}, ErrAccountValuationArithmetic
	}
	return valued, nil
}
