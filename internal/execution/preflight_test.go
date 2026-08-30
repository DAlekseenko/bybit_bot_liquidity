package execution

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

func TestAccountPreflightAcceptsUnifiedRegularMargin(t *testing.T) {
	t.Parallel()

	for _, status := range []int{3, 4, 5, 6} {
		status := status
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			expected := exchange.AccountInfo{
				UnifiedMarginStatus: status,
				MarginMode:          exchange.MarginModeRegular,
				UpdatedAt:           time.UnixMilli(1).UTC(),
			}
			info, err := NewAccountPreflight(accountInfoSourceStub{info: expected}).Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if info != expected {
				t.Fatalf("info = %#v, want %#v", info, expected)
			}
		})
	}
}

func TestAccountPreflightRejectsUnsupportedAccountMode(t *testing.T) {
	t.Parallel()

	for _, status := range []int{0, 1, 7} {
		status := status
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			_, err := NewAccountPreflight(accountInfoSourceStub{info: exchange.AccountInfo{
				UnifiedMarginStatus: status,
				MarginMode:          exchange.MarginModeRegular,
				UpdatedAt:           time.UnixMilli(1).UTC(),
			}}).Check(context.Background())
			if !errors.Is(err, ErrUnsupportedAccountMode) {
				t.Fatalf("error = %v, want ErrUnsupportedAccountMode", err)
			}
		})
	}
}

func TestAccountPreflightRejectsUnsupportedMarginMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []exchange.MarginMode{
		exchange.MarginModeIsolated,
		exchange.MarginModePortfolio,
	} {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()

			_, err := NewAccountPreflight(accountInfoSourceStub{info: exchange.AccountInfo{
				UnifiedMarginStatus: 5,
				MarginMode:          mode,
				UpdatedAt:           time.UnixMilli(1).UTC(),
			}}).Check(context.Background())
			if !errors.Is(err, ErrUnsupportedMarginMode) {
				t.Fatalf("error = %v, want ErrUnsupportedMarginMode", err)
			}
		})
	}
}

func TestAccountPreflightRejectsMissingUpdatedTime(t *testing.T) {
	t.Parallel()

	_, err := NewAccountPreflight(accountInfoSourceStub{info: exchange.AccountInfo{
		UnifiedMarginStatus: 5,
		MarginMode:          exchange.MarginModeRegular,
	}}).Check(context.Background())
	if !errors.Is(err, ErrInvalidAccountInfo) {
		t.Fatalf("error = %v, want ErrInvalidAccountInfo", err)
	}
}

func TestAccountPreflightRequiresSource(t *testing.T) {
	t.Parallel()

	var preflight *AccountPreflight
	if _, err := preflight.Check(context.Background()); !errors.Is(err, ErrAccountInfoSourceRequired) {
		t.Fatalf("nil preflight error = %v", err)
	}
	if _, err := NewAccountPreflight(nil).Check(context.Background()); !errors.Is(err, ErrAccountInfoSourceRequired) {
		t.Fatalf("nil source error = %v", err)
	}
}

func TestAccountPreflightPreservesSourceError(t *testing.T) {
	t.Parallel()

	expected := errors.New("account unavailable")
	_, err := NewAccountPreflight(accountInfoSourceStub{err: expected}).Check(context.Background())
	if !errors.Is(err, expected) {
		t.Fatalf("error = %v, want source error", err)
	}
}

type accountInfoSourceStub struct {
	info exchange.AccountInfo
	err  error
}

func (s accountInfoSourceStub) GetAccountInfo(context.Context) (exchange.AccountInfo, error) {
	return s.info, s.err
}
