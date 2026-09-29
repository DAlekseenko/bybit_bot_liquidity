package execution

import (
	"errors"
	"math"
	"testing"

	"bybit_bot_liquidity/internal/exchange"
)

// TestValidateOpeningOrder проверяет границы формы и отсутствие мутаций.
func TestValidateOpeningOrder(t *testing.T) {
	valid := exchange.OrderRequest{LinkID: "btc-20260928-0001", Symbol: "BTCUSDT", Side: exchange.SideLong, Qty: 1, StopLoss: 99, TakeProfit: 102}
	cases := []struct {
		name  string
		order exchange.OrderRequest
	}{
		{"valid", valid},
		{"missing link id", exchange.OrderRequest{Symbol: "BTCUSDT", Side: exchange.SideLong, Qty: 1, StopLoss: 99, TakeProfit: 102}},
		{"long link id", exchange.OrderRequest{LinkID: string(make([]byte, 37)), Symbol: "BTCUSDT", Side: exchange.SideLong, Qty: 1, StopLoss: 99, TakeProfit: 102}},
		{"invalid symbol", exchange.OrderRequest{LinkID: valid.LinkID, Symbol: "btc-usdt", Side: valid.Side, Qty: 1, StopLoss: 99, TakeProfit: 102}},
		{"invalid side", exchange.OrderRequest{LinkID: valid.LinkID, Symbol: valid.Symbol, Side: "BUY", Qty: 1, StopLoss: 99, TakeProfit: 102}},
		{"zero qty", exchange.OrderRequest{LinkID: valid.LinkID, Symbol: valid.Symbol, Side: valid.Side, Qty: 0, StopLoss: 99, TakeProfit: 102}},
		{"negative stop", exchange.OrderRequest{LinkID: valid.LinkID, Symbol: valid.Symbol, Side: valid.Side, Qty: 1, StopLoss: -1, TakeProfit: 102}},
		{"infinite target", exchange.OrderRequest{LinkID: valid.LinkID, Symbol: valid.Symbol, Side: valid.Side, Qty: 1, StopLoss: 99, TakeProfit: math.Inf(1)}},
	}
	for _, test := range cases {
		err := ValidateOpeningOrder(test.order)
		if (test.name == "valid") != (err == nil) || (err != nil && !errors.Is(err, ErrInvalidOrderContract)) {
			t.Errorf("%s: error = %v", test.name, err)
		}
	}
	before := valid
	if err := ValidateOpeningOrder(valid); err != nil || valid != before {
		t.Fatalf("validation changed input or failed: order=%+v err=%v", valid, err)
	}
}

// TestOpeningOrderLinkID проверяет устойчивость ключа к ID intent и полям заявки.
func TestOpeningOrderLinkID(t *testing.T) {
	order := exchange.OrderRequest{LinkID: "old", Symbol: "BTCUSDT", Side: exchange.SideLong, Qty: 1, StopLoss: 99, TakeProfit: 102}
	before := order
	first, err := OpeningOrderLinkID("intent-1", order)
	if err != nil || order != before {
		t.Fatalf("input changed or generation failed: %+v, %v", order, err)
	}
	order.LinkID = "other"
	second, err := OpeningOrderLinkID("intent-1", order)
	if err != nil || first != second || len(first) != 36 {
		t.Fatalf("IDs = %q, %q; err = %v", first, second, err)
	}
	otherIntent, err := OpeningOrderLinkID("intent-2", order)
	if err != nil || first == otherIntent {
		t.Fatalf("different intent IDs produced %q and %q: %v", first, otherIntent, err)
	}
	order.Qty++
	changedIntent, err := OpeningOrderLinkID("intent-1", order)
	if err != nil || first == changedIntent {
		t.Fatalf("changed intent fields produced %q and %q: %v", first, changedIntent, err)
	}
	for _, invalid := range []struct {
		id    string
		order exchange.OrderRequest
	}{{"", order}, {" padded ", order}, {"intent-1", exchange.OrderRequest{Symbol: "btc", Side: exchange.SideLong, Qty: 1, StopLoss: 1, TakeProfit: 2}}} {
		if _, err := OpeningOrderLinkID(invalid.id, invalid.order); !errors.Is(err, ErrInvalidOrderContract) {
			t.Errorf("invalid intent returned %v", err)
		}
	}
}
