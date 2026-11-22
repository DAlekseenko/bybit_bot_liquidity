package paper

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// ErrFillModel обозначает ошибку входа или расчёта модели fill.
var ErrFillModel = errors.New("invalid baseline fill model input")

// FillInput задаёт явные параметры локальной симуляции полного baseline-пакета.
type FillInput struct {
	Symbols               []string
	Plan                  model.TradePlan
	Rules                 map[string]planner.MarketDeltaRules
	WalletUSDT            model.Decimal
	Positions             map[string]model.LongPosition
	ReferencePrices       map[string]model.Decimal
	FeeRate, SlippageRate model.Decimal
}

// SimulateBaselineFills строит атомарный пакет fill без сетевого исполнения.
func SimulateBaselineFills(in FillInput) (backtest.BaselineFillBatchResult, error) {
	symbols, err := config.CanonicalSymbols(in.Symbols)
	if err != nil || !exactKeys(symbols, in.Rules) || !exactKeys(symbols, in.Positions) || !exactKeys(symbols, in.ReferencePrices) {
		return fillFailure(ErrFillModel)
	}
	for _, value := range []model.Decimal{in.WalletUSDT, in.FeeRate, in.SlippageRate} {
		if err := nonnegative(value); err != nil {
			return fillFailure(err)
		}
	}
	one, err := model.ParseAmount("1")
	if err != nil {
		return fillFailure(err)
	}
	if cmp, err := in.SlippageRate.Cmp(one); err != nil || cmp >= 0 {
		return fillFailure(ErrFillModel)
	}
	for _, symbol := range symbols {
		if err := positive(in.ReferencePrices[symbol]); err != nil {
			return fillFailure(err)
		}
	}
	projection, err := planner.ProjectBaselineDeltas(symbols, in.Plan, in.Rules)
	if err != nil {
		return fillFailure(err)
	}
	deltas := projection.Deltas()
	for _, delta := range deltas {
		if delta.Residual != "" || !decimalEqual(delta.RequestedQty, delta.CandidateQty) || !isZero(delta.UnplannedQty) {
			return fillFailure(fmt.Errorf("%w: %s residual=%s", backtest.ErrBaselineFillResidual, delta.Symbol, delta.Residual))
		}
	}

	quantities, targets := in.Plan.Snapshot().Positions(), in.Plan.Targets()
	targetBySymbol := make(map[string]model.Decimal, len(targets))
	for _, target := range targets {
		targetBySymbol[target.Symbol] = target.Quantity
	}
	equity := in.Plan.Snapshot().Equity()
	three, err := model.ParseAmount("3")
	if err != nil {
		return fillFailure(err)
	}
	third, err := equity.Quo(three)
	if err != nil {
		return fillFailure(err)
	}
	fills := make([]model.ConfirmedFill, 0, len(deltas))
	for _, symbol := range symbols {
		position, found := in.Positions[symbol]
		if !found || !decimalEqual(position.Quantity, quantities[symbol]) || !validPosition(position) {
			return fillFailure(backtest.ErrBaselineFillBatch)
		}
		reserveSide := model.FillBuy
		for _, delta := range deltas {
			if delta.Symbol == symbol && delta.ReduceOnly {
				reserveSide = model.FillSell
				break
			}
		}
		// Для zero-delta сохраняется прежняя BUY-оценка резерва.
		price, err := executionPrice(in.ReferencePrices[symbol], reserveSide, in.SlippageRate)
		if err != nil {
			return fillFailure(err)
		}
		maxQty, err := planner.BaselineQuantity(third, price, position.Quantity, in.FeeRate, in.Rules[symbol].QtyStep)
		if err != nil {
			return fillFailure(err)
		}
		if cmp, err := targetBySymbol[symbol].Cmp(maxQty); err != nil || cmp > 0 {
			return fillFailure(planner.ReasonInfeasibleReserve)
		}
	}
	for _, delta := range deltas {
		side := model.FillBuy
		if delta.ReduceOnly {
			side = model.FillSell
		}
		price, err := executionPrice(in.ReferencePrices[delta.Symbol], side, in.SlippageRate)
		if err != nil {
			return fillFailure(err)
		}
		notional, err := delta.CandidateQty.Mul(price)
		if err != nil {
			return fillFailure(err)
		}
		fee, err := notional.Mul(in.FeeRate)
		if err != nil {
			return fillFailure(err)
		}
		fills = append(fills, model.ConfirmedFill{
			ExecutionID: executionID(delta.Symbol, side, delta.CandidateQty, price, fee, in.Plan.Metadata().ScheduledAt),
			Symbol:      delta.Symbol, Side: side, ExecutedAt: in.Plan.Metadata().ScheduledAt,
			Quantity: delta.CandidateQty, ExecutionPrice: price, FeeUSDT: fee,
		})
	}
	result, err := backtest.ApplyFullBaselineFillBatch(backtest.BaselineFillBatchInput{
		Symbols: symbols, Plan: in.Plan, Rules: in.Rules, WalletUSDT: in.WalletUSDT, Positions: in.Positions, Fills: fills,
	})
	if err != nil {
		return fillFailure(err)
	}
	return result, nil
}

