package execution

import (
	"errors"
	"testing"

	"bybit_bot_liquidity/internal/exchange"
)

func TestNormalizeOrder(t *testing.T) {
	t.Parallel()

	rules := testInstrumentRules()
	order := exchange.OrderRequest{
		Symbol:     "BTCUSDT",
		Side:       exchange.SideLong,
		Qty:        0.12349,
		StopLoss:   49999.94,
		TakeProfit: 51000.06,
	}

	normalized, err := NormalizeOrder(order, 50000, rules)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Qty != 0.123 {
		t.Fatalf("Qty = %v, want 0.123", normalized.Qty)
	}
	if normalized.StopLoss != 49999.9 || normalized.TakeProfit != 51000.1 {
		t.Fatalf("prices = %v/%v", normalized.StopLoss, normalized.TakeProfit)
	}
}

func TestNormalizeOrderRejectsExchangeLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		order   exchange.OrderRequest
		price   float64
		change  func(*exchange.InstrumentRules)
		wantErr error
	}{
		{
			name:    "quantity rounds below minimum",
			order:   testOrder(0.0009),
			price:   50000,
			wantErr: ErrOrderBelowMinimum,
		},
		{
			name:    "notional below minimum",
			order:   testOrder(0.001),
			price:   1000,
			wantErr: ErrOrderBelowMinimum,
		},
		{
			name:    "quantity above market maximum",
			order:   testOrder(101),
			price:   50000,
			wantErr: ErrOrderAboveMaximum,
		},
		{
			name:    "entry price below minimum",
			order:   testOrder(0.1),
			price:   0.09,
			wantErr: ErrOrderBelowMinimum,
		},
		{
			name:    "entry price above maximum",
			order:   testOrder(0.1),
			price:   1000000.1,
			wantErr: ErrOrderAboveMaximum,
		},
		{
			name:    "instrument not trading",
			order:   testOrder(0.1),
			price:   50000,
			change:  func(rules *exchange.InstrumentRules) { rules.Status = "PreLaunch" },
			wantErr: ErrInstrumentNotTrading,
		},
		{
			name:    "symbol mismatch",
			order:   testOrder(0.1),
			price:   50000,
			change:  func(rules *exchange.InstrumentRules) { rules.Symbol = "ETHUSDT" },
			wantErr: ErrInvalidInstrumentRules,
		},
		{
			name: "protection collapses to entry tick",
			order: exchange.OrderRequest{
				Symbol:     "BTCUSDT",
				Side:       exchange.SideLong,
				Qty:        0.1,
				StopLoss:   49999,
				TakeProfit: 50001,
			},
			price:   50000,
			change:  func(rules *exchange.InstrumentRules) { rules.TickSize = 10 },
			wantErr: ErrInvalidProtectionLevel,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rules := testInstrumentRules()
			if test.change != nil {
				test.change(&rules)
			}
			_, err := NormalizeOrder(test.order, test.price, rules)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NormalizeOrder() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func testOrder(qty float64) exchange.OrderRequest {
	return exchange.OrderRequest{
		Symbol:     "BTCUSDT",
		Side:       exchange.SideLong,
		Qty:        qty,
		StopLoss:   49000,
		TakeProfit: 51000,
	}
}

func testInstrumentRules() exchange.InstrumentRules {
	return exchange.InstrumentRules{
		Symbol:            "BTCUSDT",
		Status:            "Trading",
		TickSize:          0.1,
		MinPrice:          0.1,
		MaxPrice:          1000000,
		QtyStep:           0.001,
		MinOrderQty:       0.001,
		MaxOrderQty:       100,
		MaxMarketOrderQty: 100,
		MinNotionalValue:  5,
	}
}
