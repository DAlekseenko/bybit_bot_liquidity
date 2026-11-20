package backtest

import (
	"errors"
	"maps"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

var (
	// ErrBaselineFillBatch обозначает несовместимый полный пакет исполнения.
	ErrBaselineFillBatch = errors.New("invalid full baseline fill batch")
	// ErrBaselineFillResidual обозначает неполное достижение запрошенной дельты.
	ErrBaselineFillResidual = errors.New("full baseline fill batch has residual")
	// ErrBaselineFillArithmetic обозначает ошибку точного суммирования пакета.
	ErrBaselineFillArithmetic = errors.New("baseline fill batch decimal limit")
)

// BaselineFillBatchInput задаёт план, исходное состояние и уже оценённые исполнения.
type BaselineFillBatchInput struct {
	Symbols    []string
	Plan       model.TradePlan
	Rules      map[string]planner.MarketDeltaRules
	WalletUSDT model.Decimal
	Positions  map[string]model.LongPosition
	Fills      []model.ConfirmedFill
}

// BaselineFillBatchResult содержит полностью применённый локальный пакет.
type BaselineFillBatchResult struct {
	WalletUSDT      model.Decimal
	Positions       map[string]model.LongPosition
	Postings        []model.FillPosting
	RealizedPnLUSDT model.Decimal
	FeesUSDT        model.Decimal
	CashDeltaUSDT   model.Decimal
}

// ApplyFullBaselineFillBatch атомарно применяет полные дельты принятого baseline-плана.
func ApplyFullBaselineFillBatch(input BaselineFillBatchInput) (BaselineFillBatchResult, error) {
	symbols, err := config.CanonicalSymbols(input.Symbols)
	if err != nil {
		return BaselineFillBatchResult{}, ErrBaselineFillBatch
	}
	projection, err := planner.ProjectBaselineDeltas(symbols, input.Plan, input.Rules)
	if err != nil {
		return BaselineFillBatchResult{}, err
	}
	if _, err := input.WalletUSDT.Cmp(input.WalletUSDT); err != nil || len(input.Positions) != len(symbols) {
		return BaselineFillBatchResult{}, ErrBaselineFillBatch
	}
	snapshotPositions := input.Plan.Snapshot().Positions()
	for _, symbol := range symbols {
		position, found := input.Positions[symbol]
		if !found || !validBatchPosition(position) {
			return BaselineFillBatchResult{}, ErrBaselineFillBatch
		}
		if cmp, err := position.Quantity.Cmp(snapshotPositions[symbol]); err != nil || cmp != 0 {
			return BaselineFillBatchResult{}, ErrBaselineFillBatch
		}
	}
	deltas := projection.Deltas()
	for _, delta := range deltas {
		cmp, err := delta.RequestedQty.Cmp(delta.CandidateQty)
		remaining, remainingErr := delta.UnplannedQty.Cmp(model.Zero())
		if err != nil || remainingErr != nil || cmp != 0 || remaining != 0 || delta.Residual != "" {
			return BaselineFillBatchResult{}, ErrBaselineFillResidual
		}
	}
	if len(input.Fills) != len(deltas) {
		return BaselineFillBatchResult{}, ErrBaselineFillBatch
	}
	result := BaselineFillBatchResult{
		WalletUSDT: input.WalletUSDT, Positions: maps.Clone(input.Positions),
		Postings:        make([]model.FillPosting, 0, len(deltas)),
		RealizedPnLUSDT: model.Zero(), FeesUSDT: model.Zero(), CashDeltaUSDT: model.Zero(),
	}
	seen := make(map[string]bool, len(input.Fills))
	for i, delta := range deltas {
		fill := input.Fills[i]
		side := model.FillBuy
		if delta.ReduceOnly {
			side = model.FillSell
		}
		cmp, err := fill.Quantity.Cmp(delta.CandidateQty)
		if err != nil || cmp != 0 || fill.Symbol != delta.Symbol || fill.Side != side ||
			!fill.ExecutedAt.Equal(input.Plan.Metadata().ScheduledAt) || seen[fill.ExecutionID] {
			return BaselineFillBatchResult{}, ErrBaselineFillBatch
		}
		posting, err := model.ProjectLongFill(result.Positions[delta.Symbol], fill)
		if err != nil {
			return BaselineFillBatchResult{}, err
		}
		result.WalletUSDT, err = result.WalletUSDT.Add(posting.CashDeltaUSDT)
		if err != nil {
			return BaselineFillBatchResult{}, ErrBaselineFillArithmetic
		}
		result.RealizedPnLUSDT, err = result.RealizedPnLUSDT.Add(posting.RealizedPnLUSDT)
		if err != nil {
			return BaselineFillBatchResult{}, ErrBaselineFillArithmetic
		}
		result.FeesUSDT, err = result.FeesUSDT.Add(posting.Fill.FeeUSDT)
		if err != nil {
			return BaselineFillBatchResult{}, ErrBaselineFillArithmetic
		}
		result.CashDeltaUSDT, err = result.CashDeltaUSDT.Add(posting.CashDeltaUSDT)
		if err != nil {
			return BaselineFillBatchResult{}, ErrBaselineFillArithmetic
		}
		result.Positions[delta.Symbol] = posting.Position
		result.Postings = append(result.Postings, posting)
		seen[fill.ExecutionID] = true
	}
	return result, nil
}

// validBatchPosition повторяет инвариант формы LONG без оценки или I/O.
func validBatchPosition(position model.LongPosition) bool {
	quantity, err := position.Quantity.Cmp(model.Zero())
	basis, basisErr := position.CostBasisUSDT.Cmp(model.Zero())
	return err == nil && basisErr == nil && ((quantity == 0 && basis == 0) || (quantity > 0 && basis > 0))
}
