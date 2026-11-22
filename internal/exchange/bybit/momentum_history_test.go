package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// historyServer отвечает фиксированным result по пути и записывает запросы.
func historyServer(t *testing.T, results map[string]any) (*Client, *[]string) {
	t.Helper()
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		result, ok := results[r.URL.Path]
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"retCode": 10001, "retMsg": "bad", "result": map[string]any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "retMsg": "OK", "result": result, "time": 1700000009000})
	}))
	t.Cleanup(server.Close)
	client, err := NewAuthenticatedClient(server.URL, "key", "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, &seen
}

// list оборачивает элементы в формат Bybit.
func list(items ...map[string]string) map[string]any {
	return map[string]any{"list": items, "nextPageCursor": "next"}
}

// order строит запись заявки по orderLinkId.
func order(link, status string) map[string]string {
	return map[string]string{"orderId": "o1", "orderLinkId": link, "symbol": "BTCUSDT", "side": "Buy", "orderStatus": status,
		"qty": "0.01", "cumExecQty": "0.01", "createdTime": "1700000000000", "updatedTime": "1700000001000"}
}

// TestLookupMomentumOrder проверяет переход к истории и то, что отсутствие заявки не означает fill.
func TestLookupMomentumOrder(t *testing.T) {
	ctx := context.Background()
	client, seen := historyServer(t, map[string]any{"/v5/order/realtime": list(), "/v5/order/history": list(order("l1", "Filled"))})
	got, found, err := client.LookupMomentumOrder(ctx, "BTCUSDT", "l1")
	if err != nil || !found || !got.FromHistory || got.Status != "Filled" || len(*seen) != 2 {
		t.Fatalf("history lookup=%+v %v %v %v", got, found, err, *seen)
	}
	client, _ = historyServer(t, map[string]any{"/v5/order/realtime": list(), "/v5/order/history": list()})
	if got, found, err = client.LookupMomentumOrder(ctx, "BTCUSDT", "l1"); err != nil || found || got.Status != "" {
		t.Fatalf("absent order must be reported as not found: %+v %v %v", got, found, err)
	}
	client, seen = historyServer(t, map[string]any{"/v5/order/realtime": list(order("l1", "New"))})
	if got, found, err = client.LookupMomentumOrder(ctx, "BTCUSDT", "l1"); err != nil || !found || got.FromHistory || len(*seen) != 1 {
		t.Fatalf("realtime lookup=%+v %v %v", got, found, err)
	}
	for name, results := range map[string]map[string]any{
		"api-error": {"/v5/order/realtime": list()},
		"foreign":   {"/v5/order/realtime": list(order("other", "New"))},
		"malformed": {"/v5/order/realtime": list(map[string]string{"orderLinkId": "l1", "symbol": "BTCUSDT"})},
	} {
		client, _ = historyServer(t, results)
		if _, found, err = client.LookupMomentumOrder(ctx, "BTCUSDT", "l1"); err == nil || found {
			t.Fatalf("%s: want error, got found=%v", name, found)
		}
	}
	if _, _, err = client.LookupMomentumOrder(ctx, "BTCUSDT", ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty link=%v", err)
	}
}

// executionRow строит запись исполнения.
func executionRow(id, execType, at string) map[string]string {
	return map[string]string{"symbol": "BTCUSDT", "orderId": "o1", "orderLinkId": "l1", "side": "Sell", "execId": id,
		"execType": execType, "execPrice": "90000", "execQty": "0.004", "execFee": "0.1", "execTime": at}
}

// TestMomentumExecutions проверяет включающие границы, повторы, порядок, пустую страницу и курсор.
func TestMomentumExecutions(t *testing.T) {
	ctx := context.Background()
	start, end := time.UnixMilli(1700000000000).UTC(), time.UnixMilli(1700000005000).UTC()
	window := HistoryWindow{Start: start, End: end, Cursor: "c1"}
	client, seen := historyServer(t, map[string]any{"/v5/execution/list": list(executionRow("e2", "Trade", "1700000005000"),
		executionRow("e2", "Trade", "1700000005000"), executionRow("e1", "Trade", "1700000000000"),
		executionRow("f1", "Funding", "1700000003000"))})
	page, err := client.MomentumExecutions(ctx, "BTCUSDT", window)
	if err != nil || len(page.Items) != 4 || page.NextCursor != "next" || !page.ServerTime.Equal(time.UnixMilli(1700000009000).UTC()) {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	var ids []string
	for _, r := range page.Items {
		ids = append(ids, r.Fill.ExecutionID+map[bool]string{true: "+", false: "-"}[r.IsTrade])
	}
	if strings.Join(ids, ",") != "e2+,e2+,e1+,-" || !strings.Contains((*seen)[0], "cursor=c1") ||
		!strings.Contains((*seen)[0], "limit=100") || !strings.Contains((*seen)[0], "startTime=1700000000000") || !strings.Contains((*seen)[0], "endTime=1700000005000") {
		t.Fatalf("ids=%v query=%v", ids, *seen)
	}
	client, _ = historyServer(t, map[string]any{"/v5/execution/list": map[string]any{"list": []any{}, "nextPageCursor": ""}})
	if page, err = client.MomentumExecutions(ctx, "BTCUSDT", window); err != nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("empty page must not be an error: %+v %v", page, err)
	}
	for _, row := range []map[string]string{executionRow("x", "Trade", "1700000005001"), executionRow("x", "Trade", "1699999999999"),
		{"symbol": "ETHUSDT", "orderId": "o", "execId": "x", "execType": "Trade", "execTime": "1700000001000"}} {
		client, _ = historyServer(t, map[string]any{"/v5/execution/list": list(row)})
		if _, err = client.MomentumExecutions(ctx, "BTCUSDT", window); !errors.Is(err, ErrHistoryData) {
			t.Fatalf("out-of-window/foreign row %v: %v", row, err)
		}
	}
	if _, err = client.MomentumExecutions(ctx, "BTCUSDT", HistoryWindow{Start: start, End: start.Add(8 * 24 * time.Hour)}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("long window=%v", err)
	}
}

// TestMomentumTransactions проверяет точные суммы, пустые поля как ноль и отказ на битой строке.
func TestMomentumTransactions(t *testing.T) {
	ctx := context.Background()
	window := HistoryWindow{Start: time.UnixMilli(1700000000000), End: time.UnixMilli(1700000005000)}
	client, seen := historyServer(t, map[string]any{"/v5/account/transaction-log": list(map[string]string{"id": "t1",
		"symbol": "BTCUSDT", "type": "SETTLEMENT", "transactionTime": "1700000002000", "funding": "-0.123456789", "cashFlow": ""})})
	page, err := client.MomentumTransactions(ctx, window)
	if err != nil || len(page.Items) != 1 || !strings.Contains((*seen)[0], "accountType=UNIFIED") || !strings.Contains((*seen)[0], "limit=50") {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	funding, _ := page.Items[0].Funding.WireDecimal()
	cash, _ := page.Items[0].CashFlow.WireDecimal()
	if funding != "-0.123456789" || cash != "0" {
		t.Fatalf("funding=%s cash=%s", funding, cash)
	}
	client, _ = historyServer(t, map[string]any{"/v5/account/transaction-log": list(map[string]string{"id": "t1",
		"type": "TRADE", "transactionTime": "1700000002000", "fee": "abc"})})
	if _, err = client.MomentumTransactions(ctx, window); !errors.Is(err, ErrHistoryData) {
		t.Fatalf("malformed=%v", err)
	}
}
