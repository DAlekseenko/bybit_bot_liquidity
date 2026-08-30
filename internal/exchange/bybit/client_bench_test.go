package bybit

import (
	"net/url"
	"testing"
)

func BenchmarkEndpoint(b *testing.B) {
	client, err := NewClient("https://api.example.test", nil)
	if err != nil {
		b.Fatal(err)
	}
	query := url.Values{
		"category": {"linear"},
		"symbol":   {"BTCUSDT"},
		"interval": {"1"},
		"limit":    {"10"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = client.endpoint("/v5/market/kline", query)
	}
}
