package marketdata

import (
	"context"
	"errors"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

func TestCollectorSnapshot(t *testing.T) {
	exchangeMock := &exchangeMock{
		candles: []exchange.Candle{{Symbol: "BTCUSDT", Volume: 10}},
		openInterest: exchange.OpenInterest{
			Symbol: "BTCUSDT",
			Time:   time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
			Value:  123,
		},
	}
	collector := NewCollector(exchangeMock, "BTCUSDT", 15)
	liquidations := []exchange.Liquidation{{Symbol: "BTCUSDT", Side: exchange.SideShort, Price: 100, Qty: 2}}

	snapshot, err := collector.Snapshot(context.Background(), 21, liquidations)
	if err != nil {
		t.Fatal(err)
	}

	if exchangeMock.candleLimit != 21 {
		t.Fatalf("candle limit = %d, want 21", exchangeMock.candleLimit)
	}
	if exchangeMock.oiWindowMinutes != 15 {
		t.Fatalf("OI window = %d, want 15", exchangeMock.oiWindowMinutes)
	}
	if len(snapshot.Candles) != 1 {
		t.Fatalf("len(Candles) = %d, want 1", len(snapshot.Candles))
	}
	if snapshot.OpenInterest.Value != 123 {
		t.Fatalf("OpenInterest.Value = %f, want 123", snapshot.OpenInterest.Value)
	}
	if len(snapshot.Liquidations) != 1 {
		t.Fatalf("len(Liquidations) = %d, want 1", len(snapshot.Liquidations))
	}
	if snapshot.CollectedAt.IsZero() {
		t.Fatal("CollectedAt must be set")
	}
}

func TestCollectorSnapshotReturnsCandleError(t *testing.T) {
	wantErr := errors.New("candles failed")
	collector := NewCollector(&exchangeMock{candlesErr: wantErr}, "BTCUSDT", 5)

	_, err := collector.Snapshot(context.Background(), 21, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestCollectorSnapshotReturnsOpenInterestError(t *testing.T) {
	wantErr := errors.New("open interest failed")
	collector := NewCollector(&exchangeMock{openInterestErr: wantErr}, "BTCUSDT", 5)

	_, err := collector.Snapshot(context.Background(), 21, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestCollectorDeduplicatesAndOrdersCandles(t *testing.T) {
	t.Parallel()

	earlier := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Minute)
	collector := NewCollector(&exchangeMock{
		candles: []exchange.Candle{
			{OpenTime: later, Close: 102, Confirmed: false},
			{OpenTime: earlier, Close: 100, Confirmed: true},
			{OpenTime: later, Close: 101, Confirmed: true},
		},
		openInterest: exchange.OpenInterest{Value: 1},
	}, "BTCUSDT", 5)

	snapshot, err := collector.Snapshot(context.Background(), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Candles) != 2 {
		t.Fatalf("candles = %#v", snapshot.Candles)
	}
	if !snapshot.Candles[0].OpenTime.Equal(earlier) || snapshot.Candles[1].Close != 101 || !snapshot.Candles[1].Confirmed {
		t.Fatalf("normalized candles = %#v", snapshot.Candles)
	}
}

func TestCollectorRetriesClassifiedReadFailure(t *testing.T) {
	t.Parallel()

	collector := NewCollectorWithRetry(&exchangeMock{
		candles:          []exchange.Candle{{OpenTime: time.Now(), Confirmed: true}},
		candleFailures:   1,
		candleFailureErr: retryableError{},
		openInterest:     exchange.OpenInterest{Value: 1},
	}, "BTCUSDT", 5, RetryPolicy{MaxAttempts: 2, InitialDelay: time.Millisecond, MaxDelay: time.Millisecond})
	collector.sleep = func(context.Context, time.Duration) error { return nil }
	collector.random = func() float64 { return 0.5 }

	if _, err := collector.Snapshot(context.Background(), 1, nil); err != nil {
		t.Fatal(err)
	}
}

type exchangeMock struct {
	candles          []exchange.Candle
	candlesErr       error
	candleFailures   int
	candleFailureErr error
	openInterest     exchange.OpenInterest
	openInterestErr  error
	candleLimit      int
	oiWindowMinutes  int
}

func (m *exchangeMock) GetCandles(ctx context.Context, symbol string, limit int) ([]exchange.Candle, error) {
	m.candleLimit = limit
	if m.candleFailures > 0 {
		m.candleFailures--
		return nil, m.candleFailureErr
	}
	return m.candles, m.candlesErr
}

func (m *exchangeMock) GetOpenInterest(ctx context.Context, symbol string, windowMinutes int) (exchange.OpenInterest, error) {
	m.oiWindowMinutes = windowMinutes
	return m.openInterest, m.openInterestErr
}

type retryableError struct{}

func (retryableError) Error() string   { return "retryable" }
func (retryableError) Retryable() bool { return true }
