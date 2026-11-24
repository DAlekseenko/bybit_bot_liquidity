package config

import (
	"strings"
	"testing"
	"time"
)

// testnetBlock — корректный блок testnet для тестов.
const testnetBlock = `testnet:
  account_id: sub-1
  trading_enabled: false
  stop_enabled: false
  max_data_age: 2m
  poll_interval: 30s
  rest_deadline: 10s
  slippage_rate: "0.005"
  margin_buffer_rate: "0.010"
  equity_tolerance_usdt: "0.01"
`

// testnetYAML возвращает корректную testnet-конфигурацию.
func testnetYAML() string {
	return strings.Replace(validYAML(), "start_policy: now", "mode: testnet\nstart_policy: now", 1) + testnetBlock
}

// TestTestnetExample проверяет, что пример загружается с закрытой отправкой заявок.
func TestTestnetExample(t *testing.T) {
	t.Setenv("MOMENTUM_TESTNET_API_SECRET", "super-secret-value")
	c, err := Load("../../../configs/momentum.testnet.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	v := c.Values()
	if v.Mode != "testnet" || v.Testnet == nil || v.Testnet.TradingEnabled || v.Testnet.StopEnabled || v.DataSource.Environment != "testnet" {
		t.Fatalf("unsafe example: %+v", v)
	}
	b, _, err := c.Snapshot()
	if err != nil || !strings.Contains(string(b), `"testnet":{`) || strings.Contains(string(b), "super-secret-value") {
		t.Fatalf("snapshot: %s %v", b, err)
	}
}

// TestTestnetValues проверяет канонизацию значений и независимость копии Values.
func TestTestnetValues(t *testing.T) {
	c, err := Parse([]byte(testnetYAML()))
	if err != nil {
		t.Fatal(err)
	}
	got := *c.Values().Testnet
	want := Testnet{AccountID: "sub-1", MaxDataAge: 2 * time.Minute, PollInterval: 30 * time.Second, RESTDeadline: 10 * time.Second,
		SlippageRate: "0.005", MarginBufferRate: "0.01", EquityToleranceUSDT: "0.01"}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	c.Values().Testnet.TradingEnabled = true
	if c.Values().Testnet.TradingEnabled {
		t.Fatal("Values exposed mutable testnet block")
	}
	paper, err := Parse([]byte(validYAML()))
	if err != nil {
		t.Fatal(err)
	}
	if b, _, _ := paper.Snapshot(); strings.Contains(string(b), "testnet\":{") || paper.Values().Testnet != nil {
		t.Fatalf("paper snapshot changed: %s", b)
	}
}

// TestTestnetRejects проверяет fail-closed отказ для неполных и запрещённых параметров.
func TestTestnetRejects(t *testing.T) {
	cases := map[string]string{
		"no block":         strings.Replace(validYAML(), "start_policy: now", "mode: testnet\nstart_policy: now", 1),
		"block in paper":   validYAML() + testnetBlock,
		"stop enabled":     strings.Replace(testnetYAML(), "stop_enabled: false", "stop_enabled: true", 1),
		"reset profile":    strings.Replace(strings.Replace(testnetYAML(), "profile_version: v1", "profile: isolated_weekly_reset\nprofile_version: v1", 1), "leverage: 1", "leverage: 2", 1),
		"mainnet data":     strings.Replace(testnetYAML(), "environment: testnet", "environment: mainnet", 1),
		"frozen data":      strings.Replace(testnetYAML(), "kind: bybit_testnet_public", "kind: frozen_dataset", 1),
		"bad account":      strings.Replace(testnetYAML(), "account_id: sub-1", "account_id: ../x", 1),
		"zero age":         strings.Replace(testnetYAML(), "max_data_age: 2m", "max_data_age: 0s", 1),
		"negative poll":    strings.Replace(testnetYAML(), "poll_interval: 30s", "poll_interval: -30s", 1),
		"garbage deadline": strings.Replace(testnetYAML(), "rest_deadline: 10s", "rest_deadline: soon", 1),
		"huge poll":        strings.Replace(testnetYAML(), "poll_interval: 30s", "poll_interval: 25h", 1),
		"deadline > age":   strings.Replace(testnetYAML(), "rest_deadline: 10s", "rest_deadline: 3m", 1),
		"slippage one":     strings.Replace(testnetYAML(), `slippage_rate: "0.005"`, `slippage_rate: "1"`, 1),
		"negative buffer":  strings.Replace(testnetYAML(), `margin_buffer_rate: "0.010"`, `margin_buffer_rate: "-0.1"`, 1),
		"bad tolerance":    strings.Replace(testnetYAML(), `equity_tolerance_usdt: "0.01"`, `equity_tolerance_usdt: "x"`, 1),
		"unknown field":    testnetYAML() + "  api_key: super-secret-value\n",
		"live mode":        strings.Replace(testnetYAML(), "mode: testnet", "mode: live", 1),
	}
	for _, field := range []string{"account_id", "trading_enabled", "stop_enabled", "max_data_age", "poll_interval", "rest_deadline",
		"slippage_rate", "margin_buffer_rate", "equity_tolerance_usdt"} {
		var kept []string
		for _, line := range strings.Split(testnetYAML(), "\n") {
			if !strings.HasPrefix(line, "  "+field+":") {
				kept = append(kept, line)
			}
		}
		cases["missing "+field] = strings.Join(kept, "\n")
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil || strings.Contains(err.Error(), "super-secret-value") {
			t.Errorf("%s: accepted or leaked: %v", name, err)
		}
	}
}
