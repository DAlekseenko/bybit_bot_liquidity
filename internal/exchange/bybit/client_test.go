package bybit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

func TestGetCandles(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/proxy/v5/market/kline" {
			t.Errorf("path = %s", r.URL.Path)
		}
		assertQuery(t, r, map[string]string{
			"category": linearCategory,
			"symbol":   "BTCUSDT",
			"interval": oneMinuteInterval,
			"limit":    "2",
		})
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}

		return httpResponse(http.StatusOK, `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {
    "category": "linear",
    "symbol": "BTCUSDT",
    "list": [
      ["120000", "102", "106", "101", "105", "2.5", "260"],
      ["60000", "100", "103", "99", "102", "1.5", "151.5"]
    ]
  },
  "time": 179999
}`), nil
	})}

	client := newClient(t, "https://api.example.test/proxy/", httpClient)
	candles, err := client.GetCandles(context.Background(), "BTCUSDT", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 2 {
		t.Fatalf("len(candles) = %d, want 2", len(candles))
	}

	first := candles[0]
	if first.Symbol != "BTCUSDT" {
		t.Errorf("first.Symbol = %q", first.Symbol)
	}
	if !first.OpenTime.Equal(time.UnixMilli(60000).UTC()) {
		t.Errorf("first.OpenTime = %s", first.OpenTime)
	}
	if first.Open != 100 || first.High != 103 || first.Low != 99 || first.Close != 102 {
		t.Errorf("unexpected first OHLC: %+v", first)
	}
	if first.Volume != 1.5 || first.Turnover != 151.5 {
		t.Errorf("unexpected first volume/turnover: %+v", first)
	}
	if !first.Confirmed {
		t.Error("older candle must be confirmed")
	}

	second := candles[1]
	if !second.OpenTime.Equal(time.UnixMilli(120000).UTC()) {
		t.Errorf("second.OpenTime = %s", second.OpenTime)
	}
	if second.Confirmed {
		t.Error("current candle must not be confirmed")
	}
}

func TestGetOpenInterest(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v5/market/open-interest" {
			t.Errorf("path = %s", r.URL.Path)
		}
		assertQuery(t, r, map[string]string{
			"category":     linearCategory,
			"symbol":       "BTCUSDT",
			"intervalTime": "5min",
			"limit":        "1",
		})

		return httpResponse(http.StatusOK, `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {
    "category": "linear",
    "symbol": "BTCUSDT",
    "list": [{"openInterest": "123.45", "timestamp": "180000"}]
  },
  "time": 180100
}`), nil
	})}

	client := newClient(t, "https://api.example.test", httpClient)
	openInterest, err := client.GetOpenInterest(context.Background(), "BTCUSDT", 5)
	if err != nil {
		t.Fatal(err)
	}
	if openInterest.Symbol != "BTCUSDT" {
		t.Errorf("Symbol = %q", openInterest.Symbol)
	}
	if openInterest.Value != 123.45 {
		t.Errorf("Value = %f", openInterest.Value)
	}
	if !openInterest.Time.Equal(time.UnixMilli(180000).UTC()) {
		t.Errorf("Time = %s", openInterest.Time)
	}
}

func TestOpenInterestInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		minutes int
		want    string
	}{
		{minutes: 5, want: "5min"},
		{minutes: 15, want: "15min"},
		{minutes: 30, want: "30min"},
		{minutes: 60, want: "1h"},
		{minutes: 240, want: "4h"},
		{minutes: 1440, want: "1d"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.want, func(t *testing.T) {
			t.Parallel()

			got, err := openInterestInterval(test.minutes)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("interval = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGetOpenInterestRejectsUnsupportedWindow(t *testing.T) {
	t.Parallel()

	client := newClient(t, "https://api.example.test", nil)
	_, err := client.GetOpenInterest(context.Background(), "BTCUSDT", 10)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
}

func TestGetInstrumentRules(t *testing.T) {
	t.Parallel()

	httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v5/market/instruments-info" {
			t.Errorf("path = %s", r.URL.Path)
		}
		assertQuery(t, r, map[string]string{
			"category": linearCategory,
			"symbol":   "BTCUSDT",
			"limit":    "1",
		})

		return httpResponse(http.StatusOK, `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {
    "category": "linear",
    "list": [{
      "symbol": "BTCUSDT",
      "status": "Trading",
      "leverageFilter": {
        "minLeverage": "1",
        "maxLeverage": "100",
        "leverageStep": "0.01"
      },
      "priceFilter": {
        "minPrice": "0.10",
        "maxPrice": "1999999.80",
        "tickSize": "0.10"
      },
      "lotSizeFilter": {
        "minNotionalValue": "5",
        "maxOrderQty": "100",
        "maxMktOrderQty": "50",
        "minOrderQty": "0.001",
        "qtyStep": "0.001"
      }
    }]
  },
  "time": 180100
}`), nil
	})}

	client := newClient(t, "https://api.example.test", httpClient)
	rules, err := client.GetInstrumentRules(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if rules.Symbol != "BTCUSDT" || rules.Status != "Trading" {
		t.Fatalf("identity = %q/%q", rules.Symbol, rules.Status)
	}
	if rules.MinLeverage != 1 || rules.MaxLeverage != 100 || rules.LeverageStep != 0.01 {
		t.Fatalf("leverage rules = %#v", rules)
	}
	if rules.TickSize != 0.1 || rules.MinPrice != 0.1 || rules.MaxPrice != 1999999.8 {
		t.Fatalf("price rules = %#v", rules)
	}
	if rules.QtyStep != 0.001 || rules.MinOrderQty != 0.001 ||
		rules.MaxOrderQty != 100 || rules.MaxMarketOrderQty != 50 ||
		rules.MinNotionalValue != 5 {
		t.Fatalf("quantity rules = %#v", rules)
	}
}

func TestGetInstrumentRulesRejectsMalformedResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  string
		wantErr string
	}{
		{
			name:    "empty list",
			result:  `{"category":"linear","list":[]}`,
			wantErr: ErrEmptyResult.Error(),
		},
		{
			name: "unexpected symbol",
			result: `{"category":"linear","list":[{
				"symbol":"ETHUSDT","status":"Trading",
				"leverageFilter":{"minLeverage":"1","maxLeverage":"2","leverageStep":"1"},
				"priceFilter":{"minPrice":"1","maxPrice":"2","tickSize":"1"},
				"lotSizeFilter":{"minNotionalValue":"1","maxOrderQty":"2","maxMktOrderQty":"2","minOrderQty":"1","qtyStep":"1"}
			}]}`,
			wantErr: "unexpected symbol",
		},
		{
			name: "invalid step",
			result: `{"category":"linear","list":[{
				"symbol":"BTCUSDT","status":"Trading",
				"leverageFilter":{"minLeverage":"1","maxLeverage":"2","leverageStep":"1"},
				"priceFilter":{"minPrice":"1","maxPrice":"2","tickSize":"0"},
				"lotSizeFilter":{"minNotionalValue":"1","maxOrderQty":"2","maxMktOrderQty":"2","minOrderQty":"1","qtyStep":"1"}
			}]}`,
			wantErr: "invalid tickSize",
		},
		{
			name: "invalid leverage step",
			result: `{"category":"linear","list":[{
				"symbol":"BTCUSDT","status":"Trading",
				"leverageFilter":{"minLeverage":"1","maxLeverage":"2","leverageStep":"0"},
				"priceFilter":{"minPrice":"1","maxPrice":"2","tickSize":"1"},
				"lotSizeFilter":{"minNotionalValue":"1","maxOrderQty":"2","maxMktOrderQty":"2","minOrderQty":"1","qtyStep":"1"}
			}]}`,
			wantErr: "invalid leverageStep",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := clientForResponse(t, http.StatusOK, fmt.Sprintf(`{
				"retCode":0,
				"retMsg":"OK",
				"result":%s,
				"time":180100
			}`, test.result))
			_, err := client.GetInstrumentRules(context.Background(), "BTCUSDT")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestGetLastPrice(t *testing.T) {
	t.Parallel()

	httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v5/market/tickers" {
			t.Errorf("path = %s", r.URL.Path)
		}
		assertQuery(t, r, map[string]string{
			"category": linearCategory,
			"symbol":   "BTCUSDT",
		})
		return httpResponse(http.StatusOK, `{
			"retCode":0,
			"retMsg":"OK",
			"result":{
				"category":"linear",
				"list":[{
					"symbol":"BTCUSDT",
					"lastPrice":"50000.1",
					"markPrice":"50001.2",
					"indexPrice":"50002.3"
				}]
			},
			"time":180100
		}`), nil
	})}

	client := newClient(t, "https://api.example.test", httpClient)
	price, err := client.GetLastPrice(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if price.Symbol != "BTCUSDT" || price.LastPrice != 50000.1 ||
		price.MarkPrice != 50001.2 || price.IndexPrice != 50002.3 {
		t.Fatalf("price = %#v", price)
	}
	if !price.Time.Equal(time.UnixMilli(180100).UTC()) {
		t.Fatalf("Time = %s", price.Time)
	}
}

