package planner

import (
	"errors"
	"slices"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	// ErrInvalidDeltaPlan обозначает несовместимый или неполный исходный план.
	ErrInvalidDeltaPlan = errors.New("invalid baseline delta plan")
	// ErrInvalidDeltaRules обозначает отсутствующие или некорректные правила рынка.
	ErrInvalidDeltaRules = errors.New("invalid market delta rules")
	// ErrDeltaArithmetic обозначает ошибку точной десятичной арифметики.
	ErrDeltaArithmetic = errors.New("baseline delta arithmetic")
)

// MarketDeltaRules задаёт точные ограничения market-кандидата по одному символу.
type MarketDeltaRules struct {
	QtyStep, MinQty, MaxMarketQty, MinIncreaseNotional model.Decimal
}

// DeltaResidual обозначает причину непокрытого количества.
type DeltaResidual string

const (
	DeltaResidualStep        DeltaResidual = "qty_step"
	DeltaResidualMinQty      DeltaResidual = "min_qty"
	DeltaResidualMinNotional DeltaResidual = "min_notional"
	DeltaResidualMarketMax   DeltaResidual = "market_max"
)

// PositionDelta описывает требуемое и предварительно достижимое изменение позиции.
type PositionDelta struct {
	Symbol                                   string
	ReduceOnly                               bool
	RequestedQty, CandidateQty, UnplannedQty model.Decimal
	Residual                                 DeltaResidual
}

// DeltaProjection хранит неизменяемый результат расчёта по хешу TradePlan.
type DeltaProjection struct {
	planHash string
	deltas   []PositionDelta
}

// PlanHash возвращает идентичность исходного TradePlan.
func (p DeltaProjection) PlanHash() string { return p.planHash }

// Deltas возвращает отдельную копию дельт в порядке сокращения и увеличения.
func (p DeltaProjection) Deltas() []PositionDelta { return slices.Clone(p.deltas) }

// RequiresReductionConfirmation показывает наличие требуемых сокращений, включая dust.
func (p DeltaProjection) RequiresReductionConfirmation() bool {
	return len(p.deltas) > 0 && p.deltas[0].ReduceOnly
}

// ProjectBaselineDeltas строит расчётную проекцию без изменения TradePlan.
func ProjectBaselineDeltas(symbols []string, plan model.TradePlan, rules map[string]MarketDeltaRules) (DeltaProjection, error) {
	canonical, err := config.CanonicalSymbols(symbols)
	meta := plan.Metadata()
	if err != nil || plan.Hash() == "" || meta.ProfileID != "baseline_delta_1x" || meta.FormulaID != "D-01" {
		return DeltaProjection{}, ErrInvalidDeltaPlan
	}
	snapshot, targets := plan.Snapshot(), plan.Targets()
	prices, positions := snapshot.Prices(), snapshot.Positions()
	if len(prices) != len(canonical) || len(positions) != len(canonical) || len(targets) != len(canonical) {
		return DeltaProjection{}, ErrInvalidDeltaPlan
	}
	positive, err := snapshot.Equity().Cmp(model.Zero())
	if err != nil || positive <= 0 {
		return DeltaProjection{}, ErrInvalidDeltaPlan
	}
	if len(rules) != len(canonical) {
		return DeltaProjection{}, ErrInvalidDeltaRules
	}
	reductions := make([]PositionDelta, 0, len(canonical))
	increases := make([]PositionDelta, 0, len(canonical))
	for i, symbol := range canonical {
		current, positionKnown := positions[symbol]
		price, priceKnown := prices[symbol]
		currentSign, err := current.Cmp(model.Zero())
		if !positionKnown || !priceKnown || currentSign < 0 || err != nil || targets[i].Symbol != symbol {
			return DeltaProjection{}, ErrInvalidDeltaPlan
		}
		rule, found := rules[symbol]
		if !found {
			return DeltaProjection{}, ErrInvalidDeltaRules
		}
		if err := validateMarketDeltaRules(rule); err != nil {
			return DeltaProjection{}, err
		}
		target := targets[i].Quantity
		change, err := target.Cmp(current)
		if err != nil {
			return DeltaProjection{}, ErrInvalidDeltaPlan
		}
		if change == 0 {
			continue
		}
		if change < 0 {
			current, target = target, current
		}
		requested, err := target.Sub(current)
		if err != nil {
			return DeltaProjection{}, ErrDeltaArithmetic
		}
		delta, err := calculateDelta(symbol, change < 0, requested, price, rule)
		if err != nil {
			return DeltaProjection{}, ErrDeltaArithmetic
		}
		if delta.ReduceOnly {
			reductions = append(reductions, delta)
		} else {
			increases = append(increases, delta)
		}
	}
	return DeltaProjection{planHash: plan.Hash(), deltas: append(reductions, increases...)}, nil
}

// validateMarketDeltaRules проверяет правила и сохраняет причину численной ошибки.
func validateMarketDeltaRules(r MarketDeltaRules) error {
	for i, value := range []model.Decimal{r.QtyStep, r.MinQty, r.MaxMarketQty, r.MinIncreaseNotional} {
		sign, err := value.Cmp(model.Zero())
		if err != nil || sign < 0 || (sign == 0 && i != 3) {
			return ErrInvalidDeltaRules
		}
		if _, err := value.WireDecimal(); err != nil {
			return ErrInvalidDeltaRules
		}
	}
	maxAligned, err := model.FloorQty(r.MaxMarketQty, r.QtyStep)
	if err != nil {
		return ErrDeltaArithmetic
	}
	feasible, err := maxAligned.Cmp(r.MinQty)
	if err != nil {
		return ErrDeltaArithmetic
	}
	if feasible < 0 {
		return ErrInvalidDeltaRules
	}
	return nil
}

// calculateDelta ограничивает кандидата и отмечает точный непокрытый остаток.
func calculateDelta(symbol string, reduce bool, requested, price model.Decimal, rule MarketDeltaRules) (PositionDelta, error) {
	bounded, reason := requested, DeltaResidual("")
	if cmp, _ := requested.Cmp(rule.MaxMarketQty); cmp > 0 {
		bounded, reason = rule.MaxMarketQty, DeltaResidualMarketMax
	}
	candidate, err := model.FloorQty(bounded, rule.QtyStep)
	if err != nil {
		return PositionDelta{}, err
	}
	if n, _ := candidate.Cmp(model.Zero()); n == 0 {
		reason = DeltaResidualStep
	} else if n, _ := candidate.Cmp(rule.MinQty); n < 0 {
		candidate, reason = model.Zero(), DeltaResidualMinQty
	} else if !reduce {
		ok, err := model.MeetsMinNotional(candidate, price, rule.MinIncreaseNotional)
		if err != nil {
			return PositionDelta{}, err
		}
		if !ok {
			candidate, reason = model.Zero(), DeltaResidualMinNotional
		}
	}
	remaining, err := requested.Sub(candidate)
	if err != nil {
		return PositionDelta{}, err
	}
	if n, _ := remaining.Cmp(model.Zero()); n > 0 && reason == "" {
		reason = DeltaResidualStep
	}
	return PositionDelta{symbol, reduce, requested, candidate, remaining, reason}, nil
}
