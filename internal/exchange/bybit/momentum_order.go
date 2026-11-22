package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrOrderLinkDuplicate означает, что заявка с этим orderLinkId уже есть на бирже:
	// это не отказ и не исполнение — вызывающий обязан найти её через историю.
	ErrOrderLinkDuplicate = errors.New("bybit order link id already exists")
	// ErrOrderRejected означает однозначный отказ биржи принять заявку.
	ErrOrderRejected = errors.New("bybit order rejected")
	// ErrOrderNotCancellable означает, что заявки нет среди активных (исполнена,
	// отменена или неизвестна); остаток нужно проверить по истории.
	ErrOrderNotCancellable = errors.New("bybit order is not active")
	// ErrOrderAckMalformed означает ответ без ожидаемой identity заявки.
	ErrOrderAckMalformed = errors.New("bybit order acknowledgement is malformed")
)

// OrderAck — подтверждение приёма запроса биржей. Это не исполнение и не отмена:
// итог заявки приходит отдельно через private WS или историю.
type OrderAck struct {
	OrderID, OrderLinkID string
	ServerTime           time.Time
}

// CreateMomentumOrder отправляет готовое тело /v5/order/create один раз, без автоповтора.
// ErrPostOutcomeUnknown означает неизвестный исход: заявка могла быть создана.
func (c *Client) CreateMomentumOrder(ctx context.Context, request json.RawMessage) (OrderAck, error) {
	var fields struct {
		Category    string `json:"category"`
		Symbol      string `json:"symbol"`
		OrderLinkID string `json:"orderLinkId"`
	}
	if err := json.Unmarshal(request, &fields); err != nil || fields.Category != "linear" || fields.OrderLinkID == "" ||
		validateSymbol(fields.Symbol) != nil {
		return OrderAck{}, fmt.Errorf("create order: %w: request must carry linear symbol and orderLinkId", ErrInvalidArgument)
	}
	result, err := c.PostMomentumTestnetSigned(ctx, "/v5/order/create", request)
	if err != nil {
		return OrderAck{}, classifyOrderError("create order", err)
	}
	return decodeOrderAck("create order", result, fields.OrderLinkID)
}

// CancelMomentumOrder запрашивает отмену по orderLinkId. Ack отмены не доказывает
// нулевой остаток: заявка могла частично исполниться до отмены.
func (c *Client) CancelMomentumOrder(ctx context.Context, symbol, orderLinkID string) (OrderAck, error) {
	if validateSymbol(symbol) != nil || orderLinkID == "" {
		return OrderAck{}, fmt.Errorf("cancel order: %w: symbol and orderLinkId are required", ErrInvalidArgument)
	}
	body := map[string]string{"category": "linear", "symbol": symbol, "orderLinkId": orderLinkID}
	result, err := c.PostMomentumTestnetSigned(ctx, "/v5/order/cancel", body)
	if err != nil {
		return OrderAck{}, classifyOrderError("cancel order", err)
	}
	return decodeOrderAck("cancel order", result, orderLinkID)
}

// classifyOrderError переводит известные retCode Bybit в typed ошибки, сохраняя исходную причину.
func classifyOrderError(op string, err error) error {
	var api *APIError
	if !errors.As(err, &api) || errors.Is(err, ErrPostOutcomeUnknown) {
		return fmt.Errorf("%s: %w", op, err)
	}
	switch api.Code {
	case 110072: // OrderLinkedID is duplicate
		return fmt.Errorf("%s: %w: %w", op, ErrOrderLinkDuplicate, err)
	case 10014: // invalid duplicate request: исход первого запроса неизвестен — сверять, не отказ
		return fmt.Errorf("%s: %w: %w", op, ErrPostOutcomeUnknown, err)
	// 110001 — заявки нет или поздно отменять; 110008 — заявка уже исполнена
	// или отменена; 110010 — уже отменена. Во всех случаях заявка неактивна, и
	// итог нужно сверить по истории, а не считать обычным отказом запроса.
	case 110001, 110008, 110010:
		return fmt.Errorf("%s: %w: %w", op, ErrOrderNotCancellable, err)
	}
	return fmt.Errorf("%s: %w: %w", op, ErrOrderRejected, err)
}

// decodeOrderAck проверяет, что ответ относится к той же заявке.
func decodeOrderAck(op string, result MomentumPostResult, orderLinkID string) (OrderAck, error) {
	var ack struct {
		OrderID     string `json:"orderId"`
		OrderLinkID string `json:"orderLinkId"`
	}
	if err := json.Unmarshal(result.Result, &ack); err != nil || ack.OrderID == "" || ack.OrderLinkID != orderLinkID {
		return OrderAck{}, fmt.Errorf("%s: %w: %w", op, ErrPostOutcomeUnknown, ErrOrderAckMalformed)
	}
	return OrderAck{OrderID: ack.OrderID, OrderLinkID: ack.OrderLinkID, ServerTime: result.ServerTime}, nil
}
