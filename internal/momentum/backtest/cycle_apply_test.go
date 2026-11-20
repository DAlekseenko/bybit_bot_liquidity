package backtest

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// applyFixture подготавливает цикл с явными тестовыми ценами.
func applyFixture(t *testing.T) (BaselineCycleInput, PreparedBaselineCycle, []NextOpenReference) {
	t.Helper()
	input := cycleFixture(t, "now")
	prepared, err := PrepareBaselineCycle(input)
	requireNoError(t, err)
	opens := []NextOpenReference{}
	for _, symbol := range input.Config.Values().Symbols {
		opens = append(opens, NextOpenReference{Symbol: symbol, Price: amount(t, "100")})
	}
	return input, prepared, opens
}

// TestApplyZeroSlipBaselineCycle проверяет fee-once и границы D-01.
func TestApplyZeroSlipBaselineCycle(t *testing.T) {
	input, prepared, opens := applyFixture(t)
	before := cloneCycleInput(input)
	result, err := ApplyZeroSlipBaselineCycle(input, prepared, opens)
	requireNoError(t, err)
	assertDecimal(t, result.WalletUSDT, "8967.001")
	assertDecimal(t, result.FeesUSDT, "2.999")
	assertDecimal(t, result.CashDeltaUSDT, "-2.999")
	assertDecimal(t, result.Positions["BTCUSDT"].Quantity, "29.86")
	assertDecimal(t, result.Positions["ETHUSDT"].Quantity, "29.85")
	if len(result.Postings) != 2 || result.Postings[0].Fill.Side != model.FillSell || result.Postings[1].Fill.Side != model.FillBuy {
		t.Fatal("phase ordering changed")
	}
	for _, posting := range result.Postings {
		assertDecimal(t, posting.Fill.ExecutionPrice, "100")
	}
	if !reflect.DeepEqual(before, input) {
		t.Fatal("input mutated")
	}
	again, err := ApplyZeroSlipBaselineCycle(input, prepared, opens)
	if err != nil || !reflect.DeepEqual(result, again) {
		t.Fatal("repeat differs")
	}
	opens[0].Price = amount(t, "100.0001")
	rejected, err := ApplyZeroSlipBaselineCycle(input, prepared, opens)
	if !errors.Is(err, ErrZeroSlipBaselineReserve) || !strings.Contains(err.Error(), "BTCUSDT at ") || !reflect.DeepEqual(rejected, BaselineFillBatchResult{}) {
		t.Fatal("over-budget execution accepted")
	}
	opens[0].Price = amount(t, "1000000")
	if _, err := ApplyZeroSlipBaselineCycle(input, prepared, opens); !errors.Is(err, planner.ReasonInfeasibleReserve) {
		t.Fatal("closing reserve failure lost planner classification")
	}
	opens[0].Price = amount(t, "100")
	opens[1].Price = amount(t, "200")
	late, err := ApplyZeroSlipBaselineCycle(input, prepared, opens)
	if !errors.Is(err, ErrZeroSlipBaselineReserve) || !strings.Contains(err.Error(), "ETHUSDT at ") || !reflect.DeepEqual(late, BaselineFillBatchResult{}) || !reflect.DeepEqual(before, input) {
		t.Fatal("later-symbol rejection returned partial state")
	}
	opens[1].Price = amount(t, "100")
	opens[0].Price = amount(t, "99.9999")
	cheaper, err := ApplyZeroSlipBaselineCycle(input, prepared, opens)
	requireNoError(t, err)
	assertDecimal(t, cheaper.Positions["BTCUSDT"].Quantity, "29.86")
	result.Positions["BTCUSDT"] = model.LongPosition{}
	result.Postings[0].Fill.ExecutionID = "changed"
	stable, err := ApplyZeroSlipBaselineCycle(input, prepared, []NextOpenReference{
		{Symbol: "HYPEUSDT", Price: amount(t, "100")},
		{Symbol: "ETHUSDT", Price: amount(t, "100")},
		{Symbol: "BTCUSDT", Price: amount(t, "100")},
	})
	if err != nil || !reflect.DeepEqual(again, stable) {
		t.Fatal("output ownership broken")
	}
	opens[0].Price = amount(t, "100")
	input.At = input.At.In(time.FixedZone("test", 3600))
	prepared.Cutoff.AsOf = input.At
	shifted, err := ApplyZeroSlipBaselineCycle(input, prepared, opens)
	if err != nil || !reflect.DeepEqual(again, shifted) {
		t.Fatal("equivalent UTC instant changed output")
	}
	row := []string{"momentum/baseline-zero-slip-fill/v1", prepared.RunID, prepared.CycleID, prepared.Plan.Hash(), "reduce", "BTCUSDT"}
	if stable.Postings[0].Fill.ExecutionID != baselineIdentity(row) {
		t.Fatal("fill ID recipe mismatch")
	}
	if baselineIdentity([]string{"momentum/baseline-zero-slip-fill/v1", "run", "cycle", "plan", "reduce", "BTCUSDT"}) != "7d9931270bc5f28bdd199dd17e065259ca062c989922a582abd2feb2f57c589f" {
		t.Fatal("fill ID golden changed")
	}
}

