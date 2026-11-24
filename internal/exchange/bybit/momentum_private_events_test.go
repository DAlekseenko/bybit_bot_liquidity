package bybit

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// privateData кодирует элементы топика в JSON-массив data.
func privateData(t *testing.T, items ...map[string]string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// executionItem возвращает корректное поле execution.linear с заданными execId и execType.
func executionItem(id, execType string) map[string]string {
	return map[string]string{"category": "linear", "symbol": "BTCUSDT", "side": "Buy", "orderId": "o1",
		"orderLinkId": "l1", "execId": id, "execType": execType, "execPrice": "90000.5", "execQty": "0.004",
		"execFee": "-0.01", "execTime": "1700000002000"}
}

// TestParsePrivateEvents проверяет типизированные события, повторы, пропуск Funding и неизвестных топиков.
func TestParsePrivateEvents(t *testing.T) {
	events, err := parsePrivateEvents("execution.linear", privateData(t,
		executionItem("e2", "Trade"), executionItem("e2", "Trade"), executionItem("e1", "AdlTrade"), executionItem("f1", "Funding")))
	if err != nil || len(events) != 3 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	var ids []string
	for _, e := range events {
		ids = append(ids, e.Fill.ExecutionID+":"+e.ExecType)
	}
	if strings.Join(ids, ",") != "e2:Trade,e2:Trade,e1:AdlTrade" {
		t.Fatalf("ids=%v", ids)
	}
	e := events[0]
	fee, _ := e.Fill.FeeUSDT.WireDecimal()
	qty, _ := e.Fill.Quantity.WireDecimal()
	if e.Kind != PrivateExecution || e.OrderID != "o1" || e.OrderLinkID != "l1" || fee != "-0.01" || qty != "0.004" ||
		!e.Fill.ExecutedAt.Equal(time.UnixMilli(1700000002000).UTC()) || e.Fill.ExecutedAt.Location() != time.UTC {
		t.Fatalf("execution=%+v", e)
	}
	orders, err := parsePrivateEvents("order.linear", privateData(t, map[string]string{"category": "linear",
		"symbol": "BTCUSDT", "orderId": "o1", "orderStatus": "PartiallyFilled", "cumExecQty": "0.008", "updatedTime": "1700000004000"}))
	if err != nil || len(orders) != 1 || orders[0].Kind != PrivateOrder || orders[0].Status != "PartiallyFilled" {
		t.Fatalf("orders=%+v err=%v", orders, err)
	}
	if cum, _ := orders[0].CumExecQty.WireDecimal(); cum != "0.008" {
		t.Fatalf("cumExecQty=%s", cum)
	}
	if other, err := parsePrivateEvents("wallet", json.RawMessage(`garbage`)); err != nil || other != nil {
		t.Fatalf("unknown topic: %v %v", other, err)
	}
}

// TestParsePrivateEventsMalformed проверяет, что некорректное событие отвергается целиком.
func TestParsePrivateEventsMalformed(t *testing.T) {
	for name, mutate := range map[string]func(map[string]string){
		"zero qty":    func(m map[string]string) { m["execQty"] = "0" },
		"bad side":    func(m map[string]string) { m["side"] = "Long" },
		"no exec id":  func(m map[string]string) { m["execId"] = "" },
		"bad time":    func(m map[string]string) { m["execTime"] = "0" },
		"bad price":   func(m map[string]string) { m["execPrice"] = "x" },
		"spot":        func(m map[string]string) { m["category"] = "spot" },
		"no order id": func(m map[string]string) { m["orderId"] = "" },
	} {
		item := executionItem("e1", "Trade")
		mutate(item)
		if _, err := parsePrivateEvents("execution.linear", privateData(t, executionItem("ok", "Trade"), item)); !errors.Is(err, ErrPrivateStreamData) || !errors.Is(err, ErrPrivateStream) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if _, err := parsePrivateEvents("order.linear", privateData(t, map[string]string{"category": "linear",
		"symbol": "BTCUSDT", "orderId": "o1", "cumExecQty": "0", "updatedTime": "1700000004000"})); !errors.Is(err, ErrPrivateStreamData) {
		t.Errorf("order without status: %v", err)
	}
	if _, err := parsePrivateEvents("order.linear", json.RawMessage(`{"not":"array"}`)); !errors.Is(err, ErrPrivateStreamData) {
		t.Errorf("bad json: %v", err)
	}
}

// TestPrivateStreamResyncGeneration проверяет, что resync прежнего подключения не делает свежим новое.
func TestPrivateStreamResyncGeneration(t *testing.T) {
	state := &PrivateStreamState{}
	if state.Fresh() || state.MarkResynced(state.Generation()) {
		t.Fatal("never connected stream must not be fresh")
	}
	state.set(true)
	if state.Fresh() {
		t.Fatal("connected stream must not be fresh before REST resync")
	}
	stale := state.Generation()
	state.set(false)
	state.set(true)
	if state.MarkResynced(stale) || state.Fresh() {
		t.Fatal("resync started on a previous connection must not mark the new one fresh")
	}
	if !state.MarkResynced(state.Generation()) || !state.Fresh() {
		t.Fatal("resync of the current connection must mark it fresh")
	}
	state.set(false)
	if state.MarkResynced(state.Generation()) || state.Fresh() {
		t.Fatal("disconnected stream must not become fresh")
	}
}

// TestPrivateStreamFeeCurrency проверяет, что fill с комиссией не в USDT отвергается.
func TestPrivateStreamFeeCurrency(t *testing.T) {
	item := privateItem{Category: "linear", Symbol: "BTCUSDT", Side: "Buy", OrderID: "o", ExecID: "e", ExecType: "Trade",
		ExecPrice: "1", ExecQty: "1", ExecFee: "0.1", ExecTime: "1700000001000"}
	for _, tc := range []struct {
		symbol, currency string
		ok               bool
	}{{"BTCUSDT", "USDT", true}, {"BTCUSDT", "", true}, {"BTCUSDT", "USDC", false}, {"BTCPERP", "", false}, {"BTCPERP", "USDC", false}} {
		it := item
		it.Symbol, it.FeeCurrency = tc.symbol, tc.currency
		if e, ok := privateEvent("execution.linear", it); ok != tc.ok || (ok && e.Kind != PrivateExecution) {
			t.Errorf("%s/%q: ok=%v kind=%s, want ok=%v", tc.symbol, tc.currency, ok, e.Kind, tc.ok)
		}
	}
}
