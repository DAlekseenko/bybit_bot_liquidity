package bybit

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// ExecutionRecord — одно исполнение; Fill заполнен только для сделок (Trade, AdlTrade, BustTrade).
type ExecutionRecord struct {
	OrderID, OrderLinkID, ExecType string
	Fill                           model.ConfirmedFill
	IsTrade                        bool
}

// TransactionRecord — строка transaction log в точных decimal; все суммы в USDT.
type TransactionRecord struct {
	ID, Type, Symbol, OrderLinkID, TradeID string
	CashFlow, Funding, Fee, Change         model.Decimal
	At                                     time.Time
}

// HistoryPage — страница истории; NextCursor=="" — окно исчерпано. Пустой
// список без ошибки означает «записей нет», а не сбой.
type HistoryPage[T any] struct {
	Items      []T
	NextCursor string
	ServerTime time.Time
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
	query.Set("currency", "USDT") // суммы ниже — только USDT; иные валюты отвергаются
	server, err := c.signedGet(ctx, "/v5/account/transaction-log", query, &result)
	if err != nil {
		return page, err
	}
	page.Items, page.NextCursor, page.ServerTime = make([]TransactionRecord, 0, len(result.List)), result.Cursor, time.UnixMilli(server).UTC()
	for _, it := range result.List {
		at, err := historyTime(it.TransactionTime, window)
		values, vErr := decimals(it.CashFlow, it.Funding, it.Fee, it.Change)
		if err != nil || vErr != nil || it.ID == "" || it.Type == "" || it.Currency != "USDT" {
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
	Currency        string `json:"currency"`
}
