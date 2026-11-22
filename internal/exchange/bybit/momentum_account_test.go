package bybit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

const momentumTestCoin = `{"coin":"USDT","equity":"1000.123456789","walletBalance":"990.5","totalPositionIM":"100.25","totalOrderIM":"10.125","locked":"0.5","bonus":"1","borrowAmount":"0","spotBorrow":"0"}`

// momentumAccountClient создаёт Testnet клиент с фиксированным ответом и счётчиком запросов.
func momentumAccountClient(t *testing.T, base, body string, calls *int, check func(*http.Request)) *Client {
	t.Helper()
	hc := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		*calls++
		if check != nil {
			check(r)
		}
		return httpResponse(http.StatusOK, body), nil
	})}
	return newAuthenticatedClient(t, base, "fake-key", "fake-secret", hc)
}

// wireOf возвращает точную строку значения или проваливает тест.
func wireOf(t *testing.T, d model.Decimal) string {
	t.Helper()
	s, err := d.WireDecimal()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestGetMomentumIsolatedBalance проверяет формулу available и игнор account-wide пустых полей.
func TestGetMomentumIsolatedBalance(t *testing.T) {
	body := `{"retCode":0,"time":1700000000123,"result":{"list":[{"accountType":"UNIFIED","totalAvailableBalance":"","totalEquity":"","accountIMRate":"","coin":[` + momentumTestCoin + `]}]}}`
	calls := 0
	client := momentumAccountClient(t, "https://api-testnet.bybit.com", body, &calls, func(r *http.Request) {
		if r.URL.Path != "/v5/account/wallet-balance" || r.URL.Host != momentumTestnetHost || r.Header.Get("X-BAPI-SIGN") == "" {
			t.Errorf("request %s", r.URL)
		}
		assertQuery(t, r, map[string]string{"accountType": "UNIFIED"})
	})
	got, err := client.GetMomentumIsolatedBalance(context.Background())
	if err != nil || calls != 1 {
		t.Fatalf("balance: %v calls=%d", err, calls)
	}
	// 990.5 - 100.25 - 10.125 - 0.5 - 1 = 878.625
	if wireOf(t, got.Available) != "878.625" || wireOf(t, got.Equity) != "1000.123456789" || got.Coin != "USDT" ||
		!got.ServerTime.Equal(time.UnixMilli(1700000000123)) || got.ServerTime.Location() != time.UTC {
		t.Fatalf("balance = %+v", got)
	}
}

// TestGetMomentumIsolatedBalanceFailsClosed проверяет пустые поля, займы, чужие активы и источник.
func TestGetMomentumIsolatedBalanceFailsClosed(t *testing.T) {
	wrap := func(coins string) string {
		return `{"retCode":0,"time":1700000000123,"result":{"list":[{"accountType":"UNIFIED","coin":[` + coins + `]}]}}`
	}
	cases := map[string]struct {
		base, body string
		want       error
	}{
		"empty field":   {"https://api-testnet.bybit.com", wrap(strings.Replace(momentumTestCoin, `"locked":"0.5"`, `"locked":""`, 1)), ErrMalformedAccountData},
		"missing field": {"https://api-testnet.bybit.com", wrap(strings.Replace(momentumTestCoin, `"bonus":"1",`, ``, 1)), ErrMalformedAccountData},
		"borrowed":      {"https://api-testnet.bybit.com", wrap(strings.Replace(momentumTestCoin, `"borrowAmount":"0"`, `"borrowAmount":"0.1"`, 1)), ErrBorrowedAsset},
		"spot borrow":   {"https://api-testnet.bybit.com", wrap(strings.Replace(momentumTestCoin, `"spotBorrow":"0"`, `"spotBorrow":"2"`, 1)), ErrBorrowedAsset},
		"other coin":    {"https://api-testnet.bybit.com", wrap(momentumTestCoin + `,{"coin":"BTC"}`), ErrUnsupportedAsset},
		"no coin":       {"https://api-testnet.bybit.com", wrap(``), ErrMalformedAccountData},
		"two accounts":  {"https://api-testnet.bybit.com", `{"retCode":0,"time":1,"result":{"list":[{"accountType":"UNIFIED"},{"accountType":"UNIFIED"}]}}`, ErrMalformedAccountData},
		"no time":       {"https://api-testnet.bybit.com", `{"retCode":0,"result":{"list":[]}}`, ErrMalformedAccountData},
		"mainnet":       {"https://api.bybit.com", wrap(momentumTestCoin), ErrInvalidArgument},
		"testnet path":  {"https://api-testnet.bybit.com/x", wrap(momentumTestCoin), ErrInvalidArgument},
	}
	for name, tc := range cases {
		calls := 0
		client := momentumAccountClient(t, tc.base, tc.body, &calls, nil)
		if _, err := client.GetMomentumIsolatedBalance(context.Background()); !errors.Is(err, tc.want) {
			t.Errorf("%s: err=%v want %v", name, err, tc.want)
		}
		if tc.want == ErrInvalidArgument && calls != 0 {
			t.Errorf("%s: request sent to unsupported source", name)
		}
	}
	unsigned := newClient(t, "https://api-testnet.bybit.com", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsigned request sent")
		return nil, nil
	})})
	if _, err := unsigned.GetMomentumIsolatedBalance(context.Background()); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unsigned: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	if _, err := momentumAccountClient(t, "https://api-testnet.bybit.com", wrap(momentumTestCoin), &calls, nil).GetMomentumIsolatedBalance(ctx); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled: %v calls=%d", err, calls)
	}
}