// exactKeys проверяет равенство набора ключей каноническому universe.
func exactKeys[T any](symbols []string, values map[string]T) bool {
	if len(symbols) != len(values) {
		return false
	}
	for _, symbol := range symbols {
		if _, ok := values[symbol]; !ok {
			return false
		}
	}
	return true
}

// nonnegative проверяет decimal без принятия нулевого значения Go-типа.
func nonnegative(value model.Decimal) error {
	if _, _, err := value.CanonicalRatio(); err != nil {
		return errors.Join(ErrFillModel, err)
	}
	if cmp, err := value.Cmp(model.Zero()); err != nil || cmp < 0 {
		return ErrFillModel
	}
	return nil
}

// positive проверяет явно заданную строго положительную цену.
func positive(value model.Decimal) error {
	if err := nonnegative(value); err != nil {
		return err
	}
	if isZero(value) {
		return ErrFillModel
	}
	return nil
}

// isZero возвращает true только для валидного точного нуля.
func isZero(value model.Decimal) bool {
	cmp, err := value.Cmp(model.Zero())
	return err == nil && cmp == 0
}

// decimalEqual сравнивает два точных значения без потери причины ошибки.
func decimalEqual(a, b model.Decimal) bool {
	cmp, err := a.Cmp(b)
	return err == nil && cmp == 0
}

// validPosition проверяет форму LONG-позиции перед расчётом резерва.
func validPosition(position model.LongPosition) bool {
	quantity, err := position.Quantity.Cmp(model.Zero())
	basis, basisErr := position.CostBasisUSDT.Cmp(model.Zero())
	return err == nil && basisErr == nil &&
		((quantity == 0 && basis == 0) || (quantity > 0 && basis > 0))
}

// executionPrice применяет точный знак проскальзывания к reference price.
func executionPrice(reference model.Decimal, side model.FillSide, slip model.Decimal) (model.Decimal, error) {
	base, err := model.ParseAmount("1")
	if err != nil {
		return model.Decimal{}, errors.Join(ErrFillModel, err)
	}
	if side == model.FillBuy {
		base, err = base.Add(slip)
	} else {
		base, err = base.Sub(slip)
	}
	if err != nil {
		return model.Decimal{}, errors.Join(ErrFillModel, err)
	}
	price, err := reference.Mul(base)
	if err != nil || isZero(price) {
		return model.Decimal{}, errors.Join(ErrFillModel, err)
	}
	return price, nil
}

// executionID хеширует канонические экономические поля одного simulated fill.
func executionID(symbol string, side model.FillSide, quantity, price, fee model.Decimal, atTime time.Time) string {
	fields := []string{symbol, string(side), decimalKey(quantity), decimalKey(price), decimalKey(fee), atTime.UTC().Format(time.RFC3339Nano)}
	var input strings.Builder
	for _, field := range fields {
		fmt.Fprintf(&input, "%d:%s", len(field), field)
	}
	hash := sha256.Sum256([]byte(input.String()))
	return "sim-" + hex.EncodeToString(hash[:])
}

// decimalKey возвращает точный ключ включая нетерминируемые рациональные значения.
func decimalKey(value model.Decimal) string {
	n, d, _ := value.CanonicalRatio()
	return n + "/" + d
}

// fillFailure гарантирует пустой результат и сохраняет исходную причину ошибки.
func fillFailure(cause error) (backtest.BaselineFillBatchResult, error) {
	if cause == nil {
		cause = ErrFillModel
	}
	return backtest.BaselineFillBatchResult{}, errors.Join(ErrFillModel, cause)
}
