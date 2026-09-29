package execution

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"bybit_bot_liquidity/internal/exchange"
)

// ErrInvalidOrderContract сообщает, что форма opening order некорректна.
var ErrInvalidOrderContract = errors.New("invalid order contract")

// OpeningOrderLinkID вычисляет ключ по стабильному ID и проверенной форме intent.
func OpeningOrderLinkID(intentID string, order exchange.OrderRequest) (string, error) {
	if len(intentID) == 0 || len(intentID) > 128 || strings.TrimSpace(intentID) != intentID {
		return "", fmt.Errorf("%w: intent id must be trimmed and 1..128 bytes", ErrInvalidOrderContract)
	}
	order.LinkID = "generated"
	if err := ValidateOpeningOrder(order); err != nil {
		return "", err
	}
	var canonical []byte
	for _, field := range []string{intentID, order.Symbol, string(order.Side)} {
		canonical = binary.AppendUvarint(canonical, uint64(len(field)))
		canonical = append(canonical, field...)
	}
	for _, value := range [...]float64{order.Qty, order.StopLoss, order.TakeProfit} {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], math.Float64bits(value))
		canonical = append(canonical, encoded[:]...)
	}
	digest := sha256.Sum256(canonical)
	return "i-" + fmt.Sprintf("%x", digest[:17]), nil
}

// ValidateOpeningOrder проверяет только форму заявки.
func ValidateOpeningOrder(order exchange.OrderRequest) error {
	if strings.TrimSpace(order.LinkID) == "" || len(order.LinkID) > 36 {
		return fmt.Errorf("%w: link id is required and must be at most 36 bytes", ErrInvalidOrderContract)
	}
	if order.Symbol == "" || strings.TrimSpace(order.Symbol) != order.Symbol {
		return fmt.Errorf("%w: symbol is required in uppercase ASCII", ErrInvalidOrderContract)
	}
	for _, char := range order.Symbol {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return fmt.Errorf("%w: symbol is required in uppercase ASCII", ErrInvalidOrderContract)
		}
	}
	if order.Side != exchange.SideLong && order.Side != exchange.SideShort {
		return fmt.Errorf("%w: side must be LONG or SHORT", ErrInvalidOrderContract)
	}
	for _, value := range []struct {
		name  string
		value float64
	}{{"qty", order.Qty}, {"stop loss", order.StopLoss}, {"take profit", order.TakeProfit}} {
		if value.value <= 0 || math.IsNaN(value.value) || math.IsInf(value.value, 0) {
			return fmt.Errorf("%w: %s must be positive and finite", ErrInvalidOrderContract, value.name)
		}
	}
	return nil
}
