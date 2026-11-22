package bybit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
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

// ExecutionRecord — одно исполнение; Fill заполнен только для сделок (Trade, AdlTrade, BustTrade).
type ExecutionRecord struct {
	OrderID, OrderLinkID, ExecType string
	Fill                           model.ConfirmedFill
	IsTrade                        bool
}

// TransactionRecord — строка transaction log в точных decimal.
type TransactionRecord struct {
	ID, Type, Symbol, OrderLinkID, TradeID string
	CashFlow, Funding, Fee, Change         model.Decimal
	At                                     time.Time
}

// HistoryWindow — включающее окно [Start, End] и курсор продолжения.
type HistoryWindow struct {
	Start, End time.Time
	Cursor     string
}

// HistoryPage — страница истории; NextCursor=="" — окно исчерпано. Пустой
// список без ошибки означает «записей нет», а не сбой.
type HistoryPage[T any] struct {
	Items      []T
	NextCursor string
	ServerTime time.Time
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
			List []orderItem `json:"list"`
		}
		if _, err := c.signedGet(ctx, path, query, &result); err != nil {
			return OrderRecord{}, false, err
		}
		if len(result.List) == 0 {
			continue
		}
		it := result.List[0]
		if len(result.List) > 1 || it.OrderLinkID != orderLinkID || it.Symbol != symbol {
			return OrderRecord{}, false, fmt.Errorf("%w: order %s", ErrHistoryData, path)
		}
		record, err := it.record(path == "/v5/order/history")
		return record, err == nil, err
	}
	return OrderRecord{}, false, nil
}

// MomentumExecutions читает страницу исполнений linear по символу за включающее окно.
func (c *Client) MomentumExecutions(ctx context.Context, symbol string, window HistoryWindow) (HistoryPage[ExecutionRecord], error) {
	var page HistoryPage[ExecutionRecord]
	var result struct {
		List   []executionItem `json:"list"`
		Cursor string          `json:"nextPageCursor"`
	}
	query, err := historyQuery(window, "symbol", symbol, "100")
	// Fill хранит комиссию в USDT, поэтому принимаются только USDT-settled контракты (XXXUSDT).
	if err != nil || validateSymbol(symbol) != nil || !strings.HasSuffix(symbol, "USDT") || len(symbol) <= 4 {
		return page, fmt.Errorf("%w: execution window", ErrInvalidArgument)
	}
	server, err := c.signedGet(ctx, "/v5/execution/list", query, &result)
	if err != nil {
		return page, err
	}
	page.Items, page.NextCursor, page.ServerTime = make([]ExecutionRecord, 0, len(result.List)), result.Cursor, time.UnixMilli(server).UTC()
	for _, it := range result.List {
		record, err := it.record(symbol, window)
		if err != nil {
			return HistoryPage[ExecutionRecord]{}, err
		}
		page.Items = append(page.Items, record)
	}
	return page, nil
}

// MomentumTransactions читает страницу transaction log UNIFIED/linear за включающее окно.
func (c *Client) MomentumTransactions(ctx context.Context, window HistoryWindow) (HistoryPage[TransactionRecord], error) {
	var page HistoryPage[TransactionRecord]
	var result struct {
		List   []transactionItem `json:"list"`
		Cursor string            `json:"nextPageCursor"`
	}
	query, err := historyQuery(window, "accountType", "UNIFIED", "50")
	if err != nil {
		return page, err
	}
	server, err := c.signedGet(ctx, "/v5/account/transaction-log", query, &result)
	if err != nil {
		return page, err
	}
	page.Items, page.NextCursor, page.ServerTime = make([]TransactionRecord, 0, len(result.List)), result.Cursor, time.UnixMilli(server).UTC()
	for _, it := range result.List {
		at, err := historyTime(it.TransactionTime, window)
		values, vErr := decimals(it.CashFlow, it.Funding, it.Fee, it.Change)
		if err != nil || vErr != nil || it.ID == "" || it.Type == "" {
			return HistoryPage[TransactionRecord]{}, fmt.Errorf("%w: transaction", ErrHistoryData)
		}
		page.Items = append(page.Items, TransactionRecord{ID: it.ID, Type: it.Type, Symbol: it.Symbol, OrderLinkID: it.OrderLinkID,
			TradeID: it.TradeID, CashFlow: values[0], Funding: values[1], Fee: values[2], Change: values[3], At: at})
	}
	return page, nil
}

