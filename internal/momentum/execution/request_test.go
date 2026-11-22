package execution

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

var symbols = []string{"ADAUSDT", "SOLUSDT", "XRPUSDT"}

// dec разбирает точное десятичное значение теста.
func dec(t *testing.T, s string) model.Decimal {
	t.Helper()
	d, err := model.ParseAmount(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// fixture строит план: ADA сокращается, SOL увеличивается с остатком, XRP — dust.
func fixture(t *testing.T, environment string) (model.TradePlan, map[string]planner.MarketDeltaRules) {
	t.Helper()
	at := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	snap, err := model.NewPortfolioSnapshot(model.SnapshotInput{Environment: environment, AccountID: "acc", ConfigVersion: "v1",
		CapturedAt: at, Equity: dec(t, "1000"),
		Prices:    map[string]model.Decimal{"ADAUSDT": dec(t, "0.5"), "SOLUSDT": dec(t, "100"), "XRPUSDT": dec(t, "2")},
		Positions: map[string]model.Decimal{"ADAUSDT": dec(t, "10"), "SOLUSDT": dec(t, "0"), "XRPUSDT": dec(t, "1")}})
	if err != nil {
		t.Fatal(err)
	}
	meta := model.PlanMetadata{StrategyID: "s", RunID: "run-1", ProfileID: "baseline_delta_1x", ModelID: "m", FormulaID: "D-01",
		ConfigHash: strings.Repeat("a", 64), ScheduledAt: at, EquityCutoffAt: at, Revision: 1}
	plan, err := model.NewTradePlan(model.PlanInput{Snapshot: snap, Metadata: meta, Targets: []model.Target{
		{Symbol: "ADAUSDT", Quantity: dec(t, "4")}, {Symbol: "SOLUSDT", Quantity: dec(t, "2.55")}, {Symbol: "XRPUSDT", Quantity: dec(t, "1.0004")}}})
	if err != nil {
		t.Fatal(err)
	}
	rule := planner.MarketDeltaRules{QtyStep: dec(t, "0.1"), MinQty: dec(t, "0.1"), MaxMarketQty: dec(t, "100"), MinIncreaseNotional: dec(t, "5")}
	return plan, map[string]planner.MarketDeltaRules{"ADAUSDT": rule, "SOLUSDT": rule, "XRPUSDT": rule}
}

// build проецирует дельты планировщиком и строит намерения.
func build(t *testing.T, plan model.TradePlan, rules map[string]planner.MarketDeltaRules) (IntentBatch, error) {
	t.Helper()
	projection, err := planner.ProjectBaselineDeltas(symbols, plan, rules)
	if err != nil {
		t.Fatal(err)
	}
	return BuildOrderIntents(plan, projection, rules)
}

// wire сериализует намерение без slippage и проверяет отсутствие ошибки.
func wire(t *testing.T, in OrderIntent) string {
	t.Helper()
	raw, err := EncodeCreateOrder(in, Slippage{})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestBuildOrderIntents проверяет порядок, floor, dust, reduceOnly и wire golden.
func TestBuildOrderIntents(t *testing.T) {
	plan, rules := fixture(t, "testnet")
	batch, err := build(t, plan, rules)
	if err != nil || len(batch.Intents) != 2 || len(batch.Skipped) != 1 {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
	sell, buy := batch.Intents[0], batch.Intents[1]
	if sell.Symbol != "ADAUSDT" || sell.Side != model.FillSell || !sell.ReduceOnly || sell.Leg != LegReduce ||
		buy.Symbol != "SOLUSDT" || buy.Side != model.FillBuy || buy.ReduceOnly || buy.Leg != LegIncrease {
		t.Fatalf("wrong intents: %#v", batch.Intents)
	}
	if q, _ := buy.Quantity.WireDecimal(); q != "2.5" || buy.Residual != planner.DeltaResidualStep {
		t.Fatalf("increase must be floored to step: %s %s", q, buy.Residual)
	}
	if s := batch.Skipped[0]; s.Symbol != "XRPUSDT" || s.Reason != planner.DeltaResidualStep || s.ReduceOnly {
		t.Fatalf("dust not reported: %#v", s)
	}
	link := regexp.MustCompile(`^cm-[0-9a-f]{32}$`)
	for _, in := range batch.Intents {
		if !link.MatchString(in.OrderLinkID) || len(in.OrderLinkID) > MaxOrderLinkIDLen || len(in.IntentID) != 64 ||
			in.PlanHash != plan.Hash() || in.PlanRevision != 1 || in.RunID != "run-1" || in.AccountID != "acc" {
			t.Fatalf("bad identity: %#v", in)
		}
	}
	again, _ := build(t, plan, rules)
	if again.Intents[0].OrderLinkID != sell.OrderLinkID || again.Intents[1].IntentID != buy.IntentID || sell.OrderLinkID == buy.OrderLinkID {
		t.Fatal("identifiers are not deterministic or not unique")
	}
	want := `{"category":"linear","symbol":"ADAUSDT","side":"Sell","orderType":"Market","qty":"6","positionIdx":0,"reduceOnly":true,"orderLinkId":"` + sell.OrderLinkID + `"}`
	if got := wire(t, sell); got != want {
		t.Fatalf("sell wire:\n%s\n%s", got, want)
	}
	want = `{"category":"linear","symbol":"SOLUSDT","side":"Buy","orderType":"Market","qty":"2.5","positionIdx":0,"reduceOnly":false,"orderLinkId":"` + buy.OrderLinkID + `"}`
	if got := wire(t, buy); got != want {
		t.Fatalf("buy wire:\n%s\n%s", got, want)
	}
}

// TestBuildOrderIntentsRejects проверяет fail-closed ветви согласования входа.
func TestBuildOrderIntentsRejects(t *testing.T) {
	plan, rules := fixture(t, "paper")
	if _, err := build(t, plan, rules); !errors.Is(err, ErrIntentEnvironment) {
		t.Fatalf("paper: %v", err)
	}
	plan, rules = fixture(t, "testnet")
	projection, _ := planner.ProjectBaselineDeltas(symbols, plan, rules)
	other, _ := fixture(t, "paper")
	if _, err := BuildOrderIntents(other, projection, rules); !errors.Is(err, ErrInvalidIntentInput) {
		t.Fatalf("foreign projection: %v", err)
	}
	if _, err := BuildOrderIntents(plan, planner.DeltaProjection{}, rules); !errors.Is(err, ErrInvalidIntentInput) {
		t.Fatalf("empty projection: %v", err)
	}
	for name, mutate := range map[string]func(*planner.MarketDeltaRules){
		"coarser step": func(r *planner.MarketDeltaRules) { r.QtyStep = dec(t, "4") },
		"lower max":    func(r *planner.MarketDeltaRules) { r.MaxMarketQty = dec(t, "1") },
		"higher min":   func(r *planner.MarketDeltaRules) { r.MinQty = dec(t, "7") },
		"notional":     func(r *planner.MarketDeltaRules) { r.MinIncreaseNotional = dec(t, "1000") },
	} {
		changed := map[string]planner.MarketDeltaRules{}
		for symbol, rule := range rules {
			mutate(&rule)
			changed[symbol] = rule
		}
		if _, err := BuildOrderIntents(plan, projection, changed); !errors.Is(err, ErrInvalidIntentInput) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	delete(rules, "ADAUSDT")
	if _, err := BuildOrderIntents(plan, projection, rules); !errors.Is(err, ErrInvalidIntentInput) {
		t.Fatalf("missing rule: %v", err)
	}
}

// TestEncodeCreateOrder проверяет golden с slippage и отказ при несогласованном намерении.
func TestEncodeCreateOrder(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	link, err := OrderLinkID(hash, 3, "XRPUSDT", LegIncrease)
	if err != nil || link != "cm-79c6e49e83ea4a3f98ad41f04ecfb742" {
		t.Fatalf("link golden: %s %v", link, err)
	}
	in := OrderIntent{OrderLinkID: link, Environment: "testnet", PlanRevision: 3, PlanHash: hash, Symbol: "XRPUSDT",
		Leg: LegIncrease, Side: model.FillBuy, Quantity: dec(t, "12.340")}
	raw, err := EncodeCreateOrder(in, Slippage{Type: SlippagePercent, Tolerance: dec(t, "0.50")})
	want := `{"category":"linear","symbol":"XRPUSDT","side":"Buy","orderType":"Market","qty":"12.34","positionIdx":0,"reduceOnly":false,"orderLinkId":"` + link + `","slippageToleranceType":"Percent","slippageTolerance":"0.5"}`
	if err != nil || string(raw) != want || strings.Contains(string(raw), "stopLoss") || strings.Contains(string(raw), "takeProfit") {
		t.Fatalf("golden:\n%s\n%s %v", raw, want, err)
	}
	for name, tc := range map[string]struct {
		mutate func(*OrderIntent)
		slip   Slippage
		want   error
	}{
		"reduce flag":   {func(i *OrderIntent) { i.ReduceOnly = true }, Slippage{}, ErrInvalidWireIntent},
		"side":          {func(i *OrderIntent) { i.Side = model.FillSell }, Slippage{}, ErrInvalidWireIntent},
		"foreign link":  {func(i *OrderIntent) { i.OrderLinkID = "cm-x" }, Slippage{}, ErrInvalidWireIntent},
		"zero qty":      {func(i *OrderIntent) { i.Quantity = model.Zero() }, Slippage{}, ErrInvalidWireIntent},
		"no qty":        {func(i *OrderIntent) { i.Quantity = model.Decimal{} }, Slippage{}, ErrInvalidWireIntent},
		"mainnet":       {func(i *OrderIntent) { i.Environment = "mainnet" }, Slippage{}, ErrIntentEnvironment},
		"slip type":     {func(*OrderIntent) {}, Slippage{Type: "Bps", Tolerance: dec(t, "1")}, ErrInvalidSlippage},
		"slip negative": {func(*OrderIntent) {}, Slippage{Type: SlippageTickSize, Tolerance: dec(t, "-5")}, ErrInvalidSlippage},
		"slip missing":  {func(*OrderIntent) {}, Slippage{Type: SlippageTickSize}, ErrInvalidSlippage},
	} {
		changed := in
		tc.mutate(&changed)
		if _, err := EncodeCreateOrder(changed, tc.slip); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, bad := range []func() (string, error){
		func() (string, error) { return OrderLinkID(strings.ToUpper(hash), 3, "XRPUSDT", LegIncrease) },
		func() (string, error) { return OrderLinkID(hash, 0, "XRPUSDT", LegIncrease) },
		func() (string, error) { return OrderLinkID(hash, 3, " ", LegIncrease) },
		func() (string, error) { return OrderLinkID(hash, 3, "XRPUSDT", "close") },
	} {
		if _, err := bad(); !errors.Is(err, ErrInvalidIntentInput) {
			t.Fatalf("bad link input accepted: %v", err)
		}
	}
}
