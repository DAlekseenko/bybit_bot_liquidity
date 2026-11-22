package execution

import (
	"strings"
	"testing"

	"bybit_bot_liquidity/internal/momentum/model"
)

// startupBase возвращает согласованный снимок: одна исполненная покупка и такая же позиция на бирже.
func startupBase(t *testing.T) StartupInput {
	return StartupInput{
		Namespace: "cm-", LatestPlanHash: "p2",
		Intents: []StoredIntent{{OrderLinkID: "cm-done", Symbol: "BTCUSDT", PlanHash: "p1", Side: model.FillBuy,
			State: StateFilled, Quantity: dec(t, "0.01"), FillIDs: map[string]bool{"e0": true}}},
		Fills:     []BrokerFill{{OrderLinkID: "cm-done", Fill: fill(t, "e0", "0.01").Fill}},
		Inventory: model.AccountInventory{Positions: []model.AccountPosition{{Symbol: "BTCUSDT", Side: "Buy", Size: dec(t, "0.01")}}},
		Positions: map[string]model.Decimal{"BTCUSDT": dec(t, "0.01")},
		Lookups:   map[string]BrokerLookup{},
	}
}

// findings сводит расхождения в строку kind:subject.
func findings(r StartupReport) string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, string(f.Kind)+":"+f.Subject)
	}
	return strings.Join(out, ",")
}

// active добавляет неурегулированное намерение покупки 0.004.
func active(t *testing.T, in *StartupInput, link, plan string, state OrderState) {
	in.Intents = append(in.Intents, StoredIntent{OrderLinkID: link, Symbol: "BTCUSDT", PlanHash: plan, Side: model.FillBuy,
		State: state, Quantity: dec(t, "0.004"), FillIDs: map[string]bool{}})
}

// TestReconcileStartupClean проверяет, что согласованный снимок разрешает входы без починки.
func TestReconcileStartupClean(t *testing.T) {
	r := ReconcileStartup(startupBase(t))
	if !r.EntriesAllowed || len(r.Repairs) != 0 || len(r.Findings) != 0 {
		t.Fatalf("clean=%+v", r)
	}
}

// TestReconcileStartupRestarts проверяет рестарт после send-before-ack и fill-before-commit.
func TestReconcileStartupRestarts(t *testing.T) {
	in := startupBase(t)
	active(t, &in, "cm-ack", "p2", StateSent)
	in.Lookups["cm-ack"] = BrokerLookup{Found: true, Status: "Filled", CumExecQty: dec(t, "0.004")}
	in.Fills = append(in.Fills, BrokerFill{OrderLinkID: "cm-ack", Fill: fill(t, "e1", "0.004").Fill})
	in.Inventory.Positions[0].Size = dec(t, "0.014")
	r := ReconcileStartup(in)
	if !r.EntriesAllowed || len(r.Repairs) != 2 || r.Repairs[0].Kind != RepairRecordFill || r.Repairs[0].Fill.ExecutionID != "e1" ||
		r.Repairs[1].Kind != RepairMarkState || r.Repairs[1].State != StateAcknowledged {
		t.Fatalf("repairs=%+v findings=%s", r.Repairs, findings(r))
	}
	again := ReconcileStartup(in)
	if len(again.Repairs) != len(r.Repairs) {
		t.Fatal("reconcile must be deterministic")
	}
	in = startupBase(t)
	active(t, &in, "cm-lost", "p2", StateUnknown)
	in.Lookups["cm-lost"] = BrokerLookup{Found: false}
	if r = ReconcileStartup(in); r.EntriesAllowed || findings(r) != "send_unconfirmed:cm-lost" || len(r.Repairs) != 0 {
		t.Fatalf("unconfirmed send must block without inferring a fill: %+v", r)
	}
}

// TestReconcileStartupBlocks проверяет устаревший план, чужие позиции и заявки, сироты и пропуск поиска.
func TestReconcileStartupBlocks(t *testing.T) {
	in := startupBase(t)
	active(t, &in, "cm-old", "p1", StateAcknowledged)
	in.Lookups["cm-old"] = BrokerLookup{Found: true, Status: "New", CumExecQty: model.Zero()}
	active(t, &in, "cm-nolookup", "p2", StateSent)
	in.Inventory.Orders = []model.AccountOrder{{OrderID: "x1", OrderLinkID: "cm-ghost", Symbol: "BTCUSDT"},
		{OrderID: "m1", Symbol: "ETHUSDT"}}
	in.Inventory.Positions = append(in.Inventory.Positions, model.AccountPosition{Symbol: "ETHUSDT", Side: "Buy", Size: dec(t, "1")})
	in.Positions["SOLUSDT"] = dec(t, "2")
	in.Fills = append(in.Fills, BrokerFill{OrderLinkID: "cm-unknown", Fill: fill(t, "e9", "0.001").Fill})
	r := ReconcileStartup(in)
	want := "foreign_order:ETHUSDT/m1,foreign_position:ETHUSDT,missing_lookup:cm-nolookup,orphan_order:cm-ghost," +
		"orphan_order:cm-unknown,position_mismatch:SOLUSDT,stale_plan:cm-old"
	if r.EntriesAllowed || findings(r) != want {
		t.Fatalf("findings=%s", findings(r))
	}
	in = startupBase(t)
	in.Inventory.Positions[0].Size = dec(t, "0.02")
	if r = ReconcileStartup(in); findings(r) != "position_mismatch:BTCUSDT" {
		t.Fatalf("size mismatch=%s", findings(r))
	}
}
