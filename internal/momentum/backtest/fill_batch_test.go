package backtest

import (
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// batchDecimal разбирает точный тестовый литерал.
func batchDecimal(t *testing.T, text string) model.Decimal {
	t.Helper()
	value, err := model.ParseAmount(text)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// batchPlan создаёт настоящий неизменяемый план из явно заданных количеств.
func batchPlan(t *testing.T, positions map[string]model.LongPosition, targets map[string]string, profile, formula string) model.TradePlan {
	t.Helper()
	quantities := make(map[string]model.Decimal)
	prices := make(map[string]model.Decimal)
	output := make([]model.Target, 0, len(targets))
	for symbol, position := range positions {
		quantities[symbol], prices[symbol] = position.Quantity, batchDecimal(t, "100")
		output = append(output, model.Target{Symbol: symbol, Quantity: batchDecimal(t, targets[symbol])})
	}
	at := time.Date(2025, 7, 7, 0, 0, 0, 0, time.UTC)
	snapshot, err := model.NewPortfolioSnapshot(model.SnapshotInput{
		Environment: "backtest", AccountID: "a", ConfigVersion: "v1", CapturedAt: at,
		Equity: batchDecimal(t, "9000"), Prices: prices, Positions: quantities,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := model.NewTradePlan(model.PlanInput{
		Snapshot: snapshot, Targets: output,
		Metadata: model.PlanMetadata{
			StrategyID: "s", RunID: "r", ProfileID: profile, ModelID: "m", FormulaID: formula,
			ConfigHash: strings.Repeat("a", 64), ScheduledAt: at, EquityCutoffAt: at, Revision: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// batchFixture задаёт сокращение BTC, увеличение ETH и неизменный HYPE.
func batchFixture(t *testing.T) BaselineFillBatchInput {
	t.Helper()
	positions := map[string]model.LongPosition{
		"BTCUSDT":  {Quantity: batchDecimal(t, "3"), CostBasisUSDT: batchDecimal(t, "270")},
		"ETHUSDT":  {Quantity: model.Zero(), CostBasisUSDT: model.Zero()},
		"HYPEUSDT": {Quantity: batchDecimal(t, "1"), CostBasisUSDT: batchDecimal(t, "50")},
	}
	plan := batchPlan(t, positions, map[string]string{"BTCUSDT": "2", "ETHUSDT": "2", "HYPEUSDT": "1"}, "baseline_delta_1x", "D-01")
	rules := make(map[string]planner.MarketDeltaRules)
	for symbol := range positions {
		rules[symbol] = planner.MarketDeltaRules{QtyStep: batchDecimal(t, "0.1"), MinQty: batchDecimal(t, "0.1"), MaxMarketQty: batchDecimal(t, "100"), MinIncreaseNotional: model.Zero()}
	}
	at := plan.Metadata().ScheduledAt
	return BaselineFillBatchInput{Symbols: []string{"HYPEUSDT", "ETHUSDT", "BTCUSDT"}, Plan: plan, Rules: rules, WalletUSDT: batchDecimal(t, "1000"), Positions: positions, Fills: []model.ConfirmedFill{
		{ExecutionID: "sell", Symbol: "BTCUSDT", Side: model.FillSell, ExecutedAt: at, Quantity: batchDecimal(t, "1"), ExecutionPrice: batchDecimal(t, "110"), FeeUSDT: batchDecimal(t, "1")},
		{ExecutionID: "buy", Symbol: "ETHUSDT", Side: model.FillBuy, ExecutedAt: at, Quantity: batchDecimal(t, "2"), ExecutionPrice: batchDecimal(t, "100"), FeeUSDT: batchDecimal(t, "2")},
	}}
}

// requireBatchAmount сравнивает точные суммы без денежного округления.
func requireBatchAmount(t *testing.T, got model.Decimal, text string) {
	t.Helper()
	if cmp, err := got.Cmp(batchDecimal(t, text)); err != nil || cmp != 0 {
		t.Fatalf("amount differs from %s: %v", text, err)
	}
}

// TestFullBaselineFillBatch проверяет полный пакет, порядок и владение данными.
func TestFullBaselineFillBatch(t *testing.T) {
	input := batchFixture(t)
	beforePositions, beforeFills := maps.Clone(input.Positions), append([]model.ConfirmedFill(nil), input.Fills...)
	first, err := ApplyFullBaselineFillBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	requireBatchAmount(t, first.WalletUSDT, "1017")
	requireBatchAmount(t, first.RealizedPnLUSDT, "20")
	requireBatchAmount(t, first.FeesUSDT, "3")
	requireBatchAmount(t, first.CashDeltaUSDT, "17")
	requireBatchAmount(t, first.Positions["BTCUSDT"].CostBasisUSDT, "180")
	requireBatchAmount(t, first.Positions["ETHUSDT"].CostBasisUSDT, "200")
	if len(first.Postings) != 2 || first.Postings[0].Fill.Side != model.FillSell || first.Postings[1].Fill.Side != model.FillBuy || !reflect.DeepEqual(first.Positions["HYPEUSDT"], input.Positions["HYPEUSDT"]) {
		t.Fatal("incorrect phases or unchanged share")
	}
	second, err := ApplyFullBaselineFillBatch(input)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("repeat differs: %v", err)
	}
	reordered := input
	reordered.Symbols = []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}
	ordered, err := ApplyFullBaselineFillBatch(reordered)
	if err != nil || !reflect.DeepEqual(second, ordered) {
		t.Fatalf("universe ordering differs: %v", err)
	}
	first.Positions["BTCUSDT"] = model.LongPosition{}
	first.Postings[0] = model.FillPosting{}
	third, err := ApplyFullBaselineFillBatch(input)
	if err != nil || !reflect.DeepEqual(second, third) || !reflect.DeepEqual(input.Positions, beforePositions) || !reflect.DeepEqual(input.Fills, beforeFills) {
		t.Fatal("state alias or input mutation")
	}
}

// TestFullBatchNoopAndClose проверяет пустой пакет и полное закрытие с нулевой комиссией.
func TestFullBatchNoopAndClose(t *testing.T) {
	input := batchFixture(t)
	input.Plan = batchPlan(t, input.Positions, map[string]string{"BTCUSDT": "3", "ETHUSDT": "0", "HYPEUSDT": "1"}, "baseline_delta_1x", "D-01")
	input.Fills = nil
	result, err := ApplyFullBaselineFillBatch(input)
	if err != nil || len(result.Postings) != 0 || !reflect.DeepEqual(result.Positions, input.Positions) {
		t.Fatalf("noop differs: %v", err)
	}
	input.Plan = batchPlan(t, input.Positions, map[string]string{"BTCUSDT": "0", "ETHUSDT": "0", "HYPEUSDT": "0"}, "baseline_delta_1x", "D-01")
	at := input.Plan.Metadata().ScheduledAt
	input.Fills = []model.ConfirmedFill{
		{ExecutionID: "btc", Symbol: "BTCUSDT", Side: model.FillSell, ExecutedAt: at, Quantity: batchDecimal(t, "3"), ExecutionPrice: batchDecimal(t, "80"), FeeUSDT: model.Zero()},
		{ExecutionID: "hype", Symbol: "HYPEUSDT", Side: model.FillSell, ExecutedAt: at, Quantity: batchDecimal(t, "1"), ExecutionPrice: batchDecimal(t, "40"), FeeUSDT: model.Zero()},
	}
	result, err = ApplyFullBaselineFillBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	requireBatchAmount(t, result.WalletUSDT, "960")
	requireBatchAmount(t, result.Postings[0].RealizedPnLUSDT, "-30")
	for _, position := range result.Positions {
		requireBatchAmount(t, position.Quantity, "0")
		requireBatchAmount(t, position.CostBasisUSDT, "0")
	}
}

// TestFullBatchRejects проверяет независимые отказы без частично применённого состояния.
func TestFullBatchRejects(t *testing.T) {
	cases := map[string]func(*BaselineFillBatchInput){
		"symbols":          func(i *BaselineFillBatchInput) { i.Symbols = nil },
		"extra-symbol":     func(i *BaselineFillBatchInput) { i.Symbols = append(i.Symbols, "XUSDT") },
		"extra-rule":       func(i *BaselineFillBatchInput) { i.Rules["XUSDT"] = i.Rules["BTCUSDT"] },
		"plan":             func(i *BaselineFillBatchInput) { i.Plan = model.TradePlan{} },
		"rules":            func(i *BaselineFillBatchInput) { delete(i.Rules, "BTCUSDT") },
		"wallet":           func(i *BaselineFillBatchInput) { i.WalletUSDT = model.Decimal{} },
		"missing-position": func(i *BaselineFillBatchInput) { delete(i.Positions, "BTCUSDT") },
		"extra-position":   func(i *BaselineFillBatchInput) { i.Positions["XUSDT"] = i.Positions["BTCUSDT"] },
		"basis": func(i *BaselineFillBatchInput) {
			p := i.Positions["BTCUSDT"]
			p.CostBasisUSDT = model.Zero()
			i.Positions["BTCUSDT"] = p
		},
		"starting-quantity": func(i *BaselineFillBatchInput) {
			p := i.Positions["BTCUSDT"]
			p.Quantity = batchDecimal(t, "4")
			i.Positions["BTCUSDT"] = p
		},
		"count":     func(i *BaselineFillBatchInput) { i.Fills = i.Fills[:1] },
		"symbol":    func(i *BaselineFillBatchInput) { i.Fills[1].Symbol = "HYPEUSDT" },
		"side":      func(i *BaselineFillBatchInput) { i.Fills[1].Side = model.FillSell },
		"quantity":  func(i *BaselineFillBatchInput) { i.Fills[1].Quantity = batchDecimal(t, "1") },
		"time":      func(i *BaselineFillBatchInput) { i.Fills[1].ExecutedAt = i.Fills[1].ExecutedAt.Add(time.Second) },
		"id":        func(i *BaselineFillBatchInput) { i.Fills[1].ExecutionID = "sell" },
		"price":     func(i *BaselineFillBatchInput) { i.Fills[1].ExecutionPrice = model.Zero() },
		"fee":       func(i *BaselineFillBatchInput) { i.Fills[1].FeeUSDT = model.Decimal{} },
		"buy-first": func(i *BaselineFillBatchInput) { i.Fills[0], i.Fills[1] = i.Fills[1], i.Fills[0] },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			input := batchFixture(t)
			edit(&input)
			beforePositions, beforeFills := maps.Clone(input.Positions), append([]model.ConfirmedFill(nil), input.Fills...)
			result, err := ApplyFullBaselineFillBatch(input)
			if err == nil || !reflect.DeepEqual(result, BaselineFillBatchResult{}) || !reflect.DeepEqual(input.Positions, beforePositions) || !reflect.DeepEqual(input.Fills, beforeFills) {
				t.Fatalf("partial result/mutation: %v", err)
			}
		})
	}
}

// TestFullBatchResiduals проверяет отказ всех известных ограничений перед увеличениями.
func TestFullBatchResiduals(t *testing.T) {
	for _, reason := range []string{"max", "step", "min", "notional"} {
		t.Run(reason, func(t *testing.T) {
			input := batchFixture(t)
			r := input.Rules["ETHUSDT"]
			switch reason {
			case "max":
				r.MaxMarketQty = batchDecimal(t, "1")
			case "step":
				r.QtyStep = batchDecimal(t, "1.5")
			case "min":
				r.MinQty = batchDecimal(t, "3")
			case "notional":
				r.MinIncreaseNotional = batchDecimal(t, "201")
			}
			input.Rules["ETHUSDT"] = r
			result, err := ApplyFullBaselineFillBatch(input)
			if !errors.Is(err, ErrBaselineFillResidual) || !reflect.DeepEqual(result, BaselineFillBatchResult{}) {
				t.Fatalf("residual accepted: %v", err)
			}
		})
	}
}

// TestFullBatchArithmeticRollback проверяет переполнение после успешного локального сокращения.
func TestFullBatchArithmeticRollback(t *testing.T) {
	input := batchFixture(t)
	huge := batchDecimal(t, strings.Repeat("9", 120))
	var err error
	for n := 0; n < 3; n++ {
		huge, err = huge.Mul(huge)
		if err != nil {
			t.Fatal(err)
		}
	}
	previous, err := huge.Sub(batchDecimal(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	input.WalletUSDT, err = batchDecimal(t, "1").Quo(huge)
	if err != nil {
		t.Fatal(err)
	}
	input.Fills[1].FeeUSDT, err = batchDecimal(t, "1").Quo(previous)
	if err != nil {
		t.Fatal(err)
	}
	before := maps.Clone(input.Positions)
	result, err := ApplyFullBaselineFillBatch(input)
	if !errors.Is(err, ErrBaselineFillArithmetic) || !reflect.DeepEqual(result, BaselineFillBatchResult{}) || !reflect.DeepEqual(input.Positions, before) {
		t.Fatalf("arithmetic did not roll back: %v", err)
	}
}

// TestFullBatchAllIncreasesAndRationalBasis проверяет одну фазу и рациональную базу.
func TestFullBatchAllIncreasesAndRationalBasis(t *testing.T) {
	input := batchFixture(t)
	for symbol := range input.Positions {
		input.Positions[symbol] = model.LongPosition{Quantity: model.Zero(), CostBasisUSDT: model.Zero()}
	}
	input.Plan = batchPlan(t, input.Positions, map[string]string{"BTCUSDT": "1", "ETHUSDT": "2", "HYPEUSDT": "3"}, "baseline_delta_1x", "D-01")
	input.Fills = nil
	for index, symbol := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		quantity := []string{"1", "2", "3"}[index]
		input.Fills = append(input.Fills, model.ConfirmedFill{ExecutionID: symbol, Symbol: symbol, Side: model.FillBuy, ExecutedAt: input.Plan.Metadata().ScheduledAt, Quantity: batchDecimal(t, quantity), ExecutionPrice: batchDecimal(t, "100"), FeeUSDT: model.Zero()})
	}
	result, err := ApplyFullBaselineFillBatch(input)
	if err != nil || len(result.Postings) != 3 {
		t.Fatalf("increase phase failed: %v", err)
	}
	requireBatchAmount(t, result.WalletUSDT, "1000")
	input = batchFixture(t)
	position := input.Positions["BTCUSDT"]
	position.CostBasisUSDT = batchDecimal(t, "100")
	input.Positions["BTCUSDT"] = position
	result, err = ApplyFullBaselineFillBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	numerator, denominator, _ := result.RealizedPnLUSDT.CanonicalRatio()
	if numerator != "230" || denominator != "3" {
		t.Fatalf("rational realized differs: %s/%s", numerator, denominator)
	}
}

// TestFullBatchPropagatesPlanAndModelErrors проверяет исходные классификации отказа.
func TestFullBatchPropagatesPlanAndModelErrors(t *testing.T) {
	for _, field := range []string{"profile", "formula", "price"} {
		input := batchFixture(t)
		expected := model.ErrInvalidLedgerFill
		if field == "price" {
			input.Fills[1].ExecutionPrice = model.Zero()
		} else {
			profile, formula := "baseline_delta_1x", "D-01"
			if field == "profile" {
				profile = "isolated_weekly_reset"
			} else {
				formula = "unknown"
			}
			input.Plan = batchPlan(t, input.Positions, map[string]string{"BTCUSDT": "2", "ETHUSDT": "2", "HYPEUSDT": "1"}, profile, formula)
			expected = planner.ErrInvalidDeltaPlan
		}
		result, err := ApplyFullBaselineFillBatch(input)
		if !errors.Is(err, expected) || !reflect.DeepEqual(result, BaselineFillBatchResult{}) {
			t.Fatalf("%s classification differs: %v", field, err)
		}
	}
}
