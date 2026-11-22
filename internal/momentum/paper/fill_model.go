package paper

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

var (
	// ErrFillModel обозначает ошибку входа или расчёта модели fill.
	ErrFillModel = errors.New("invalid baseline fill model input")
	// ErrFillMinimumNotional обозначает недостаточный notional по execution price.
	ErrFillMinimumNotional = errors.New("fill execution price is below minimum notional")
)

// FillAdmission проверяет план политикой владельца стратегии перед симуляцией.
type FillAdmission func(
	plan model.TradePlan,
	rules map[string]planner.MarketDeltaRules,
	feeRate model.Decimal,
	buyPrices, sellPrices map[string]model.Decimal,
) error

// FillInput задаёт явные параметры локальной симуляции полного baseline-пакета.
type FillInput struct {
	Symbols               []string
	Plan                  model.TradePlan
	Rules                 map[string]planner.MarketDeltaRules
	WalletUSDT            model.Decimal
	Positions             map[string]model.LongPosition
	ReferencePrices       map[string]model.Decimal
	FeeRate, SlippageRate model.Decimal
	Admission             FillAdmission
}

// SimulateBaselineFills строит атомарный пакет fill без сетевого исполнения.
func SimulateBaselineFills(in FillInput) (backtest.BaselineFillBatchResult, error) {
	symbols, err := normalizeSymbols(in.Symbols)
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
	if in.Admission == nil {
		return fillFailure(ErrFillModel)
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

	snapshotPositions := in.Plan.Snapshot().Positions()
	for _, symbol := range symbols {
		position, found := in.Positions[symbol]
		if !found || !decimalEqual(position.Quantity, snapshotPositions[symbol]) || !validPosition(position) {
			return fillFailure(backtest.ErrBaselineFillBatch)
		}
	}

	buyPrices, sellPrices := make(map[string]model.Decimal, len(symbols)), make(map[string]model.Decimal, len(symbols))
	for _, symbol := range symbols {
		buyPrices[symbol], err = executionPrice(in.ReferencePrices[symbol], model.FillBuy, in.SlippageRate)
		if err != nil {
			return fillFailure(err)
		}
		sellPrices[symbol], err = executionPrice(in.ReferencePrices[symbol], model.FillSell, in.SlippageRate)
		if err != nil {
			return fillFailure(err)
		}
	}
	for _, delta := range deltas {
		if delta.ReduceOnly {
			continue
		}
		meetsMinimum, err := model.MeetsMinNotional(delta.CandidateQty, buyPrices[delta.Symbol], in.Rules[delta.Symbol].MinIncreaseNotional)
		if err != nil {
			return fillFailure(err)
		}
		if !meetsMinimum {
			return fillFailure(ErrFillMinimumNotional)
		}
	}
	if err := in.Admission(in.Plan, maps.Clone(in.Rules), in.FeeRate, maps.Clone(buyPrices), maps.Clone(sellPrices)); err != nil {
		return fillFailure(err)
	}

	fills := make([]model.ConfirmedFill, 0, len(deltas))
	for _, delta := range deltas {
		side := model.FillBuy
		if delta.ReduceOnly {
			side = model.FillSell
		}
		price := buyPrices[delta.Symbol]
		if side == model.FillSell {
			price = sellPrices[delta.Symbol]
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

// normalizeSymbols сортирует копию universe и отклоняет пустые или дублирующиеся ключи.
func normalizeSymbols(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, ErrFillModel
	}
	symbols := slices.Clone(values)
	slices.Sort(symbols)
	for i, symbol := range symbols {
		if strings.TrimSpace(symbol) != symbol || symbol == "" || i > 0 && symbols[i-1] == symbol {
			return nil, ErrFillModel
		}
	}
	return symbols, nil
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