func TestGetLeverageSignsRequest(t *testing.T) {
	t.Parallel()

	httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v5/position/list" {
			t.Errorf("path = %s", r.URL.Path)
		}
		assertQuery(t, r, map[string]string{
			"category": linearCategory,
			"symbol":   "BTCUSDT",
		})
		headers := map[string]string{
			"X-BAPI-API-KEY":     "test-key",
			"X-BAPI-TIMESTAMP":   "1658384314791",
			"X-BAPI-RECV-WINDOW": "5000",
			"X-BAPI-SIGN":        "acbce6f124ccb7a0f1a447455f5ade0ab2b18de10682b04f40e02d260e338d26",
		}
		for name, expected := range headers {
			if actual := r.Header.Get(name); actual != expected {
				t.Errorf("%s = %q, want %q", name, actual, expected)
			}
		}
		return httpResponse(http.StatusOK, `{
			"retCode":0,
			"retMsg":"OK",
			"result":{
				"category":"linear",
				"list":[{
					"symbol":"BTCUSDT",
					"leverage":"10",
					"side":"",
					"positionIdx":0
				}]
			},
			"time":1658384314800
		}`), nil
	})}

	client := newAuthenticatedClient(t, "https://api.example.test", "test-key", "test-secret", httpClient)
	client.now = func() time.Time { return time.UnixMilli(1658384314791).UTC() }
	info, err := client.GetLeverage(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if info.Symbol != "BTCUSDT" || info.Leverage != 10 {
		t.Fatalf("info = %#v", info)
	}
	if !info.Time.Equal(time.UnixMilli(1658384314800).UTC()) {
		t.Fatalf("Time = %s", info.Time)
	}
}

func TestGetLeverageRejectsHedgeMode(t *testing.T) {
	t.Parallel()

	client := authenticatedClientForResponse(t, http.StatusOK, `{
		"retCode":0,
		"retMsg":"OK",
		"result":{
			"category":"linear",
			"list":[
				{"symbol":"BTCUSDT","leverage":"10","positionIdx":1},
				{"symbol":"BTCUSDT","leverage":"10","positionIdx":2}
			]
		},
		"time":1658384314800
	}`)
	_, err := client.GetLeverage(context.Background(), "BTCUSDT")
	if !errors.Is(err, ErrUnsupportedPositionMode) {
		t.Fatalf("error = %v, want ErrUnsupportedPositionMode", err)
	}
}

func TestGetLeverageRejectsMalformedResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  string
		time    int64
		wantErr string
		wantIs  error
	}{
		{
			name:   "empty list",
			result: `{"category":"linear","list":[]}`,
			time:   1,
			wantIs: ErrEmptyResult,
		},
		{
			name:    "wrong category",
			result:  `{"category":"inverse","list":[{"symbol":"BTCUSDT","leverage":"1","positionIdx":0}]}`,
			time:    1,
			wantErr: "unexpected category",
		},
		{
			name:    "missing position index",
			result:  `{"category":"linear","list":[{"symbol":"BTCUSDT","leverage":"1"}]}`,
			time:    1,
			wantErr: "positionIdx is missing",
		},
		{
			name:    "wrong symbol",
			result:  `{"category":"linear","list":[{"symbol":"ETHUSDT","leverage":"1","positionIdx":0}]}`,
			time:    1,
			wantErr: "unexpected symbol",
		},
		{
			name:    "zero leverage",
			result:  `{"category":"linear","list":[{"symbol":"BTCUSDT","leverage":"0","positionIdx":0}]}`,
			time:    1,
			wantErr: "invalid leverage",
		},
		{
			name:    "missing server time",
			result:  `{"category":"linear","list":[{"symbol":"BTCUSDT","leverage":"1","positionIdx":0}]}`,
			wantErr: "invalid server time",
		},
		{
			name:    "duplicate one-way record",
			result:  `{"category":"linear","list":[{"positionIdx":0},{"positionIdx":0}]}`,
			time:    1,
			wantErr: "expected one one-way leverage",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := authenticatedClientForResponse(t, http.StatusOK, fmt.Sprintf(`{
				"retCode":0,
				"retMsg":"OK",
				"result":%s,
				"time":%d
			}`, test.result, test.time))
			_, err := client.GetLeverage(context.Background(), "BTCUSDT")
			if test.wantIs != nil {
				if !errors.Is(err, test.wantIs) {
					t.Fatalf("error = %v, want %v", err, test.wantIs)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestGetAccountInfoSignsRequest(t *testing.T) {
	t.Parallel()

	httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v5/account/info" {
			t.Errorf("path = %s", r.URL.Path)
		}
		assertQuery(t, r, map[string]string{})
		headers := map[string]string{
			"X-BAPI-API-KEY":     "test-key",
			"X-BAPI-TIMESTAMP":   "1658384314791",
			"X-BAPI-RECV-WINDOW": "5000",
			"X-BAPI-SIGN":        "c6ce9f5b0b7806bb5e58cb7afcfacb29f9de4472fd08286adfe698bde1cdc6eb",
		}
		for name, expected := range headers {
			if actual := r.Header.Get(name); actual != expected {
				t.Errorf("%s = %q, want %q", name, actual, expected)
			}
		}
		return httpResponse(http.StatusOK, `{
			"retCode":0,
			"retMsg":"OK",
			"result":{
				"unifiedMarginStatus":5,
				"marginMode":"REGULAR_MARGIN",
				"updatedTime":"1697078946000"
			}
		}`), nil
	})}

	client := newAuthenticatedClient(t, "https://api.example.test", "test-key", "test-secret", httpClient)
	client.now = func() time.Time { return time.UnixMilli(1658384314791).UTC() }
	info, err := client.GetAccountInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.UnifiedMarginStatus != 5 || info.MarginMode != exchange.MarginModeRegular {
		t.Fatalf("info = %#v", info)
	}
	if !info.UpdatedAt.Equal(time.UnixMilli(1697078946000).UTC()) {
		t.Fatalf("UpdatedAt = %s", info.UpdatedAt)
	}
}

func TestParseAccountInfoRejectsMalformedResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  accountInfoResult
		wantErr string
	}{
		{
			name:    "missing unified status",
			result:  accountInfoResult{MarginMode: "REGULAR_MARGIN", UpdatedTime: "1"},
			wantErr: "invalid unifiedMarginStatus",
		},
		{
			name:    "unsupported margin mode",
			result:  accountInfoResult{UnifiedMarginStatus: 5, MarginMode: "UNKNOWN", UpdatedTime: "1"},
			wantErr: "unsupported marginMode",
		},
		{
			name:    "invalid updated time",
			result:  accountInfoResult{UnifiedMarginStatus: 5, MarginMode: "REGULAR_MARGIN", UpdatedTime: "0"},
			wantErr: "invalid updatedTime",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseAccountInfo(test.result)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestGetAccountBalanceSignsRequest(t *testing.T) {
	t.Parallel()

	httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v5/account/wallet-balance" {
			t.Errorf("path = %s", r.URL.Path)
		}
		assertQuery(t, r, map[string]string{"accountType": unifiedAccountType})
		headers := map[string]string{
			"X-BAPI-API-KEY":     "test-key",
			"X-BAPI-TIMESTAMP":   "1658384314791",
			"X-BAPI-RECV-WINDOW": "5000",
			"X-BAPI-SIGN":        "edbffe3f3eeb9baf6b94d4564b43c1ab691113041c2c533b9ccb6ce114c0e930",
		}
		for name, expected := range headers {
			if actual := r.Header.Get(name); actual != expected {
				t.Errorf("%s = %q, want %q", name, actual, expected)
			}
		}
		return httpResponse(http.StatusOK, `{
			"retCode":0,
			"retMsg":"OK",
			"result":{"list":[{
				"accountType":"UNIFIED",
				"totalAvailableBalance":"1000.5",
				"totalEquity":"1200.25",
				"totalWalletBalance":"1100.75"
			}]},
			"time":1658384314800
		}`), nil
	})}

	client := newAuthenticatedClient(t, "https://api.example.test", "test-key", "test-secret", httpClient)
	client.now = func() time.Time { return time.UnixMilli(1658384314791).UTC() }
	balance, err := client.GetAccountBalance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if balance.AvailableUSD != 1000.5 || balance.EquityUSD != 1200.25 || balance.WalletUSD != 1100.75 {
		t.Fatalf("balance = %#v", balance)
	}
	if !balance.Time.Equal(time.UnixMilli(1658384314800).UTC()) {
		t.Fatalf("Time = %s", balance.Time)
	}
}

func TestGetAccountBalanceRequiresCredentialsWithoutRequest(t *testing.T) {
	t.Parallel()

	requests := 0
	client := newClient(t, "https://api.example.test", &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return nil, errors.New("unexpected request")
		}),
	})
	_, err := client.GetAccountBalance(context.Background())
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestAuthenticatedClientFormattingDoesNotExposeCredentials(t *testing.T) {
	t.Parallel()

	client := newAuthenticatedClient(t, "https://api.example.test", "test-key", "test-secret", nil)
	formatted := fmt.Sprintf("%#v", client)
	if strings.Contains(formatted, "test-key") || strings.Contains(formatted, "test-secret") {
		t.Fatalf("formatted client exposes credentials: %s", formatted)
	}
}

func TestParseMarketPriceRejectsMalformedResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		item       tickerItem
		serverTime int64
		wantErr    string
	}{
		{
			name: "wrong symbol",
			item: tickerItem{
				Symbol: "ETHUSDT", LastPrice: "1", MarkPrice: "1", IndexPrice: "1",
			},
			serverTime: 1,
			wantErr:    "unexpected symbol",
		},
		{
			name: "missing server time",
			item: tickerItem{
				Symbol: "BTCUSDT", LastPrice: "1", MarkPrice: "1", IndexPrice: "1",
			},
			wantErr: "invalid server time",
		},
		{
			name: "zero last price",
			item: tickerItem{
				Symbol: "BTCUSDT", LastPrice: "0", MarkPrice: "1", IndexPrice: "1",
			},
			serverTime: 1,
			wantErr:    "invalid lastPrice",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseMarketPrice(test.item, "BTCUSDT", test.serverTime)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestParseAccountBalanceRejectsMalformedResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		item       walletBalanceItem
		serverTime int64
		wantErr    string
	}{
		{
			name: "wrong account type",
			item: walletBalanceItem{
				AccountType: "CONTRACT", TotalAvailableBalance: "1", TotalEquity: "1", TotalWalletBalance: "1",
			},
			serverTime: 1,
			wantErr:    "unexpected account type",
		},
		{
			name: "missing server time",
			item: walletBalanceItem{
				AccountType: "UNIFIED", TotalAvailableBalance: "1", TotalEquity: "1", TotalWalletBalance: "1",
			},
			wantErr: "invalid server time",
		},
		{
			name: "negative available balance",
			item: walletBalanceItem{
				AccountType: "UNIFIED", TotalAvailableBalance: "-1", TotalEquity: "1", TotalWalletBalance: "1",
			},
			serverTime: 1,
			wantErr:    "invalid totalAvailableBalance",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseAccountBalance(test.item, test.serverTime)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestGetCandlesRejectsInvalidArgumentsWithoutRequest(t *testing.T) {
	requests := 0
	client := newClient(t, "https://example.com", &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return nil, errors.New("unexpected request")
		}),
	})

	tests := []struct {
		name   string
		symbol string
		limit  int
	}{
		{name: "empty symbol", symbol: "", limit: 1},
		{name: "lowercase symbol", symbol: "btcusdt", limit: 1},
		{name: "surrounding whitespace", symbol: " BTCUSDT", limit: 1},
		{name: "zero limit", symbol: "BTCUSDT", limit: 0},
		{name: "limit above maximum", symbol: "BTCUSDT", limit: maxKlineLimit + 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.GetCandles(context.Background(), test.symbol, test.limit)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}

	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestNewClientRejectsInvalidBaseURL(t *testing.T) {
	client, err := NewClient("://invalid", nil)
	if client != nil {
		t.Fatalf("client = %#v, want nil", client)
	}
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
}

func TestClientReturnsAPIError(t *testing.T) {
	client := clientForResponse(t, http.StatusOK, `{
  "retCode": 10001,
  "retMsg": "invalid request",
  "result": {},
  "time": 180000
}`)

	_, err := client.GetOpenInterest(context.Background(), "BTCUSDT", 5)
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiError.Code != 10001 || apiError.Message != "invalid request" {
		t.Errorf("unexpected API error: %+v", apiError)
	}
}

func TestClientReturnsHTTPError(t *testing.T) {
	client := clientForResponse(t, http.StatusTooManyRequests, "rate limited")

	_, err := client.GetOpenInterest(context.Background(), "BTCUSDT", 5)
	var httpError *HTTPError
	if !errors.As(err, &httpError) {
		t.Fatalf("error = %v, want *HTTPError", err)
	}
	if httpError.StatusCode != http.StatusTooManyRequests || httpError.Body != "rate limited" {
		t.Errorf("unexpected HTTP error: %+v", httpError)
	}
}

func TestClientRejectsMalformedResponses(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "invalid JSON",
			body:    `{`,
			wantErr: "decode response envelope",
		},
		{
			name:    "missing retCode",
			body:    `{"retMsg":"OK","result":{},"time":180000}`,
			wantErr: "retCode is missing",
		},
		{
			name:    "missing result",
			body:    `{"retCode":0,"retMsg":"OK","time":180000}`,
			wantErr: "result is missing",
		},
		{
			name: "invalid numeric value",
			body: `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {
    "category": "linear",
    "symbol": "BTCUSDT",
    "list": [{"openInterest": "NaN", "timestamp": "180000"}]
  },
  "time": 180100
}`,
			wantErr: "invalid openInterest",
		},
		{
			name: "unexpected symbol",
			body: `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {"category": "linear", "symbol": "ETHUSDT", "list": []},
  "time": 180100
}`,
			wantErr: "unexpected symbol",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := clientForResponse(t, http.StatusOK, test.body)
			_, err := client.GetOpenInterest(context.Background(), "BTCUSDT", 5)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestClientRejectsEmptyResults(t *testing.T) {
	client := clientForResponse(t, http.StatusOK, `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {"category": "linear", "symbol": "BTCUSDT", "list": []},
  "time": 180100
}`)

	_, err := client.GetOpenInterest(context.Background(), "BTCUSDT", 5)
	if !errors.Is(err, ErrEmptyResult) {
		t.Fatalf("error = %v, want ErrEmptyResult", err)
	}
}

func TestGetCandlesRejectsMalformedItem(t *testing.T) {
	client := clientForResponse(t, http.StatusOK, `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {
    "category": "linear",
    "symbol": "BTCUSDT",
    "list": [["180000", "100"]]
  },
  "time": 180100
}`)

	_, err := client.GetCandles(context.Background(), "BTCUSDT", 1)
	if err == nil || !strings.Contains(err.Error(), "expected at least 7 fields") {
		t.Fatalf("error = %v", err)
	}
}

func TestGetCandlesRejectsEmptyResult(t *testing.T) {
	client := clientForResponse(t, http.StatusOK, `{
  "retCode": 0,
  "retMsg": "OK",
  "result": {"category": "linear", "symbol": "BTCUSDT", "list": []},
  "time": 180100
}`)

	_, err := client.GetCandles(context.Background(), "BTCUSDT", 1)
	if !errors.Is(err, ErrEmptyResult) {
		t.Fatalf("error = %v, want ErrEmptyResult", err)
	}
}

func TestGetCandlesRejectsInvalidKlineData(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "invalid server time",
			body: `{
  "retCode": 0,
  "result": {"category": "linear", "symbol": "BTCUSDT", "list": [["60000", "100", "103", "99", "102", "1.5", "151.5"]]},
  "time": 0
}`,
			wantErr: "invalid server time",
		},
		{
			name: "unexpected category",
			body: `{
  "retCode": 0,
  "result": {"category": "inverse", "symbol": "BTCUSDT", "list": [["60000", "100", "103", "99", "102", "1.5", "151.5"]]},
  "time": 180100
}`,
			wantErr: "unexpected category",
		},
		{
			name: "non-positive price",
			body: `{
  "retCode": 0,
  "result": {"category": "linear", "symbol": "BTCUSDT", "list": [["60000", "0", "103", "99", "102", "1.5", "151.5"]]},
  "time": 180100
}`,
			wantErr: "invalid open",
		},
		{
			name: "negative volume",
			body: `{
  "retCode": 0,
  "result": {"category": "linear", "symbol": "BTCUSDT", "list": [["60000", "100", "103", "99", "102", "-1", "151.5"]]},
  "time": 180100
}`,
			wantErr: "invalid volume",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := clientForResponse(t, http.StatusOK, test.body)
			_, err := client.GetCandles(context.Background(), "BTCUSDT", 1)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestClientRejectsOversizedResponse(t *testing.T) {
	client := clientForResponse(t, http.StatusOK, strings.Repeat("x", maxResponseBytes+1))

	_, err := client.GetOpenInterest(context.Background(), "BTCUSDT", 5)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
}

func TestClientPreservesContextCancellation(t *testing.T) {
	client := newClient(t, "https://example.com", &http.Client{
		Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetOpenInterest(ctx, "BTCUSDT", 5)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestPrivateMethodsRemainNotImplemented(t *testing.T) {
	client := newClient(t, "https://example.com", nil)

	if _, err := client.PlaceOrder(context.Background(), exchange.OrderRequest{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("PlaceOrder error = %v", err)
	}
	if err := client.ClosePosition(context.Background(), "BTCUSDT"); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("ClosePosition error = %v", err)
	}
}

func assertQuery(t *testing.T, request *http.Request, expected map[string]string) {
	t.Helper()

	query := request.URL.Query()
	if len(query) != len(expected) {
		t.Errorf("query = %v, want %v", query, expected)
	}
	for key, want := range expected {
		if got := query.Get(key); got != want {
			t.Errorf("query[%s] = %q, want %q", key, got, want)
		}
	}
}

func newClient(t *testing.T, baseURL string, httpClient *http.Client) *Client {
	t.Helper()

	client, err := NewClient(baseURL, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newAuthenticatedClient(t *testing.T, baseURL, apiKey, apiSecret string, httpClient *http.Client) *Client {
	t.Helper()

	client, err := NewAuthenticatedClient(baseURL, apiKey, apiSecret, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func clientForResponse(t *testing.T, status int, body string) *Client {
	t.Helper()

	return newClient(t, "https://api.example.test", &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return httpResponse(status, body), nil
		}),
	})

}

func authenticatedClientForResponse(t *testing.T, status int, body string) *Client {
	t.Helper()

	client := newAuthenticatedClient(t, "https://api.example.test", "test-key", "test-secret", &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return httpResponse(status, body), nil
		}),
	})
	client.now = func() time.Time { return time.UnixMilli(1658384314791).UTC() }
	return client
}

func httpResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
