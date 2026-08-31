package execution

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"bybit_bot_liquidity/internal/exchange"
)

var ErrInvalidOrderContract = errors.New("invalid order contract")

// ValidateOpeningOrder makes the semantics of a future POST /v5/order/create
// explicit before any private transport is enabled. A retry must reuse LinkID;
// attached TP/SL belong to the opening intent, while close orders are always
// reduce-only and are intentionally outside this MVP contract.
func ValidateOpeningOrder(order exchange.OrderRequest) error {
	if strings.TrimSpace(order.LinkID) == "" || len(order.LinkID) > 36 {
		return fmt.Errorf("%w: order link id is required and must be at most 36 characters", ErrInvalidOrderContract)
	}
	if strings.TrimSpace(order.Symbol) == "" || (order.Side != exchange.SideLong && order.Side != exchange.SideShort) {
		return fmt.Errorf("%w: symbol and side are required", ErrInvalidOrderContract)
	}
	for name, value := range map[string]float64{"qty": order.Qty, "stop loss": order.StopLoss, "take profit": order.TakeProfit} {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%w: %s must be positive and finite", ErrInvalidOrderContract, name)
		}
	}
	return nil
}

// CanTransitionOrderStatus allows only monotonic local lifecycle transitions.
// Unknown is terminal until external reconciliation supplies a definitive state.
func CanTransitionOrderStatus(from, to exchange.OrderStatus) bool {
	if from == exchange.OrderStatusIntent {
		return to == exchange.OrderStatusSubmitted || to == exchange.OrderStatusUnknown
	}
	if from == exchange.OrderStatusSubmitted {
		return to == exchange.OrderStatusAccepted || to == exchange.OrderStatusFilled || to == exchange.OrderStatusCancelled || to == exchange.OrderStatusRejected || to == exchange.OrderStatusUnknown
	}
	if from == exchange.OrderStatusAccepted {
		return to == exchange.OrderStatusFilled || to == exchange.OrderStatusCancelled || to == exchange.OrderStatusRejected || to == exchange.OrderStatusUnknown
	}
	return false
}
