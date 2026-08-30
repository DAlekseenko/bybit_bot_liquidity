//go:build integration

package bybit

import (
	"context"
	"os"
	"testing"
	"time"
)

const testnetRESTURL = "https://api-testnet.bybit.com"

func TestTestnetPublicReadOnlyContract(t *testing.T) {
	requireContractTests(t)

	client, err := NewClient(testnetRESTURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	candles, err := client.GetCandles(ctx, "BTCUSDT", 22)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 22 {
		t.Fatalf("candles = %d, want 22", len(candles))
	}
	if candles[0].OpenTime.After(candles[len(candles)-1].OpenTime) {
		t.Fatal("candles are not chronological")
	}

	openInterest, err := client.GetOpenInterest(ctx, "BTCUSDT", 5)
	if err != nil {
		t.Fatal(err)
	}
	if openInterest.Value <= 0 || openInterest.Time.IsZero() {
		t.Fatalf("invalid open interest: %#v", openInterest)
	}

	price, err := client.GetLastPrice(ctx, "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if price.LastPrice <= 0 || price.MarkPrice <= 0 {
		t.Fatalf("invalid market price: %#v", price)
	}

	rules, err := client.GetInstrumentRules(ctx, "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if rules.Symbol != "BTCUSDT" || rules.QtyStep <= 0 || rules.TickSize <= 0 {
		t.Fatalf("invalid instrument rules: %#v", rules)
	}
}

func TestTestnetSignedReadOnlyContract(t *testing.T) {
	requireContractTests(t)
	apiKey, apiSecret := os.Getenv("BYBIT_API_KEY"), os.Getenv("BYBIT_API_SECRET")
	if apiKey == "" || apiSecret == "" {
		t.Skip("BYBIT_API_KEY and BYBIT_API_SECRET are not configured")
	}

	client, err := NewAuthenticatedClient(testnetRESTURL, apiKey, apiSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := client.GetAccountInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetAccountBalance(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetPosition(ctx, "BTCUSDT"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetLeverage(ctx, "BTCUSDT"); err != nil {
		t.Fatal(err)
	}
}

func requireContractTests(t *testing.T) {
	t.Helper()
	if os.Getenv("BYBIT_TESTNET_CONTRACT_TESTS") != "1" {
		t.Skip("set BYBIT_TESTNET_CONTRACT_TESTS=1 to make live Testnet requests")
	}
}
