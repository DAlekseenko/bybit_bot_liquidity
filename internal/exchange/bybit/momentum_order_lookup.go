package bybit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// ErrHistoryData означает некорректную запись истории или запись вне запрошенного окна.
var ErrHistoryData = errors.New("bybit history record is malformed")

// OrderRecord — состояние заявки из realtime или history; Fill не выводится.
type OrderRecord struct {
	OrderID, OrderLinkID, Symbol, Side, Status string
	Qty, CumExecQty                            model.Decimal
	CreatedAt, UpdatedAt                       time.Time
	FromHistory                                bool
}

// HistoryWindow — включающее окно [Start, End] и курсор продолжения.
type HistoryWindow struct {
	Start, End time.Time
	Cursor     string
}

// LookupMomentumOrder ищет заявку по orderLinkId сначала среди активных, затем в
// истории за явное окно [sentAt-1м, sentAt-1м+7д] (без окна Bybit смотрит только
// последние 7 дней). found=false означает, что заявки, отправленной в sentAt, биржа
// не знает; вывод о fill из этого делать нельзя.
func (c *Client) LookupMomentumOrder(ctx context.Context, symbol, orderLinkID string, sentAt time.Time) (OrderRecord, bool, error) {
	if err := validateSymbol(symbol); err != nil || orderLinkID == "" || sentAt.IsZero() {
		return OrderRecord{}, false, fmt.Errorf("%w: symbol, orderLinkId and send time are required", ErrInvalidArgument)
	}
	start := sentAt.Add(-time.Minute)
	queries := map[string]url.Values{
		"/v5/order/realtime": {"category": {"linear"}, "symbol": {symbol}, "orderLinkId": {orderLinkID}},
		"/v5/order/history": {"category": {"linear"}, "symbol": {symbol}, "orderLinkId": {orderLinkID},
			"startTime": {strconv.FormatInt(start.UnixMilli(), 10)},
			"endTime":   {strconv.FormatInt(start.Add(7*24*time.Hour).UnixMilli(), 10)}},
	}
	for _, path := range []string{"/v5/order/realtime", "/v5/order/history"} {
		query := queries[path]
		var result struct {
			List *[]orderItem `json:"list"`
		}
		// Только Testnet-путь без redirect; отсутствующий list — сбой, а не «заявки нет».
		if _, err := c.momentumSignedGet(ctx, path, query, &result); err != nil {
			return OrderRecord{}, false, err
		}
		if result.List == nil {
			return OrderRecord{}, false, fmt.Errorf("%w: %s list is missing", ErrHistoryData, path)
		}
		if len(*result.List) == 0 {
			continue
		}
		it := (*result.List)[0]
		if len(*result.List) > 1 || it.OrderLinkID != orderLinkID || it.Symbol != symbol {
			return OrderRecord{}, false, fmt.Errorf("%w: order %s", ErrHistoryData, path)
		}
		record, err := it.record(path == "/v5/order/history")
		return record, err == nil, err
	}
	return OrderRecord{}, false, nil
}

// historyTime разбирает Unix ms и проверяет попадание во включающее окно.
func historyTime(raw string, w HistoryWindow) (time.Time, error) {
	ms, err := strconv.ParseInt(raw, 10, 64)
	at := time.UnixMilli(ms).UTC()
	if err != nil || ms <= 0 || at.Before(w.Start.Truncate(time.Millisecond)) || at.After(w.End) {
		return time.Time{}, ErrHistoryData
	}
	return at, nil
}

// orderItem — поля заявки Bybit V5.
type orderItem struct {
	OrderID     string `json:"orderId"`
	OrderLinkID string `json:"orderLinkId"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`
	Status      string `json:"orderStatus"`
	Qty         string `json:"qty"`
	CumExecQty  string `json:"cumExecQty"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
}

// record проверяет заявку; время не ограничено окном.
func (it orderItem) record(fromHistory bool) (OrderRecord, error) {
	all := HistoryWindow{Start: time.UnixMilli(1), End: time.UnixMilli(1 << 52)}
	created, cErr := historyTime(it.CreatedTime, all)
	updated, uErr := historyTime(it.UpdatedTime, all)
	qty, qErr := model.ParseQty(it.Qty)
	cum, xErr := model.ParseQty(it.CumExecQty)
	if cErr != nil || uErr != nil || qErr != nil || xErr != nil || it.OrderID == "" || it.Status == "" ||
		(it.Side != "Buy" && it.Side != "Sell") {
		return OrderRecord{}, fmt.Errorf("%w: order", ErrHistoryData)
	}
	return OrderRecord{OrderID: it.OrderID, OrderLinkID: it.OrderLinkID, Symbol: it.Symbol, Side: it.Side, Status: it.Status,
		Qty: qty, CumExecQty: cum, CreatedAt: created, UpdatedAt: updated, FromHistory: fromHistory}, nil
}
