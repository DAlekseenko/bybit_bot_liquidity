package backtest

import (
	"errors"
	"fmt"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// ErrZeroSlipBaselineApply обозначает отказ нулевого проскальзывания и полного пакета.
var (
	ErrZeroSlipBaselineApply = errors.New("invalid zero-slip baseline application")
	// ErrZeroSlipBaselineReserve обозначает отказ D-01 по цене исполнения.
	ErrZeroSlipBaselineReserve = errors.New("zero-slip D-01 execution reserve rejected")
)

// ApplyZeroSlipBaselineCycle проверяет резерв и применяет фиксированные цели по next-open.
func ApplyZeroSlipBaselineCycle(input BaselineCycleInput, prepared PreparedBaselineCycle, nextOpens []NextOpenReference) (BaselineFillBatchResult, error) {
	cfg := input.Config.Values()
	parameters := input.Model.Parameters()
	_, modelHash, err := input.Model.Snapshot()
	if err != nil {
		return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, err)
	}
	bound, err := NewSimulationModel(input.Config, input.Dataset.Manifest, parameters)
	if err != nil {
		return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, err)
	}
	_, boundHash, err := bound.Snapshot()
	if err != nil || boundHash != modelHash || !validBaselineCycleTime(input, cfg.StartPolicy) {
		return BaselineFillBatchResult{}, ErrZeroSlipBaselineApply
	}
	slip, err := parameters.SlippageRate.Cmp(model.Zero())
	if err != nil || slip != 0 {
		return BaselineFillBatchResult{}, ErrZeroSlipBaselineApply
	}
	_, configHash, err := input.Config.Snapshot()
	if err != nil {
		return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, err)
	}
	runID := baselineIdentity(baselineReplayIdentity{
		Format: "momentum/baseline-replay/v1", ConfigSHA256: configHash,
		DatasetSHA256: cfg.DataSource.ManifestSHA256, ModelSHA256: modelHash,
		Start: input.Start.Round(0).UTC().Format(time.RFC3339Nano),
		End:   input.End.Round(0).UTC().Format(time.RFC3339Nano),
	})
	cycleID := baselineIdentity([]string{"momentum/baseline-cycle/v1", runID, input.Kind, input.At.Round(0).UTC().Format(time.RFC3339Nano)})
	meta := prepared.Plan.Metadata()
	expected := model.PlanMetadata{
		StrategyID: cfg.StrategyID, RunID: cycleID, ProfileID: cfg.Profile, ModelID: cfg.ModelID,
		FormulaID: "D-01", ConfigHash: configHash, ScheduledAt: input.At.Round(0).UTC(),
		EquityCutoffAt: input.At.Round(0).UTC(), Revision: 1,
	}
	if prepared.RunID != runID || prepared.CycleID != cycleID || meta != expected ||
		!prepared.Cutoff.AsOf.Equal(input.At) ||
		!sameApplyDecimal(prepared.Plan.Snapshot().Equity(), prepared.Cutoff.EquityUSDT) ||
		!prepared.Plan.Snapshot().CapturedAt().Equal(input.At) ||
		prepared.Projection.PlanHash() != prepared.Plan.Hash() {
		return BaselineFillBatchResult{}, ErrZeroSlipBaselineApply
	}
	verified, err := ProjectAccountingStep(AccountingInput{
		Symbols: cfg.Symbols, AsOf: input.At, MarkMaxAge: parameters.MarkMaxAge,
		WalletUSDT: input.WalletUSDT, Positions: input.Positions, Marks: input.Marks, Funding: input.Funding,
	})
	if err != nil || !sameApplyAccounting(verified, prepared.Cutoff) {
		return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, err)
	}
	planPrices := prepared.Plan.Snapshot().Prices()
	if len(input.Positions) != len(cfg.Symbols) || len(prepared.Cutoff.Valuations) != len(cfg.Symbols) {
		return BaselineFillBatchResult{}, ErrZeroSlipBaselineApply
	}
	for i, symbol := range cfg.Symbols {
		old, exists := input.Positions[symbol]
		valuation := prepared.Cutoff.Valuations[i]
		if !exists || valuation.Mark.Symbol != symbol ||
			!sameApplyDecimal(planPrices[symbol], valuation.Mark.Price) ||
			!sameApplyDecimal(old.Quantity, valuation.Position.Quantity) ||
			!sameApplyDecimal(old.CostBasisUSDT, valuation.Position.CostBasisUSDT) {
			return BaselineFillBatchResult{}, ErrZeroSlipBaselineApply
		}
	}
	prices := make(map[string]model.Decimal, len(nextOpens))
	for _, reference := range nextOpens {
		if _, found := prices[reference.Symbol]; found {
			return BaselineFillBatchResult{}, ErrZeroSlipBaselineApply
		}
		wire, wireErr := reference.Price.WireDecimal()
		if _, priceErr := model.ParsePrice(wire); wireErr != nil || priceErr != nil {
			return BaselineFillBatchResult{}, ErrZeroSlipBaselineApply
		}
		prices[reference.Symbol] = reference.Price
	}
	if len(prices) != len(cfg.Symbols) {
		return BaselineFillBatchResult{}, ErrZeroSlipBaselineApply
	}
	three, err := model.ParseRate("3")
	if err != nil {
		return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, err)
	}
	budget, err := prepared.Cutoff.EquityUSDT.Quo(three)
	if err != nil {
		return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, err)
	}
	for _, target := range prepared.Plan.Targets() {
		price, exists := prices[target.Symbol]
		if !exists {
			return BaselineFillBatchResult{}, ErrZeroSlipBaselineApply
		}
		maximum, err := planner.BaselineQuantity(budget, price, input.Positions[target.Symbol].Quantity,
			parameters.FeeRate, parameters.Rules[target.Symbol].QtyStep)
		if err != nil {
			return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, ErrZeroSlipBaselineReserve,
				fmt.Errorf("symbol %s at %s: %w", target.Symbol, input.At.UTC().Format(time.RFC3339Nano), err))
		}
		comparison, err := target.Quantity.Cmp(maximum)
		if err != nil || comparison > 0 {
			return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, ErrZeroSlipBaselineReserve,
				fmt.Errorf("symbol %s at %s", target.Symbol, input.At.UTC().Format(time.RFC3339Nano)))
		}
	}
	projection, err := planner.ProjectBaselineDeltas(cfg.Symbols, prepared.Plan, parameters.Rules)
	if err != nil {
		return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, err)
	}
	deltas := projection.Deltas()
	fills := make([]model.ConfirmedFill, 0, len(deltas))
	for _, delta := range deltas {
		if delta.Residual != "" || !sameApplyDecimal(delta.CandidateQty, delta.RequestedQty) ||
			!sameApplyDecimal(delta.UnplannedQty, model.Zero()) {
			return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, ErrBaselineFillResidual)
		}
		price := prices[delta.Symbol]
		notional, err := delta.CandidateQty.Mul(price)
		if err != nil {
			return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, err)
		}
		fee, err := notional.Mul(parameters.FeeRate)
		if err != nil {
			return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, err)
		}
		phase, side := "increase", model.FillBuy
		if delta.ReduceOnly {
			phase, side = "reduce", model.FillSell
		}
		fills = append(fills, model.ConfirmedFill{
			ExecutionID: baselineIdentity([]string{"momentum/baseline-zero-slip-fill/v1", runID, cycleID, prepared.Plan.Hash(), phase, delta.Symbol}),
			Symbol:      delta.Symbol, Side: side, Quantity: delta.CandidateQty, ExecutionPrice: price,
			FeeUSDT: fee, ExecutedAt: meta.ScheduledAt,
		})
	}
	result, err := ApplyFullBaselineFillBatch(BaselineFillBatchInput{
		Symbols: cfg.Symbols, Plan: prepared.Plan, Rules: parameters.Rules,
		WalletUSDT: prepared.Cutoff.WalletUSDT, Positions: input.Positions, Fills: fills,
	})
	if err != nil {
		return BaselineFillBatchResult{}, errors.Join(ErrZeroSlipBaselineApply, err)
	}
	return result, nil
}

