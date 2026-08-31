package execution

import (
	"errors"
	"testing"

	"bybit_bot_liquidity/internal/exchange"
)

func TestValidateOpeningOrder(t *testing.T) {
	t.Parallel()
	valid := exchange.OrderRequest{LinkID: "btc-20260831-0001", Symbol: "BTCUSDT", Side: exchange.SideLong, Qty: 1, StopLoss: 99, TakeProfit: 102}
	if err := ValidateOpeningOrder(valid); err != nil {
		t.Fatal(err)
	}
	valid.LinkID = ""
	if err := ValidateOpeningOrder(valid); !errors.Is(err, ErrInvalidOrderContract) {
		t.Fatalf("error = %v", err)
	}
}

func TestCanTransitionOrderStatus(t *testing.T) {
	t.Parallel()
	if !CanTransitionOrderStatus(exchange.OrderStatusSubmitted, exchange.OrderStatusAccepted) {
		t.Fatal("submitted must transition to accepted")
	}
	if CanTransitionOrderStatus(exchange.OrderStatusFilled, exchange.OrderStatusSubmitted) {
		t.Fatal("terminal status must not transition")
	}
}
