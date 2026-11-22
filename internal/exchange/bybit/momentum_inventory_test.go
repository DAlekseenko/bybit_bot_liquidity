package bybit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

const (
	invPosBTC = `{"positionIdx":0,"symbol":"BTCUSDT","side":"Buy","size":"0.01","avgPrice":"60000","markPrice":"61000","leverage":"1","positionIM":"610","autoAddMargin":0,"positionStatus":"Normal","updatedTime":"1700000000000"}`
	invPosXRP = `{"positionIdx":1,"symbol":"XRPUSDT","side":"Sell","size":"5","avgPrice":"0.5","markPrice":"0.51","leverage":"3","positionIM":"0.85","autoAddMargin":1,"positionStatus":"Normal","updatedTime":"1700000000000"}`
	invOrder  = `{"orderId":"%s","orderLinkId":"%s","symbol":"ETHUSDT","side":"Buy","positionIdx":0,"orderStatus":"New","qty":"0.1","leavesQty":"0.1","price":"3000.5","reduceOnly":false,"createdTime":"1700000000000","updatedTime":"1700000000001"}`
)

// inventoryPage формирует страницу ответа с курсором и временем сервера.
func inventoryPage(server int64, cursor, list string) string {
	return fmt.Sprintf(`{"retCode":0,"time":%d,"result":{"category":"linear","nextPageCursor":%q,"list":[%s]}}`, server, cursor, list)
}

// inventoryClient отвечает страницами по пути и значению cursor.
func inventoryClient(t *testing.T, pages map[string]string, calls *int) *Client {
	t.Helper()
	hc := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		*calls++
		q := r.URL.Query()
		if q.Get("category") != "linear" || q.Get("settleCoin") != "USDT" || q.Get("symbol") != "" || r.Header.Get("X-BAPI-SIGN") == "" {
			t.Errorf("query %s", r.URL)
		}
		body, ok := pages[r.URL.Path+"?"+q.Get("cursor")]
		if !ok {
			t.Errorf("unexpected page %s", r.URL)
			return httpResponse(http.StatusInternalServerError, ""), nil
		}
		return httpResponse(http.StatusOK, body), nil
	})}
	return newAuthenticatedClient(t, "https://api-testnet.bybit.com", "fake-key", "fake-secret", hc)
}

// TestGetMomentumAccountInventoryPaginates проверяет обход курсора, чужие символы и раннее время.
func TestGetMomentumAccountInventoryPaginates(t *testing.T) {
	calls := 0
	client := inventoryClient(t, map[string]string{
		"/v5/position/list?":    inventoryPage(1700000000300, "p2", invPosBTC),
		"/v5/position/list?p2":  inventoryPage(1700000000200, "", invPosXRP),
		"/v5/order/realtime?":   inventoryPage(1700000000400, "o2", fmt.Sprintf(invOrder, "1", "cm-abc")),
		"/v5/order/realtime?o2": inventoryPage(1700000000500, "", fmt.Sprintf(invOrder, "2", "")),
	}, &calls)
	inv, err := client.GetMomentumAccountInventory(context.Background())
	if err != nil || calls != 4 || len(inv.Positions) != 2 || len(inv.Orders) != 2 {
		t.Fatalf("inventory = %+v, %v, calls=%d", inv, err, calls)
	}
	if inv.Positions[1].Symbol != "XRPUSDT" || inv.Positions[1].PositionIdx != 1 || !inv.Positions[1].AutoAddMargin {
		t.Fatalf("unknown position not visible: %+v", inv.Positions[1])
	}
	if !inv.ServerTime.Equal(time.UnixMilli(1700000000200)) {
		t.Fatalf("server time = %s", inv.ServerTime)
	}
	if !model.OrderOwnedBy(inv.Orders[0], "cm-") || model.OrderOwnedBy(inv.Orders[1], "cm-") || model.OrderOwnedBy(inv.Orders[0], "") ||
		model.OrderOwnedBy(inv.Orders[0], "other-") {
		t.Fatal("ownership namespace mismatch")
	}
	if wireOf(t, inv.Orders[0].Price) != "3000.5" || inv.Orders[0].ReduceOnly || inv.Orders[1].OrderLinkID != "" {
		t.Fatalf("order = %+v", inv.Orders[0])
	}
}