// TestApplyZeroSlipRefusesMalformed проверяет независимые ошибки на свежем входе.
func TestApplyZeroSlipRefusesMalformed(t *testing.T) {
	for _, name := range []string{"zero model", "nonzero slip", "wrong run", "wrong cycle", "wrong cutoff", "wrong equity",
		"wrong projection", "missing price", "extra price", "duplicate price", "invalid price", "zero price", "negative price",
		"quantity", "basis", "missing position", "wrong kind", "zero plan", "missing valuation", "residual", "wrong model",
		"cutoff wallet", "input wallet", "input funding", "funding identity",
		"funding cash", "funding total", "valuation", "unrealized"} {
		t.Run(name, func(t *testing.T) {
			input, prepared, opens := applyFixture(t)
			switch name {
			case "zero model":
				input.Model = SimulationModel{}
			case "nonzero slip", "residual":
				parameters := input.Model.Parameters()
				if name == "nonzero slip" {
					parameters.SlippageRate = amount(t, "0.01")
				} else {
					rule := parameters.Rules["BTCUSDT"]
					rule.MaxMarketQty = amount(t, "1")
					parameters.Rules["BTCUSDT"] = rule
				}
				var err error
				input.Model, err = NewSimulationModel(input.Config, input.Dataset.Manifest, parameters)
				requireNoError(t, err)
				prepared, err = PrepareBaselineCycle(input)
				requireNoError(t, err)
			case "cutoff wallet":
				prepared.Cutoff.WalletUSDT = amount(t, "9970")
			case "input wallet":
				input.WalletUSDT = amount(t, "10000")
			case "input funding":
				input.Funding[0].Event.Rate = amount(t, "0.02")
				input.Funding[1].Event.Rate = amount(t, "-0.05")
			case "funding identity":
				input.Funding[0].FundingID = "same-cash-different-source"
			case "funding cash":
				prepared.Cutoff.FundingPostings[0].CashDeltaUSDT = model.Zero()
			case "funding total":
				prepared.Cutoff.FundingCashDeltaUSDT = model.Zero()
			case "valuation":
				for symbol, text := range map[string]string{"BTCUSDT": "101", "ETHUSDT": "96"} {
					mark := input.Marks[symbol]
					mark.Price = amount(t, text)
					input.Marks[symbol] = mark
				}
				value, err := ProjectAccountingStep(AccountingInput{
					Symbols: input.Config.Values().Symbols, AsOf: input.At, MarkMaxAge: time.Hour,
					WalletUSDT: input.WalletUSDT, Positions: input.Positions, Marks: input.Marks, Funding: input.Funding,
				})
				requireNoError(t, err)
				prepared.Cutoff = value
			case "unrealized":
				prepared.Cutoff.UnrealizedPnLUSDT = amount(t, "1000")
			case "wrong model":
				parameters := input.Model.Parameters()
				parameters.FeeRate = amount(t, "0.002")
				input.Model = constructSimulation(t, input.Config, input.Dataset.Manifest, parameters)
			case "wrong run":
				prepared.RunID = "wrong"
			case "wrong cycle":
				prepared.CycleID = "wrong"
			case "wrong cutoff":
				prepared.Cutoff.AsOf = prepared.Cutoff.AsOf.Add(1)
			case "wrong equity":
				prepared.Cutoff.EquityUSDT = amount(t, "10000")
			case "wrong projection":
				prepared.Projection = PreparedBaselineCycle{}.Projection
			case "missing price":
				opens = opens[:2]
			case "extra price":
				opens = append(opens, NextOpenReference{Symbol: "OTHERUSDT", Price: amount(t, "1")})
			case "duplicate price":
				opens[2] = opens[0]
			case "invalid price":
				opens[0].Price = model.Decimal{}
			case "zero price":
				opens[0].Price = model.Zero()
			case "negative price":
				opens[0].Price, _ = model.ParseRate("-1")
			case "quantity", "basis":
				position := input.Positions["BTCUSDT"]
				if name == "quantity" {
					position.Quantity = amount(t, "41")
				} else {
					position.CostBasisUSDT = amount(t, "4001")
				}
				input.Positions["BTCUSDT"] = position
			case "missing position":
				delete(input.Positions, "BTCUSDT")
			case "wrong kind":
				input.Kind = "unknown"
			case "zero plan":
				prepared.Plan = model.TradePlan{}
			case "missing valuation":
				prepared.Cutoff.Valuations = nil
			}
			before := cloneCycleInput(input)
			got, err := ApplyZeroSlipBaselineCycle(input, prepared, opens)
			if !errors.Is(err, ErrZeroSlipBaselineApply) || !reflect.DeepEqual(got, BaselineFillBatchResult{}) || !reflect.DeepEqual(before, input) {
				t.Fatalf("failed atomic refusal: %v", err)
			}
			if name == "residual" && !errors.Is(err, ErrBaselineFillResidual) {
				t.Fatal("residual classification lost")
			}
		})
	}
}

