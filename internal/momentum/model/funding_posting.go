package model

import (
	"errors"
	"strings"
)

// ErrInvalidFundingAssessment обозначает некорректные данные события funding.
var ErrInvalidFundingAssessment = errors.New("invalid long funding assessment")

// ErrFundingArithmetic обозначает превышение предела точной арифметики funding.
var ErrFundingArithmetic = errors.New("funding decimal limit")

// LongFundingAssessment задаёт событие и явную цену mark в момент funding.
type LongFundingAssessment struct {
	FundingID string
	Symbol    string
	Event     FundingEvent
	MarkPrice Decimal
}

// FundingPosting содержит рассчитанный денежный поток и прежнюю позицию.
type FundingPosting struct {
	Assessment    LongFundingAssessment
	Position      LongPosition
	CashDeltaUSDT Decimal
}

// ProjectLongFunding рассчитывает одну проводку по фактической позиции в момент события.
func ProjectLongFunding(position LongPosition, assessment LongFundingAssessment) (FundingPosting, error) {
	if assessment.FundingID == "" || strings.TrimSpace(assessment.FundingID) != assessment.FundingID ||
		assessment.Symbol == "" || strings.TrimSpace(assessment.Symbol) != assessment.Symbol ||
		assessment.Event.FundingTime.IsZero() || !assessment.Event.Rate.valid() ||
		!assessment.MarkPrice.valid() || assessment.MarkPrice.value.Sign() <= 0 {
		return FundingPosting{}, ErrInvalidFundingAssessment
	}
	if !position.Quantity.valid() || !position.CostBasisUSDT.valid() {
		return FundingPosting{}, ErrInvalidLedgerPosition
	}
	sign := position.Quantity.value.Sign()
	if sign < 0 || position.CostBasisUSDT.value.Sign() != sign {
		return FundingPosting{}, ErrInvalidLedgerPosition
	}
	assessment.Event.FundingTime = assessment.Event.FundingTime.Round(0).UTC()
	if sign == 0 || assessment.Event.Rate.value.Sign() == 0 {
		return FundingPosting{Assessment: assessment, Position: position, CashDeltaUSDT: Zero()}, nil
	}
	value, err := position.Quantity.Mul(assessment.MarkPrice)
	if err != nil {
		return FundingPosting{}, ErrFundingArithmetic
	}
	fee, err := value.Mul(assessment.Event.Rate)
	if err != nil {
		return FundingPosting{}, ErrFundingArithmetic
	}
	cash, err := Zero().Sub(fee)
	if err != nil {
		return FundingPosting{}, ErrFundingArithmetic
	}
	return FundingPosting{Assessment: assessment, Position: position, CashDeltaUSDT: cash}, nil
}