// sameApplyDecimal сравнивает точные числа с безопасным отказом.
func sameApplyDecimal(left, right model.Decimal) bool {
	comparison, err := left.Cmp(right)
	return err == nil && comparison == 0
}

// sameApplyAccounting сравнивает полную проекцию без повторного начисления или округления.
func sameApplyAccounting(a, b AccountingResult) bool {
	if !a.AsOf.Equal(b.AsOf) || len(a.FundingPostings) != len(b.FundingPostings) || len(a.Valuations) != len(b.Valuations) {
		return false
	}
	pairs := [][2]model.Decimal{
		{a.WalletUSDT, b.WalletUSDT},
		{a.FundingCashDeltaUSDT, b.FundingCashDeltaUSDT},
		{a.UnrealizedPnLUSDT, b.UnrealizedPnLUSDT},
		{a.EquityUSDT, b.EquityUSDT},
	}
	for i, p := range a.FundingPostings {
		q := b.FundingPostings[i]
		if p.Assessment.FundingID != q.Assessment.FundingID || p.Assessment.Symbol != q.Assessment.Symbol || !p.Assessment.Event.FundingTime.Equal(q.Assessment.Event.FundingTime) {
			return false
		}
		pairs = append(pairs,
			[2]model.Decimal{p.Assessment.Event.Rate, q.Assessment.Event.Rate},
			[2]model.Decimal{p.Assessment.MarkPrice, q.Assessment.MarkPrice},
			[2]model.Decimal{p.Position.Quantity, q.Position.Quantity},
			[2]model.Decimal{p.Position.CostBasisUSDT, q.Position.CostBasisUSDT},
			[2]model.Decimal{p.CashDeltaUSDT, q.CashDeltaUSDT},
		)
	}
	for i, p := range a.Valuations {
		q := b.Valuations[i]
		if p.Mark.Symbol != q.Mark.Symbol || !p.AsOf.Equal(q.AsOf) || !p.Mark.ObservedAt.Equal(q.Mark.ObservedAt) {
			return false
		}
		pairs = append(pairs,
			[2]model.Decimal{p.Position.Quantity, q.Position.Quantity},
			[2]model.Decimal{p.Position.CostBasisUSDT, q.Position.CostBasisUSDT},
			[2]model.Decimal{p.Mark.Price, q.Mark.Price},
			[2]model.Decimal{p.NotionalUSDT, q.NotionalUSDT},
			[2]model.Decimal{p.UnrealizedPnLUSDT, q.UnrealizedPnLUSDT},
		)
	}
	for _, pair := range pairs {
		if !sameApplyDecimal(pair[0], pair[1]) {
			return false
		}
	}
	return true
}