// TestApplyZeroSlipShapes проверяет режимы дельт, нулевую fee и no-op.
func TestApplyZeroSlipShapes(t *testing.T) {
	for _, name := range []string{"all increases", "all reductions", "zero fee", "no-op"} {
		t.Run(name, func(t *testing.T) {
			input, _, opens := applyFixture(t)
			if name == "zero fee" || name == "no-op" {
				parameters := input.Model.Parameters()
				parameters.FeeRate = model.Zero()
				input.Model = constructSimulation(t, input.Config, input.Dataset.Manifest, parameters)
			}
			if name == "no-op" {
				input.Funding = nil
			}
			if name == "all increases" || name == "all reductions" || name == "no-op" {
				for symbol := range input.Positions {
					position := model.LongPosition{Quantity: model.Zero(), CostBasisUSDT: model.Zero()}
					if name == "all reductions" || name == "no-op" && symbol != "HYPEUSDT" {
						quantity, basis := "40", "4000"
						if name == "no-op" {
							quantity, basis = "30", "3000"
						}
						position = model.LongPosition{Quantity: amount(t, quantity), CostBasisUSDT: amount(t, basis)}
					}
					input.Positions[symbol] = position
				}
			}
			prepared, err := PrepareBaselineCycle(input)
			requireNoError(t, err)
			result, err := ApplyZeroSlipBaselineCycle(input, prepared, opens)
			requireNoError(t, err)
			switch name {
			case "zero fee":
				assertDecimal(t, result.FeesUSDT, "0")
			case "no-op":
				if len(result.Postings) != 0 {
					t.Fatal("no-op produced postings")
				}
				assertDecimal(t, result.WalletUSDT, "9000")
				opens[2].Price = model.Decimal{}
				if _, err := ApplyZeroSlipBaselineCycle(input, prepared, opens); !errors.Is(err, ErrZeroSlipBaselineApply) {
					t.Fatal("inactive no-op reference accepted")
				}
			}
		})
	}
	maximum := amount(t, strings.Repeat("9", 63)+"."+strings.Repeat("9", 64))
	notional, err := maximum.Mul(maximum)
	requireNoError(t, err)
	_, err = notional.Mul(maximum)
	requireNoError(t, err)
}

// TestApplyZeroSlipArithmeticOverflow проверяет reserve и позднюю ledger арифметику.
func TestApplyZeroSlipArithmeticOverflow(t *testing.T) {
	for _, kind := range []string{"reserve", "later ledger"} {
		t.Run(kind, func(t *testing.T) {
			input, _, opens := applyFixture(t)
			var prepared PreparedBaselineCycle
			base, power := "3", 2440
			if kind == "reserve" {
				base, power = "2", 3900
			}
			divisor, multiplier := amount(t, "1"), amount(t, base)
			for range power {
				var err error
				divisor, err = divisor.Mul(multiplier)
				requireNoError(t, err)
			}
			epsilon, err := amount(t, "1").Quo(divisor)
			requireNoError(t, err)
			if kind == "reserve" {
				input.WalletUSDT, input.Funding = epsilon, nil
				for symbol := range input.Positions {
					input.Positions[symbol] = model.LongPosition{Quantity: model.Zero(), CostBasisUSDT: model.Zero()}
				}
				opens[0].Price = amount(t, "1"+strings.Repeat("0", 127))
			} else {
				input.WalletUSDT, err = input.WalletUSDT.Add(epsilon)
				requireNoError(t, err)
				opens[1].Price = amount(t, "99."+strings.Repeat("9", 61))
			}
			prepared, err = PrepareBaselineCycle(input)
			requireNoError(t, err)
			before := cloneCycleInput(input)
			got, err := ApplyZeroSlipBaselineCycle(input, prepared, opens)
			cause := ErrZeroSlipBaselineReserve
			if kind == "later ledger" {
				cause = ErrBaselineFillArithmetic
			}
			if !errors.Is(err, cause) || !errors.Is(err, ErrZeroSlipBaselineApply) ||
				!reflect.DeepEqual(got, BaselineFillBatchResult{}) || !reflect.DeepEqual(before, input) {
				t.Fatalf("arithmetic classification/atomicity missing: %v", err)
			}
		})
	}
}