// TestGetMomentumInventoryEmptyIsNotError проверяет, что пустой счёт отличается от ошибки.
func TestGetMomentumInventoryEmptyIsNotError(t *testing.T) {
	calls := 0
	client := inventoryClient(t, map[string]string{
		"/v5/position/list?":  inventoryPage(1700000000300, "", ""),
		"/v5/order/realtime?": inventoryPage(1700000000300, "", ""),
	}, &calls)
	inv, err := client.GetMomentumAccountInventory(context.Background())
	if err != nil || inv.Positions == nil || inv.Orders == nil || len(inv.Positions)+len(inv.Orders) != 0 {
		t.Fatalf("empty inventory = %+v, %v", inv, err)
	}
}

// TestGetMomentumInventoryFailsClosed проверяет неполные страницы, повтор курсора и ошибки API.
func TestGetMomentumInventoryFailsClosed(t *testing.T) {
	order := func(mutate func(string) string) string { return mutate(fmt.Sprintf(invOrder, "1", "cm-1")) }
	same := func(s string) string { return s }
	cases := map[string]map[string]string{
		"missing list":    {"/v5/position/list?": `{"retCode":0,"time":1,"result":{"category":"linear","nextPageCursor":""}}`},
		"missing cursor":  {"/v5/position/list?": `{"retCode":0,"time":1,"result":{"category":"linear","list":[]}}`},
		"wrong category":  {"/v5/position/list?": strings.Replace(inventoryPage(1, "", ""), "linear", "spot", 1)},
		"repeated cursor": {"/v5/position/list?": inventoryPage(1, "a", ""), "/v5/position/list?a": inventoryPage(1, "a", "")},
		"api error page2": {"/v5/position/list?": inventoryPage(1, "a", ""), "/v5/position/list?a": `{"retCode":10006,"retMsg":"rate"}`},
		"bad position":    {"/v5/position/list?": inventoryPage(1, "", strings.Replace(invPosBTC, `"leverage":"1"`, `"leverage":""`, 1))},
		"dup position":    {"/v5/position/list?": inventoryPage(1, "", invPosBTC+","+invPosBTC)},
		"dup order": {"/v5/position/list?": inventoryPage(1, "", ""),
			"/v5/order/realtime?": inventoryPage(1, "", order(same)+","+order(same))},
	}
	for field, mutate := range map[string]func(string) string{
		"no id":       func(s string) string { return strings.Replace(s, `"orderId":"1"`, `"orderId":""`, 1) },
		"symbol":      func(s string) string { return strings.Replace(s, "ETHUSDT", "eth", 1) },
		"side":        func(s string) string { return strings.Replace(s, `"Buy"`, `"Long"`, 1) },
		"idx":         func(s string) string { return strings.Replace(s, `"positionIdx":0`, `"positionIdx":7`, 1) },
		"status":      func(s string) string { return strings.Replace(s, `"New"`, `"Filled"`, 1) },
		"reduce only": func(s string) string { return strings.Replace(s, `"reduceOnly":false,`, ``, 1) },
		"qty":         func(s string) string { return strings.Replace(s, `"qty":"0.1"`, `"qty":"0"`, 1) },
		"leaves":      func(s string) string { return strings.Replace(s, `"leavesQty":"0.1"`, `"leavesQty":"0.2"`, 1) },
		"price":       func(s string) string { return strings.Replace(s, `"price":"3000.5"`, `"price":""`, 1) },
		"time": func(s string) string {
			return strings.Replace(s, `"createdTime":"1700000000000"`, `"createdTime":"x"`, 1)
		},
	} {
		cases["order "+field] = map[string]string{"/v5/position/list?": inventoryPage(1, "", ""), "/v5/order/realtime?": inventoryPage(1, "", order(mutate))}
	}
	for name, pages := range cases {
		calls := 0
		if inv, err := inventoryClient(t, pages, &calls).GetMomentumAccountInventory(context.Background()); err == nil {
			t.Errorf("%s: expected error, got %+v", name, inv)
		}
	}
	calls := 0
	endless := map[string]string{}
	for i := 0; i < momentumInventoryMaxPages; i++ {
		endless[fmt.Sprintf("/v5/position/list?%s", map[bool]string{true: "", false: fmt.Sprint(i)}[i == 0])] = inventoryPage(1, fmt.Sprint(i+1), "")
	}
	if _, _, err := inventoryClient(t, endless, &calls).GetMomentumPositions(context.Background()); !errors.Is(err, ErrMalformedAccountData) || calls != momentumInventoryMaxPages {
		t.Fatalf("page limit: %v calls=%d", err, calls)
	}
}
