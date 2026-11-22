package bybit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// historyServer отвечает фиксированным result по пути и записывает запросы.
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
	if _, err = client.MomentumExecutions(ctx, "BTCPERP", window); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("USDC-settled symbol must be refused: %v", err)
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
		"symbol": "BTCUSDT", "type": "SETTLEMENT", "transactionTime": "1700000002000", "funding": "-0.123456789", "cashFlow": "",
		"currency": "USDT"})})
	page, err := client.MomentumTransactions(ctx, window)
	if err != nil || len(page.Items) != 1 || !strings.Contains((*seen)[0], "accountType=UNIFIED") || !strings.Contains((*seen)[0], "limit=50") ||
		!strings.Contains((*seen)[0], "currency=USDT") {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	funding, _ := page.Items[0].Funding.WireDecimal()
	cash, _ := page.Items[0].CashFlow.WireDecimal()
	if funding != "-0.123456789" || cash != "0" {
		t.Fatalf("funding=%s cash=%s", funding, cash)
	}
	client, _ = historyServer(t, map[string]any{"/v5/account/transaction-log": list(map[string]string{"id": "t1",
		"type": "TRADE", "transactionTime": "1700000002000", "fee": "abc", "currency": "USDT"})})
	if _, err = client.MomentumTransactions(ctx, window); !errors.Is(err, ErrHistoryData) {
		t.Fatalf("malformed=%v", err)
	}
	client, _ = historyServer(t, map[string]any{"/v5/account/transaction-log": list(map[string]string{"id": "t2",
		"type": "TRADE", "transactionTime": "1700000002000", "fee": "1", "currency": "USDC"})})
	if _, err = client.MomentumTransactions(ctx, window); !errors.Is(err, ErrHistoryData) {
		t.Fatalf("non-USDT transaction=%v", err)
	}
}

// TestHistoryRefusesMainnet проверяет, что приватная история читается только с Testnet.
func TestHistoryRefusesMainnet(t *testing.T) {
	client, err := NewAuthenticatedClient("https://api.bybit.com", "key", "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	window := HistoryWindow{Start: time.UnixMilli(1700000000000), End: time.UnixMilli(1700000005000)}
	if _, err := client.MomentumExecutions(context.Background(), "BTCUSDT", window); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("executions on mainnet: %v", err)
	}
	if _, err := client.MomentumTransactions(context.Background(), window); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("transactions on mainnet: %v", err)
	}
}
