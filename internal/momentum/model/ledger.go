package model

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalidLedgerFill     = errors.New("invalid confirmed fill")
	ErrInvalidLedgerPosition = errors.New("invalid long ledger position")
	ErrLedgerOverclose       = errors.New("fill exceeds long position")
	ErrLedgerFillConflict    = errors.New("execution ID has different fill data")
	ErrLedgerArithmetic      = errors.New("fill ledger decimal limit")
)

// FillSide задаёт направление подтверждённого исполнения.
type FillSide string

const (
	FillBuy  FillSide = "BUY"
	FillSell FillSide = "SELL"
)

// ConfirmedFill содержит точные данные уже подтверждённого исполнения.
type ConfirmedFill struct {
	ExecutionID    string
	Symbol         string
	Side           FillSide
	ExecutedAt     time.Time
	Quantity       Decimal
	ExecutionPrice Decimal
	FeeUSDT        Decimal
}

// LongPosition хранит количество и полную оставшуюся стоимость позиции.
type LongPosition struct {
	Quantity      Decimal
	CostBasisUSDT Decimal
}

// FillPosting описывает состояние позиции после исполнения и вклад в денежный остаток.
type FillPosting struct {
	Fill            ConfirmedFill
	Position        LongPosition
	RealizedPnLUSDT Decimal
	CashDeltaUSDT   Decimal
}

// FillLedger содержит проекцию упорядоченных исполнений одного журнала.
type FillLedger struct {
	Positions       map[string]LongPosition
	Postings        []FillPosting
	RealizedPnLUSDT Decimal
	FeesUSDT        Decimal
	CashDeltaUSDT   Decimal
}

// ProjectLongFill вычисляет одну проводку из уже проверенного исполнением события.
func ProjectLongFill(before LongPosition, fill ConfirmedFill) (FillPosting, error) {
	if !validConfirmedFill(fill) {
		return FillPosting{}, ErrInvalidLedgerFill
	}
	fill.ExecutedAt = fill.ExecutedAt.Round(0).UTC()
	return projectLongFill(before, fill)
}

// ReplayFillLedger сводит события одного счёта и стратегии в порядке журнала.
func ReplayFillLedger(fills []ConfirmedFill) (FillLedger, error) {
	result := FillLedger{
		Positions:       make(map[string]LongPosition),
		RealizedPnLUSDT: Zero(),
		FeesUSDT:        Zero(),
		CashDeltaUSDT:   Zero(),
	}
	seen := make(map[string]ConfirmedFill)
	for _, fill := range fills {
		if !validConfirmedFill(fill) {
			return FillLedger{}, ErrInvalidLedgerFill
		}
		fill.ExecutedAt = fill.ExecutedAt.Round(0).UTC()
		if previous, exists := seen[fill.ExecutionID]; exists {
			if !sameConfirmedFill(previous, fill) {
				return FillLedger{}, ErrLedgerFillConflict
			}
			continue
		}

		before, ok := result.Positions[fill.Symbol]
		if !ok {
			before = LongPosition{Quantity: Zero(), CostBasisUSDT: Zero()}
		}
		posting, err := projectLongFill(before, fill)
		if err != nil {
			return FillLedger{}, err
		}
		if err := result.recordPosting(posting); err != nil {
			return FillLedger{}, err
		}
		seen[fill.ExecutionID] = fill
	}
	return result, nil
}

