package execution

import (
	"slices"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

var preflightNow = time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)

// dec разбирает точное значение для теста.
func dec(t *testing.T, s string) model.Decimal {
	t.Helper()
	d, err := model.ParseAmount(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// preflightFixture возвращает чистый счёт, где вход разрешён ровно на границе маржи.
func preflightFixture(t *testing.T) (PreflightInput, PreflightParams) {
	t.Helper()
	at := preflightNow.Add(-time.Second)
	btc := model.AccountPosition{Symbol: "BTCUSDT", Side: "Buy", Size: dec(t, "0.01"), Leverage: dec(t, "1"), Status: "Normal", ServerTime: at}
	in := PreflightInput{
		Mode:             model.AccountMode{UnifiedMarginStatus: 5, MarginMode: "ISOLATED_MARGIN", ServerTime: at},
		Balance:          model.IsolatedBalance{Coin: "USDT", Available: dec(t, "1515"), ServerTime: at},
		Inventory:        model.AccountInventory{Positions: []model.AccountPosition{btc}, Orders: []model.AccountOrder{}, ServerTime: at},
		Symbols:          []string{"BTCUSDT", "ETHUSDT"},
		Expected:         map[string]model.Decimal{"BTCUSDT": dec(t, "0.01")},
		InstrumentStatus: map[string]string{"BTCUSDT": "Trading", "ETHUSDT": "Trading"},
		Marks:            map[string]MarkObservation{"ETHUSDT": {Price: dec(t, "3000"), Time: at}},
		Deltas: []planner.PositionDelta{
			{Symbol: "BTCUSDT", ReduceOnly: true, CandidateQty: dec(t, "0.005")},
			{Symbol: "ETHUSDT", CandidateQty: dec(t, "0.5")},
		},
	}
	p := PreflightParams{Now: preflightNow, MaxDataAge: 5 * time.Second, TargetLeverage: dec(t, "1"),
		MarginBufferRate: dec(t, "0.01"), OrderLinkNamespace: "cm-"}
	return in, p
}

// TestPreflightAllowsCleanAccount проверяет разрешение и точную требуемую маржу на границе.
func TestPreflightAllowsCleanAccount(t *testing.T) {
	in, p := preflightFixture(t)
	got := Preflight(in, p)
	wire, err := got.RequiredMargin.WireDecimal()
	if !got.Allowed || len(got.Denials) != 0 || err != nil || wire != "1515" {
		t.Fatalf("result = %+v, %q", got, wire)
	}
}

// TestPreflightDenies проверяет каждую причину запрета на отдельной мутации.
func TestPreflightDenies(t *testing.T) {
	cases := map[string]struct {
		mutate func(*testing.T, *PreflightInput, *PreflightParams)
		want   PreflightReason
	}{
		"no now":          {func(_ *testing.T, _ *PreflightInput, p *PreflightParams) { p.Now = time.Time{} }, ReasonInvalidParameters},
		"no max age":      {func(_ *testing.T, _ *PreflightInput, p *PreflightParams) { p.MaxDataAge = 0 }, ReasonInvalidParameters},
		"no leverage":     {func(_ *testing.T, _ *PreflightInput, p *PreflightParams) { p.TargetLeverage = model.Decimal{} }, ReasonInvalidParameters},
		"no buffer":       {func(_ *testing.T, _ *PreflightInput, p *PreflightParams) { p.MarginBufferRate = model.Decimal{} }, ReasonInvalidParameters},
		"negative buffer": {func(t *testing.T, _ *PreflightInput, p *PreflightParams) { p.MarginBufferRate = dec(t, "-0.1") }, ReasonInvalidParameters},
		"no namespace":    {func(_ *testing.T, _ *PreflightInput, p *PreflightParams) { p.OrderLinkNamespace = "" }, ReasonInvalidParameters},
		"no symbols":      {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) { in.Symbols = nil }, ReasonInvalidParameters},
		"stale balance": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Balance.ServerTime = preflightNow.Add(-6 * time.Second)
		}, ReasonStaleData},
		"future mode": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Mode.ServerTime = preflightNow.Add(time.Second)
		}, ReasonStaleData},
		"zero inventory": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) { in.Inventory.ServerTime = time.Time{} }, ReasonStaleData},
		"stale mark": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Marks["ETHUSDT"] = MarkObservation{in.Marks["ETHUSDT"].Price, preflightNow.Add(-time.Hour)}
		}, ReasonStaleData},
		"regular margin": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) { in.Mode.MarginMode = "REGULAR_MARGIN" }, ReasonMarginMode},
		"hedge":          {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) { in.Inventory.Positions[0].PositionIdx = 1 }, ReasonPositionMode},
		"unknown symbol": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Inventory.Positions[0].Symbol = "XRPUSDT"
		}, ReasonUnknownPosition},
		"qty mismatch": {func(t *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Inventory.Positions[0].Size = dec(t, "0.02")
		}, ReasonPositionMismatch},
		"short position":   {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) { in.Inventory.Positions[0].Side = "Sell" }, ReasonPositionMismatch},
		"missing position": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) { in.Inventory.Positions = nil }, ReasonPositionMismatch},
		"leverage": {func(t *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Inventory.Positions[0].Leverage = dec(t, "2")
		}, ReasonLeverageMismatch},
		"auto add": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Inventory.Positions[0].AutoAddMargin = true
		}, ReasonAutoAddMargin},
		"liq status": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) { in.Inventory.Positions[0].Status = "Liq" }, ReasonPositionStatus},
		"manual order": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Inventory.Orders = []model.AccountOrder{{OrderID: "1", OrderLinkID: ""}}
		}, ReasonUnknownOrder},
		"owned order": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Inventory.Orders = []model.AccountOrder{{OrderID: "2", OrderLinkID: "cm-x"}}
		}, ReasonOwnedOrderOpen},
		"unsupported delta": {func(t *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Deltas = append(in.Deltas, planner.PositionDelta{Symbol: "XRPUSDT", CandidateQty: dec(t, "1")})
		}, ReasonUnsupportedSymbol},
		"instrument": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.InstrumentStatus["ETHUSDT"] = "Settling"
		}, ReasonInstrumentStatus},
		"no mark": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) { delete(in.Marks, "ETHUSDT") }, ReasonMissingMark},
		"bad qty": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Deltas[1].CandidateQty = model.Decimal{}
		}, ReasonInvalidParameters},
		"no balance": {func(_ *testing.T, in *PreflightInput, _ *PreflightParams) { in.Balance.Available = model.Decimal{} }, ReasonUnavailableBalance},
		"one below": {func(t *testing.T, in *PreflightInput, _ *PreflightParams) {
			in.Balance.Available = dec(t, "1514.999999")
		}, ReasonInsufficientMargin},
		"zero available": {func(t *testing.T, in *PreflightInput, _ *PreflightParams) { in.Balance.Available = dec(t, "0") }, ReasonInsufficientMargin},
	}
	for name, tc := range cases {
		in, p := preflightFixture(t)
		tc.mutate(t, &in, &p)
		got := Preflight(in, p)
		if got.Allowed || !slices.ContainsFunc(got.Denials, func(d PreflightDenial) bool { return d.Reason == tc.want }) {
			t.Errorf("%s: want %s, got %+v", name, tc.want, got)
		}
	}
}

// TestPreflightReduceOnlyNeedsNoMargin проверяет, что сокращения не требуют маржи и mark.
func TestPreflightReduceOnlyNeedsNoMargin(t *testing.T) {
	in, p := preflightFixture(t)
	in.Deltas = in.Deltas[:1]
	in.Marks = nil
	in.Balance.Available = dec(t, "0")
	if got := Preflight(in, p); !got.Allowed {
		t.Fatalf("reduce-only preflight denied: %+v", got)
	}
}
