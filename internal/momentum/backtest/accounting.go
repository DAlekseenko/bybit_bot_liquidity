// Package backtest содержит чистые проекции бухгалтерского состояния backtest.
package backtest

import (
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	// ErrInvalidAccountingInput обозначает неверную форму или время шага учёта.
	ErrInvalidAccountingInput = errors.New("invalid accounting input")
	// ErrAccountingArithmetic обозначает ошибку сложения итогов учёта.
	ErrAccountingArithmetic = errors.New("accounting decimal limit")
)

// AccountingInput содержит полный снимок портфеля до ребалансировки.
type AccountingInput struct {
	Symbols    []string
	AsOf       time.Time
	MarkMaxAge time.Duration
	WalletUSDT model.Decimal
	Positions  map[string]model.LongPosition
	Marks      map[string]model.LongMarkObservation
	Funding    []model.LongFundingAssessment
}

// AccountingResult содержит проводки funding и оценку исходных позиций.
type AccountingResult struct {
	AsOf                 time.Time
	WalletUSDT           model.Decimal
	FundingCashDeltaUSDT model.Decimal
	UnrealizedPnLUSDT    model.Decimal
	EquityUSDT           model.Decimal
	FundingPostings      []model.FundingPosting
	Valuations           []model.LongValuation
}

// ProjectAccountingStep сначала применяет funding к старым позициям, затем считает equity.
func ProjectAccountingStep(input AccountingInput) (AccountingResult, error) {
	symbols, err := config.CanonicalSymbols(input.Symbols)
	asOf := input.AsOf.Round(0)
	if err != nil || input.AsOf.IsZero() || input.MarkMaxAge <= 0 || !validAccountingDecimal(input.WalletUSDT) ||
		!exactAccountingKeys(input.Positions, symbols) || !exactAccountingKeys(input.Marks, symbols) {
		return AccountingResult{}, ErrInvalidAccountingInput
	}
	fundingBySymbol := make(map[string]model.LongFundingAssessment, len(input.Funding))
	ids := make(map[string]struct{}, len(input.Funding))
	for _, assessment := range input.Funding {
		if !containsAccountingSymbol(symbols, assessment.Symbol) || !assessment.Event.FundingTime.Equal(asOf) {
			return AccountingResult{}, ErrInvalidAccountingInput
		}
		if _, ok := ids[assessment.FundingID]; ok {
			return AccountingResult{}, ErrInvalidAccountingInput
		}
		if _, ok := fundingBySymbol[assessment.Symbol]; ok {
			return AccountingResult{}, ErrInvalidAccountingInput
		}
		ids[assessment.FundingID], fundingBySymbol[assessment.Symbol] = struct{}{}, assessment
	}
	result := AccountingResult{AsOf: asOf.UTC(), WalletUSDT: input.WalletUSDT, FundingCashDeltaUSDT: model.Zero(), UnrealizedPnLUSDT: model.Zero()}
	for _, symbol := range symbols {
		mark := input.Marks[symbol]
		if mark.Symbol != symbol {
			return AccountingResult{}, ErrInvalidAccountingInput
		}
		if assessment, ok := fundingBySymbol[symbol]; ok {
			posting, err := model.ProjectLongFunding(input.Positions[symbol], assessment)
			if err != nil {
				return AccountingResult{}, err
			}
			result.FundingPostings = append(result.FundingPostings, posting)
			result.FundingCashDeltaUSDT, err = result.FundingCashDeltaUSDT.Add(posting.CashDeltaUSDT)
			if err != nil {
				return AccountingResult{}, ErrAccountingArithmetic
			}
		}
	}
	result.WalletUSDT, err = input.WalletUSDT.Add(result.FundingCashDeltaUSDT)
	if err != nil {
		return AccountingResult{}, ErrAccountingArithmetic
	}
	for _, symbol := range symbols {
		valuation, err := model.ProjectLongValuation(input.Positions[symbol], input.Marks[symbol], asOf, input.MarkMaxAge)
		if err != nil {
			return AccountingResult{}, err
		}
		result.Valuations = append(result.Valuations, valuation)
		result.UnrealizedPnLUSDT, err = result.UnrealizedPnLUSDT.Add(valuation.UnrealizedPnLUSDT)
		if err != nil {
			return AccountingResult{}, ErrAccountingArithmetic
		}
	}
	result.EquityUSDT, err = result.WalletUSDT.Add(result.UnrealizedPnLUSDT)
	if err != nil {
		return AccountingResult{}, ErrAccountingArithmetic
	}
	return result, nil
}

// validAccountingDecimal проверяет точное число без доступа к внутреннему представлению Decimal.
func validAccountingDecimal(value model.Decimal) bool { _, err := value.Cmp(value); return err == nil }

// exactAccountingKeys проверяет совпадение ключей map с каноническим universe.
func exactAccountingKeys[V any](values map[string]V, symbols []string) bool {
	if len(values) != len(symbols) {
		return false
	}
	for _, symbol := range symbols {
		if _, ok := values[symbol]; !ok {
			return false
		}
	}
	return true
}

// containsAccountingSymbol проверяет принадлежность символа явному universe.
func containsAccountingSymbol(symbols []string, candidate string) bool {
	for _, symbol := range symbols {
		if symbol == candidate {
			return true
		}
	}
	return false
}