// projectLongFill рассчитывает результат для уже проверенных данных исполнения.
func projectLongFill(before LongPosition, fill ConfirmedFill) (FillPosting, error) {
	if !before.Quantity.valid() || !before.CostBasisUSDT.valid() {
		return FillPosting{}, ErrInvalidLedgerPosition
	}
	sign := before.Quantity.value.Sign()
	if sign < 0 || before.CostBasisUSDT.value.Sign() != sign {
		return FillPosting{}, ErrInvalidLedgerPosition
	}
	if fill.Side == FillSell && before.Quantity.value.Cmp(fill.Quantity.value) < 0 {
		return FillPosting{}, ErrLedgerOverclose
	}
	value, err := fill.Quantity.Mul(fill.ExecutionPrice)
	if err != nil {
		return FillPosting{}, ErrLedgerArithmetic
	}

	after, realized := before, Zero()
	if fill.Side == FillBuy {
		after.Quantity, err = before.Quantity.Add(fill.Quantity)
		if err != nil {
			return FillPosting{}, ErrLedgerArithmetic
		}
		after.CostBasisUSDT, err = before.CostBasisUSDT.Add(value)
		if err != nil {
			return FillPosting{}, ErrLedgerArithmetic
		}
	} else {
		after, realized, err = projectLongReduction(before, fill.Quantity, value)
	}
	if err != nil {
		return FillPosting{}, err
	}
	cash, err := realized.Sub(fill.FeeUSDT)
	if err != nil {
		return FillPosting{}, ErrLedgerArithmetic
	}
	return FillPosting{Fill: fill, Position: after, RealizedPnLUSDT: realized, CashDeltaUSDT: cash}, nil
}

// projectLongReduction распределяет прежнюю стоимость пропорционально закрытому количеству.
func projectLongReduction(before LongPosition, quantity, proceeds Decimal) (LongPosition, Decimal, error) {
	share, err := quantity.Quo(before.Quantity)
	if err != nil {
		return LongPosition{}, Decimal{}, ErrLedgerArithmetic
	}
	basisUsed, err := before.CostBasisUSDT.Mul(share)
	if err != nil {
		return LongPosition{}, Decimal{}, ErrLedgerArithmetic
	}
	remaining, err := before.Quantity.Sub(quantity)
	if err != nil {
		return LongPosition{}, Decimal{}, ErrLedgerArithmetic
	}
	basis, err := before.CostBasisUSDT.Sub(basisUsed)
	if err != nil {
		return LongPosition{}, Decimal{}, ErrLedgerArithmetic
	}
	realized, err := proceeds.Sub(basisUsed)
	if err != nil {
		return LongPosition{}, Decimal{}, ErrLedgerArithmetic
	}
	return LongPosition{Quantity: remaining, CostBasisUSDT: basis}, realized, nil
}

// recordPosting добавляет проводку и обновляет точные совокупные суммы.
func (l *FillLedger) recordPosting(posting FillPosting) error {
	realized, err := l.RealizedPnLUSDT.Add(posting.RealizedPnLUSDT)
	if err != nil {
		return ErrLedgerArithmetic
	}
	fees, err := l.FeesUSDT.Add(posting.Fill.FeeUSDT)
	if err != nil {
		return ErrLedgerArithmetic
	}
	cash, err := realized.Sub(fees)
	if err != nil {
		return ErrLedgerArithmetic
	}
	l.RealizedPnLUSDT, l.FeesUSDT, l.CashDeltaUSDT = realized, fees, cash
	l.Positions[posting.Fill.Symbol] = posting.Position
	l.Postings = append(l.Postings, posting)
	return nil
}

// validConfirmedFill проверяет идентичность, направление, время и точные суммы исполнения.
func validConfirmedFill(fill ConfirmedFill) bool {
	return fill.ExecutionID != "" && strings.TrimSpace(fill.ExecutionID) == fill.ExecutionID &&
		fill.Symbol != "" && strings.TrimSpace(fill.Symbol) == fill.Symbol && !fill.ExecutedAt.IsZero() &&
		(fill.Side == FillBuy || fill.Side == FillSell) && fill.Quantity.valid() && fill.Quantity.value.Sign() > 0 &&
		fill.ExecutionPrice.valid() && fill.ExecutionPrice.value.Sign() > 0 && fill.FeeUSDT.valid()
}

// sameConfirmedFill сравнивает установленную идентичность и все поля уже валидного события.
func sameConfirmedFill(a, b ConfirmedFill) bool {
	return a.Symbol == b.Symbol && a.Side == b.Side && a.ExecutedAt.Equal(b.ExecutedAt) &&
		a.Quantity.value.Cmp(b.Quantity.value) == 0 && a.ExecutionPrice.value.Cmp(b.ExecutionPrice.value) == 0 &&
		a.FeeUSDT.value.Cmp(b.FeeUSDT.value) == 0
}
