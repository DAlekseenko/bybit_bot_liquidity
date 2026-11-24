package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// historyServer отвечает фиксированным result по пути через fake transport Testnet и записывает запросы.
func historyServer(t *testing.T, results map[string]any) (*Client, *[]string) {
	t.Helper()
	var seen []string
	client := testPostClient(t, "https://api-testnet.bybit.com", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		body := map[string]any{"retCode": 10001, "retMsg": "bad", "result": map[string]any{}}
		if result, ok := results[r.URL.Path]; ok {
			body = map[string]any{"retCode": 0, "retMsg": "OK", "result": result, "time": 1700000009000}
		}
		raw, _ := json.Marshal(body)
		return timeResponse(r, http.StatusOK, string(raw)), nil
	}))
	return client, &seen
}

// list оборачивает элементы в формат Bybit.
func list(items ...map[string]string) map[string]any {
	if items == nil {
		items = []map[string]string{}
	}
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
	sent := time.UnixMilli(1700000060000).UTC()
	client, seen := historyServer(t, map[string]any{"/v5/order/realtime": list(), "/v5/order/history": list(order("l1", "Filled"))})
	got, found, err := client.LookupMomentumOrder(ctx, "BTCUSDT", "l1", sent)
	if err != nil || !found || !got.FromHistory || got.Status != "Filled" || len(*seen) != 2 {
		t.Fatalf("history lookup=%+v %v %v %v", got, found, err, *seen)
	}
	if !strings.Contains((*seen)[1], "startTime=1700000000000") || !strings.Contains((*seen)[1], "endTime=1700604800000") {
		t.Fatalf("history window must be explicit: %v", (*seen)[1])
	}
	if _, _, err = client.LookupMomentumOrder(ctx, "BTCUSDT", "l1", time.Time{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing send time=%v", err)
	}
	client, _ = historyServer(t, map[string]any{"/v5/order/realtime": list(), "/v5/order/history": list()})
	if got, found, err = client.LookupMomentumOrder(ctx, "BTCUSDT", "l1", sent); err != nil || found || got.Status != "" {
		t.Fatalf("absent order must be reported as not found: %+v %v %v", got, found, err)
	}
	client, seen = historyServer(t, map[string]any{"/v5/order/realtime": list(order("l1", "New"))})
	if got, found, err = client.LookupMomentumOrder(ctx, "BTCUSDT", "l1", sent); err != nil || !found || got.FromHistory || len(*seen) != 1 {
		t.Fatalf("realtime lookup=%+v %v %v", got, found, err)
	}
	for name, results := range map[string]map[string]any{
		"api-error": {"/v5/order/realtime": list()},
		"foreign":   {"/v5/order/realtime": list(order("other", "New"))},
		"malformed": {"/v5/order/realtime": list(map[string]string{"orderLinkId": "l1", "symbol": "BTCUSDT"})},
	} {
		client, _ = historyServer(t, results)
		if _, found, err = client.LookupMomentumOrder(ctx, "BTCUSDT", "l1", sent); err == nil || found {
			t.Fatalf("%s: want error, got found=%v", name, found)
		}
	}
	client, _ = historyServer(t, map[string]any{"/v5/order/realtime": list(), "/v5/order/history": map[string]any{}})
	if _, found, err = client.LookupMomentumOrder(ctx, "BTCUSDT", "l1", sent); !errors.Is(err, ErrHistoryData) || found {
		t.Fatalf("missing list must be an error, not absence: %v %v", found, err)
	}
	mainnet, err := NewAuthenticatedClient("https://api.bybit.com", "key", "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = mainnet.LookupMomentumOrder(ctx, "BTCUSDT", "l1", sent); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("mainnet private read must be refused: %v", err)
	}
	if _, _, err = client.LookupMomentumOrder(ctx, "BTCUSDT", "", sent); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty link=%v", err)
	}
}

// executionRow строит запись исполнения.