// historyQuery проверяет окно (Bybit допускает не более 7 дней) и строит запрос;
// limit — максимум страницы конкретного endpoint (execution 100, transaction-log 50).
func historyQuery(w HistoryWindow, key, value, limit string) (url.Values, error) {
	if w.Start.IsZero() || w.End.Before(w.Start) || w.End.Sub(w.Start) > 7*24*time.Hour {
		return nil, fmt.Errorf("%w: history window must be 0..7 days", ErrInvalidArgument)
	}
	q := url.Values{"category": {"linear"}, key: {value}, "limit": {limit},
		"startTime": {strconv.FormatInt(w.Start.UnixMilli(), 10)}, "endTime": {strconv.FormatInt(w.End.UnixMilli(), 10)}}
	if w.Cursor != "" {
		q.Set("cursor", w.Cursor)
	}
	return q, nil
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

// decimals разбирает строки как signed decimal; пустая строка — ноль.
func decimals(raw ...string) ([]model.Decimal, error) {
	out := make([]model.Decimal, len(raw))
	for i, s := range raw {
		if s == "" {
			out[i] = model.Zero()
			continue
		}
		d, err := model.ParseAmount(s)
		if err != nil {
			return nil, err
		}
		out[i] = d
	}
	return out, nil
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

// executionItem — поля исполнения Bybit V5.
type executionItem struct {
	Symbol      string `json:"symbol"`
	OrderID     string `json:"orderId"`
	OrderLinkID string `json:"orderLinkId"`
	Side        string `json:"side"`
	ExecID      string `json:"execId"`
	ExecType    string `json:"execType"`
	ExecPrice   string `json:"execPrice"`
	ExecQty     string `json:"execQty"`
	ExecFee     string `json:"execFee"`
	ExecTime    string `json:"execTime"`
}

// record проверяет исполнение и строит ConfirmedFill для сделок.
func (it executionItem) record(symbol string, w HistoryWindow) (ExecutionRecord, error) {
	at, err := historyTime(it.ExecTime, w)
	if err != nil || it.Symbol != symbol || it.ExecID == "" || it.OrderID == "" || it.ExecType == "" {
		return ExecutionRecord{}, fmt.Errorf("%w: execution", ErrHistoryData)
	}
	record := ExecutionRecord{OrderID: it.OrderID, OrderLinkID: it.OrderLinkID, ExecType: it.ExecType}
	switch it.ExecType {
	case "Trade", "AdlTrade", "BustTrade":
	default:
		return record, nil
	}
	side := map[string]model.FillSide{"Buy": model.FillBuy, "Sell": model.FillSell}[it.Side]
	qty, qErr := model.ParseQty(it.ExecQty)
	price, pErr := model.ParsePrice(it.ExecPrice)
	fee, fErr := model.ParseAmount(it.ExecFee)
	if side == "" || qErr != nil || pErr != nil || fErr != nil {
		return ExecutionRecord{}, fmt.Errorf("%w: execution", ErrHistoryData)
	}
	if c, err := qty.Cmp(model.Zero()); err != nil || c <= 0 {
		return ExecutionRecord{}, fmt.Errorf("%w: execution", ErrHistoryData)
	}
	record.IsTrade = true
	record.Fill = model.ConfirmedFill{ExecutionID: it.ExecID, Symbol: it.Symbol, Side: side, ExecutedAt: at,
		Quantity: qty, ExecutionPrice: price, FeeUSDT: fee}
	return record, nil
}

// transactionItem — поля transaction log Bybit V5.
type transactionItem struct {
	ID              string `json:"id"`
	Symbol          string `json:"symbol"`
	Type            string `json:"type"`
	TransactionTime string `json:"transactionTime"`
	CashFlow        string `json:"cashFlow"`
	Funding         string `json:"funding"`
	Fee             string `json:"fee"`
	Change          string `json:"change"`
	OrderLinkID     string `json:"orderLinkId"`
	TradeID         string `json:"tradeId"`
}
