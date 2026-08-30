package exchange

import "testing"

func TestLiquidationUSDValue(t *testing.T) {
	liquidation := Liquidation{Price: 50000, Qty: 0.25}

	if got := liquidation.USDValue(); got != 12500 {
		t.Fatalf("USDValue() = %v, want 12500", got)
	}
}
