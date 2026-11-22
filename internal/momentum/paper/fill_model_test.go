package paper

import (
	"errors"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// amount разбирает точное тестовое число.
func amount(t *testing.T, value string) model.Decimal {
	d, err := model.ParseAmount(value)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type countedTransport struct{ calls int }

// RoundTrip считает обращения к подменённому HTTP-транспорту.
func (c *countedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls++
	return nil, errors.New("unexpected HTTP request")
}

// fixture создаёт immutable D-01 план с одной продажей и одной покупкой.
func fixture(t *testing.T, equity string, noop bool) FillInput {
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	positions := map[string]model.LongPosition{
		"BTCUSDT":  {Quantity: amount(t, "2"), CostBasisUSDT: amount(t, "180")},
		"ETHUSDT":  {Quantity: model.Zero(), CostBasisUSDT: model.Zero()},
		"HYPEUSDT": {Quantity: amount(t, "1"), CostBasisUSDT: amount(t, "50")},
	}
	snapshot, err := model.NewPortfolioSnapshot(model.SnapshotInput{
		Environment: "backtest", AccountID: "paper", ConfigVersion: "v1", CapturedAt: at,
		Equity: amount(t, equity), Prices: map[string]model.Decimal{"BTCUSDT": amount(t, "100"), "ETHUSDT": amount(t, "100"), "HYPEUSDT": amount(t, "50")},
		Positions: map[string]model.Decimal{"BTCUSDT": amount(t, "2"), "ETHUSDT": model.Zero(), "HYPEUSDT": amount(t, "1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	btcTarget, ethTarget := amount(t, "1"), amount(t, "1")
	if noop {
		btcTarget, ethTarget = amount(t, "2"), model.Zero()
	}
	plan, err := model.NewTradePlan(model.PlanInput{
		Snapshot: snapshot, Targets: []model.Target{{Symbol: "BTCUSDT", Quantity: btcTarget}, {Symbol: "ETHUSDT", Quantity: ethTarget}, {Symbol: "HYPEUSDT", Quantity: amount(t, "1")}},
		Metadata: model.PlanMetadata{StrategyID: "s", RunID: "r", ProfileID: "baseline_delta_1x", ModelID: "m", FormulaID: "D-01", ConfigHash: strings.Repeat("a", 64), ScheduledAt: at, EquityCutoffAt: at, Revision: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]planner.MarketDeltaRules{}
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"} {
		rules[symbol] = planner.MarketDeltaRules{QtyStep: amount(t, "0.1"), MinQty: amount(t, "0.1"), MaxMarketQty: amount(t, "10"), MinIncreaseNotional: model.Zero()}
	}
	return FillInput{Symbols: []string{"ETHUSDT", "BTCUSDT", "HYPEUSDT"}, Plan: plan, Rules: rules, WalletUSDT: amount(t, "1000"), Positions: positions, ReferencePrices: map[string]model.Decimal{"BTCUSDT": amount(t, "100"), "ETHUSDT": amount(t, "100"), "HYPEUSDT": amount(t, "50")}, FeeRate: amount(t, "0.01"), SlippageRate: amount(t, "0.1"), Admission: planner.AdmitBaselineExecution}
}

// TestSimulateBaselineFills проверяет точные цены, fees, порядок и повторяемость.
func TestSimulateBaselineFills(t *testing.T) {
	in := fixture(t, "3000", false)
	previousTransport := http.DefaultTransport
	counter := &countedTransport{}
	http.DefaultTransport = counter
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	t.Setenv("BYBIT_API_KEY", "poison")
	t.Setenv("BYBIT_API_SECRET", "poison")
	beforePositions, beforeRules, beforePrices := maps.Clone(in.Positions), maps.Clone(in.Rules), maps.Clone(in.ReferencePrices)
	beforeSymbols, beforeHash := append([]string(nil), in.Symbols...), in.Plan.Hash()
	got, err := SimulateBaselineFills(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Postings) != 2 || got.Postings[0].Fill.Side != model.FillSell || got.Postings[1].Fill.Side != model.FillBuy {
		t.Fatal("fills are not reduction-first")
	}
	for _, want := range []struct {
		i          int
		price, fee string
	}{{0, "90", "0.9"}, {1, "110", "1.1"}} {
		fill := got.Postings[want.i].Fill
		if !decimalEqual(fill.ExecutionPrice, amount(t, want.price)) || !decimalEqual(fill.FeeUSDT, amount(t, want.fee)) || !fill.ExecutedAt.Equal(in.Plan.Metadata().ScheduledAt) {
			t.Fatalf("incorrect fill: %+v", fill)
		}
	}
	if !decimalEqual(got.WalletUSDT, amount(t, "998")) || !decimalEqual(got.Positions["ETHUSDT"].CostBasisUSDT, amount(t, "110")) ||
		!reflect.DeepEqual(in.Positions, beforePositions) || !reflect.DeepEqual(in.Rules, beforeRules) ||
		!reflect.DeepEqual(in.ReferencePrices, beforePrices) || !reflect.DeepEqual(in.Symbols, beforeSymbols) || in.Plan.Hash() != beforeHash {
		t.Fatal("wallet, position, or input changed incorrectly")
	}
	repeat, err := SimulateBaselineFills(in)
	if err != nil || !reflect.DeepEqual(got, repeat) {
		t.Fatalf("repeat differs: %v", err)
	}
	in.ReferencePrices["ETHUSDT"] = amount(t, "101")
	changed, err := SimulateBaselineFills(in)
	if err != nil || changed.Postings[1].Fill.ExecutionID == got.Postings[1].Fill.ExecutionID {
		t.Fatalf("price did not change ID: %v", err)
	}
	in = fixture(t, "3000", false)
	in.FeeRate = amount(t, "0.02")
	changed, err = SimulateBaselineFills(in)
	if err != nil || changed.Postings[1].Fill.ExecutionID == got.Postings[1].Fill.ExecutionID {
		t.Fatalf("fee did not change ID: %v", err)
	}
	in.SlippageRate = model.Zero()
	zeroSlip, err := SimulateBaselineFills(in)
	if err != nil || !decimalEqual(zeroSlip.Postings[0].Fill.ExecutionPrice, amount(t, "100")) || !decimalEqual(zeroSlip.Postings[1].Fill.ExecutionPrice, amount(t, "100")) {
		t.Fatalf("zero slippage: %v", err)
	}
	if counter.calls != 0 {
		t.Fatalf("trade HTTP calls: %d", counter.calls)
	}
	boundary := fixture(t, "300", false)
	boundary.SlippageRate = model.Zero()
	boundary.ReferencePrices["BTCUSDT"], boundary.ReferencePrices["HYPEUSDT"] = amount(t, "90"), amount(t, "50")
	boundaryPlan, err := model.NewTradePlan(model.PlanInput{
		Snapshot: boundary.Plan.Snapshot(), Targets: []model.Target{{Symbol: "BTCUSDT", Quantity: amount(t, "1")}, {Symbol: "ETHUSDT", Quantity: model.Zero()}, {Symbol: "HYPEUSDT", Quantity: amount(t, "1")}}, Metadata: boundary.Plan.Metadata(),
	})
	if err != nil {
		t.Fatal(err)
	}
	boundary.Plan = boundaryPlan
	if _, err := SimulateBaselineFills(boundary); err != nil {
		t.Fatalf("SELL reserve boundary should fit at execution price: %v", err)
	}
}

// TestSimulateBaselineFillsRejects проверяет fail-closed входы, reserve и остатки.
func TestSimulateBaselineFillsRejects(t *testing.T) {
	mutations := map[string]func(*FillInput){
		"bad-fee":           func(i *FillInput) { i.FeeRate = model.Decimal{} },
		"negative-fee":      func(i *FillInput) { i.FeeRate = amount(t, "-0.1") },
		"negative-slip":     func(i *FillInput) { i.SlippageRate = amount(t, "-0.1") },
		"slip-one":          func(i *FillInput) { i.SlippageRate = amount(t, "1") },
		"missing-reference": func(i *FillInput) { delete(i.ReferencePrices, "BTCUSDT") },
		"foreign-reference": func(i *FillInput) { i.ReferencePrices["XUSDT"] = amount(t, "1") },
		"malformed-price":   func(i *FillInput) { i.ReferencePrices["BTCUSDT"] = model.Decimal{} },
		"missing-rule":      func(i *FillInput) { delete(i.Rules, "BTCUSDT") },
		"missing-position":  func(i *FillInput) { delete(i.Positions, "BTCUSDT") },
		"foreign-position":  func(i *FillInput) { i.Positions["XUSDT"] = i.Positions["BTCUSDT"] },
		"position-mismatch": func(i *FillInput) {
			i.Positions["BTCUSDT"] = model.LongPosition{Quantity: amount(t, "3"), CostBasisUSDT: amount(t, "300")}
		},
		"residual": func(i *FillInput) { r := i.Rules["BTCUSDT"]; r.MaxMarketQty = amount(t, "0.5"); i.Rules["BTCUSDT"] = r },
		"reserve": func(i *FillInput) {
			s := i.Plan.Snapshot()
			small, _ := model.NewPortfolioSnapshot(model.SnapshotInput{Environment: s.Environment(), AccountID: s.AccountID(), ConfigVersion: s.ConfigVersion(), CapturedAt: s.CapturedAt(), Equity: amount(t, "3"), Prices: s.Prices(), Positions: s.Positions()})
			p, _ := model.NewTradePlan(model.PlanInput{Snapshot: small, Targets: i.Plan.Targets(), Metadata: i.Plan.Metadata()})
			i.Plan = p
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			in := fixture(t, "3000", false)
			mutate(&in)
			before := in.Positions["BTCUSDT"]
			got, err := SimulateBaselineFills(in)
			if !errors.Is(err, ErrFillModel) || !reflect.DeepEqual(got, backtest.BaselineFillBatchResult{}) || !reflect.DeepEqual(in.Positions["BTCUSDT"], before) {
				t.Fatalf("not atomic fail-closed: %v", err)
			}
		})
	}
}

// TestSimulateBaselineFillsNoop проверяет нулевую дельту без искусственных fills.
func TestSimulateBaselineFillsNoop(t *testing.T) {
	in := fixture(t, "3000", true)
	got, err := SimulateBaselineFills(in)
	if err != nil || len(got.Postings) != 0 || !reflect.DeepEqual(got.Positions, in.Positions) {
		t.Fatalf("noop result: %v", err)
	}
}

// TestFillAdmission проверяет явную политику, текущие входы и изоляцию её карт.
func TestFillAdmission(t *testing.T) {
	policyErr := errors.New("policy rejected")
	for _, tc := range []struct {
		name   string
		policy FillAdmission
		noop   bool
		want   error
	}{
		{"nil", nil, false, ErrFillModel}, {"nil noop", nil, true, ErrFillModel}, {"reject", func(model.TradePlan, map[string]planner.MarketDeltaRules, model.Decimal, map[string]model.Decimal, map[string]model.Decimal) error {
			return policyErr
		}, false, policyErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture(t, "3", tc.noop)
			in.Admission = tc.policy
			got, err := SimulateBaselineFills(in)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, backtest.BaselineFillBatchResult{}) {
				t.Fatalf("result=%+v err=%v", got, err)
			}
		})
	}
	in := fixture(t, "3", false)
	before := in.Rules["BTCUSDT"]
	var observed bool
	in.Admission = func(plan model.TradePlan, rules map[string]planner.MarketDeltaRules, fee model.Decimal, buys, sells map[string]model.Decimal) error {
		observed = plan.Hash() == in.Plan.Hash() && decimalEqual(fee, in.FeeRate) && rules["BTCUSDT"] == before && decimalEqual(buys["ETHUSDT"], amount(t, "110")) && decimalEqual(sells["BTCUSDT"], amount(t, "90")) && errors.Is(planner.AdmitBaselineExecution(plan, rules, fee, buys, sells), planner.ReasonInfeasibleReserve)
		rules["BTCUSDT"] = planner.MarketDeltaRules{}
		buys["ETHUSDT"], sells["BTCUSDT"] = model.Zero(), model.Zero()
		return nil
	}
	got, err := SimulateBaselineFills(in)
	if err != nil || !observed || !decimalEqual(got.Postings[1].Fill.ExecutionPrice, amount(t, "110")) || in.Rules["BTCUSDT"] != before {
		t.Fatalf("callback copies or alternative policy: %v", err)
	}
}

// TestExecutionMinimumNotional применяет минимум к фактической цене BUY и освобождает SELL.
func TestExecutionMinimumNotional(t *testing.T) {
	for _, tc := range []struct {
		name, ref, slip, minimum string
		sell                     bool
		reject                   bool
	}{
		{"below", "80", "0", "95", false, true}, {"equal", "95", "0", "95", false, false}, {"slippage", "80", "0.2", "95", false, false}, {"sell exemption", "100", "0.1", "95", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture(t, "3000", false)
			in.Admission = func(model.TradePlan, map[string]planner.MarketDeltaRules, model.Decimal, map[string]model.Decimal, map[string]model.Decimal) error {
				return nil
			}
			in.ReferencePrices["ETHUSDT"], in.SlippageRate = amount(t, tc.ref), amount(t, tc.slip)
			rule := in.Rules["ETHUSDT"]
			if tc.sell {
				rule = in.Rules["BTCUSDT"]
			}
			rule.MinIncreaseNotional = amount(t, tc.minimum)
			if tc.sell {
				in.Rules["BTCUSDT"] = rule
			} else {
				in.Rules["ETHUSDT"] = rule
			}
			got, err := SimulateBaselineFills(in)
			if tc.reject && (!errors.Is(err, ErrFillMinimumNotional) || !reflect.DeepEqual(got, backtest.BaselineFillBatchResult{})) {
				t.Fatalf("expected atomic minimum rejection: %v", err)
			}
			if !tc.reject && err != nil {
				t.Fatalf("unexpected minimum rejection: %v", err)
			}
		})
	}
}
