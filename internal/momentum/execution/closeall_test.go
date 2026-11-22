package execution

import (
	"testing"

	"bybit_bot_liquidity/internal/momentum/model"
)

// closeInput строит состояние: HALTED, позиция BTCUSDT 0.01, шаг закрытия 0.001.
func closeInput(t *testing.T) CloseAllInput {
	return CloseAllInput{Halted: true, Namespace: "cm-", Symbols: []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"},
		Inventory: model.AccountInventory{Positions: []model.AccountPosition{{Symbol: "BTCUSDT", Side: "Buy", Size: dec(t, "0.01")}}},
		MinQty:    map[string]model.Decimal{"BTCUSDT": dec(t, "0.001"), "ETHUSDT": dec(t, "0.01"), "HYPEUSDT": dec(t, "0.1")},
		Closes:    map[string]Lifecycle{}}
}

// TestCloseAllSequence проверяет halt → cancel → close → verify → CLOSED и повтор без дубля.
func TestCloseAllSequence(t *testing.T) {
	in := closeInput(t)
	in.Halted = false
	if step := NextCloseAll(in); step.Phase != CloseAllPersistHalt || len(step.Close)+len(step.Cancel) != 0 {
		t.Fatalf("halt must be persisted before any action: %+v", step)
	}
	in = closeInput(t)
	in.Inventory.Orders = []model.AccountOrder{{OrderID: "o1", OrderLinkID: "cm-open", Symbol: "ETHUSDT"}}
	if step := NextCloseAll(in); step.Phase != CloseAllCancelling || len(step.Cancel) != 1 || len(step.Close) != 0 {
		t.Fatalf("pending opening order must be cancelled first: %+v", step)
	}
	in.Inventory.Orders = nil
	step := NextCloseAll(in)
	if step.Phase != CloseAllClosing || len(step.Close) != 1 || step.Close[0].Symbol != "BTCUSDT" {
		t.Fatalf("own position must be closed: %+v", step)
	}
	if q, _ := step.Close[0].Quantity.WireDecimal(); q != "0.01" {
		t.Fatalf("close qty=%s", q)
	}
	in.Closes["BTCUSDT"] = apply(t, newOrder(t), Evidence{Kind: EvidenceSent}, Evidence{Kind: EvidenceTimeout})
	if step = NextCloseAll(in); step.Phase != CloseAllVerifying || len(step.Close) != 0 {
		t.Fatalf("unknown close must not be repeated or reported closed: %+v", step)
	}
	in.Closes["BTCUSDT"] = apply(t, newOrder(t), fill(t, "e1", "0.004"), status(t, "PartiallyFilledCanceled", "0.004"))
	in.Inventory.Positions[0].Size = dec(t, "0.006")
	if step = NextCloseAll(in); step.Phase != CloseAllClosing || len(step.Close) != 1 {
		t.Fatalf("partial close after reconnect must close the remainder: %+v", step)
	}
	in.Closes["BTCUSDT"] = apply(t, newOrder(t), Evidence{Kind: EvidenceAccepted, OrderID: "o1"}, fill(t, "e1", "0.01"))
	in.Inventory.Positions = nil
	if step = NextCloseAll(in); step.Phase != CloseAllClosed {
		t.Fatalf("flat account must be CLOSED: %+v", step)
	}
	if again := NextCloseAll(in); again.Phase != CloseAllClosed || len(again.Close) != 0 {
		t.Fatalf("repeated command must not duplicate: %+v", again)
	}
}

// TestCloseAllBlocks проверяет, что dust, чужие позиции и заявки не дают ложный CLOSED и не закрываются.
func TestCloseAllBlocks(t *testing.T) {
	in := closeInput(t)
	in.Inventory.Positions = []model.AccountPosition{{Symbol: "BTCUSDT", Side: "Buy", Size: dec(t, "0.0005")},
		{Symbol: "SOLUSDT", Side: "Buy", Size: dec(t, "3")}}
	in.Inventory.Orders = []model.AccountOrder{{OrderID: "m1", Symbol: "ETHUSDT"}}
	step := NextCloseAll(in)
	if step.Phase != CloseAllBlocked || len(step.Close) != 0 || len(step.Cancel) != 0 {
		t.Fatalf("blocked=%+v", step)
	}
	got := ""
	for _, f := range step.Findings {
		got += string(f.Kind) + ":" + f.Subject + ";"
	}
	if got != "foreign_order:ETHUSDT/m1;foreign_position:SOLUSDT;position_mismatch:BTCUSDT/dust;" {
		t.Fatalf("findings=%s", got)
	}
}