// TestGetMomentumAccountMode проверяет режим счёта, время сервера и некорректный ответ.
func TestGetMomentumAccountMode(t *testing.T) {
	calls := 0
	body := `{"retCode":0,"time":1700000000999,"result":{"unifiedMarginStatus":5,"marginMode":"ISOLATED_MARGIN","updatedTime":"1690000000000"}}`
	got, err := momentumAccountClient(t, "https://api-testnet.bybit.com", body, &calls, nil).GetMomentumAccountMode(context.Background())
	if err != nil || got.MarginMode != "ISOLATED_MARGIN" || got.UnifiedMarginStatus != 5 || !got.ServerTime.Equal(time.UnixMilli(1700000000999)) {
		t.Fatalf("mode = %+v, %v", got, err)
	}
	bad := `{"retCode":0,"time":1700000000999,"result":{"unifiedMarginStatus":5,"marginMode":"X","updatedTime":"1"}}`
	if _, err := momentumAccountClient(t, "https://api-testnet.bybit.com", bad, &calls, nil).GetMomentumAccountMode(context.Background()); !errors.Is(err, ErrMalformedAccountData) {
		t.Fatalf("bad mode: %v", err)
	}
	if _, err := momentumAccountClient(t, "https://api.bybit.com", body, &calls, nil).GetMomentumAccountMode(context.Background()); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("mainnet: %v", err)
	}
}

// momentumTestPosition возвращает валидную one-way позицию для мутаций в тестах.
func momentumTestPosition() momentumPositionItem {
	zero, idx := 0, 0
	return momentumPositionItem{PositionIdx: &idx, Symbol: "BTCUSDT", Side: "Buy", Size: "0.015", AvgPrice: "60000.5",
		MarkPrice: "61000.25", Leverage: "1", PositionIM: "915.0037", AutoAddMargin: &zero, PositionStatus: "Normal", UpdatedTime: "1700000000000"}
}

// TestParseMomentumPosition проверяет точный разбор позиции и отказ на неполных полях.
func TestParseMomentumPosition(t *testing.T) {
	server := time.UnixMilli(1700000000500).UTC()
	got, err := parseMomentumPosition(momentumTestPosition(), server)
	if err != nil || wireOf(t, got.Size) != "0.015" || wireOf(t, got.PositionIM) != "915.0037" || got.AutoAddMargin ||
		got.PositionIdx != 0 || got.Side != "Buy" || !got.UpdatedTime.Equal(time.UnixMilli(1700000000000)) || got.ServerTime != server {
		t.Fatalf("position = %+v, %v", got, err)
	}
	empty := momentumTestPosition()
	empty.Side, empty.Size, empty.AvgPrice, empty.PositionIM = "", "0", "0", "0"
	if _, err := parseMomentumPosition(empty, server); err != nil {
		t.Fatalf("empty position: %v", err)
	}
	two, one, three := 2, 1, 3
	mutations := map[string]func(*momentumPositionItem){
		"symbol":       func(p *momentumPositionItem) { p.Symbol = "btc" },
		"no idx":       func(p *momentumPositionItem) { p.PositionIdx = nil },
		"idx":          func(p *momentumPositionItem) { p.PositionIdx = &three },
		"auto add":     func(p *momentumPositionItem) { p.AutoAddMargin = &two },
		"no auto add":  func(p *momentumPositionItem) { p.AutoAddMargin = nil },
		"status":       func(p *momentumPositionItem) { p.PositionStatus = "" },
		"size":         func(p *momentumPositionItem) { p.Size = "" },
		"avg":          func(p *momentumPositionItem) { p.AvgPrice = "-1" },
		"mark":         func(p *momentumPositionItem) { p.MarkPrice = "0" },
		"leverage":     func(p *momentumPositionItem) { p.Leverage = "" },
		"im":           func(p *momentumPositionItem) { p.PositionIM = "1e3" },
		"updated":      func(p *momentumPositionItem) { p.UpdatedTime = "01" },
		"side no size": func(p *momentumPositionItem) { p.Size = "0" },
		"bad side":     func(p *momentumPositionItem) { p.Side = "Long" },
	}
	for name, mutate := range mutations {
		item := momentumTestPosition()
		mutate(&item)
		if _, err := parseMomentumPosition(item, server); !errors.Is(err, ErrMalformedAccountData) {
			t.Errorf("%s: %v", name, err)
		}
	}
	hedge := momentumTestPosition()
	hedge.PositionIdx = &one
	if got, err := parseMomentumPosition(hedge, server); err != nil || got.PositionIdx != 1 {
		t.Fatalf("hedge idx must stay visible: %+v %v", got, err)
	}
}
