package execution

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/exchange"
)

func TestExecutionReadinessPreflightCheck(t *testing.T) {
	t.Parallel()

	source := testExecutionReadinessSource()
	got, err := NewExecutionReadinessPreflight(source).Check(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}

	want := ExecutionReadiness{
		Symbol:   "BTCUSDT",
		Account:  source.account,
		Balance:  source.balance,
		Price:    source.price,
		Leverage: source.leverage,
	}
	if got != want {
		t.Fatalf("Check() = %+v, want %+v", got, want)
	}
	wantCalls := []string{"account", "position", "balance", "price", "leverage", "rules"}
	if !reflect.DeepEqual(source.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", source.calls, wantCalls)
	}
}

func TestExecutionReadinessPreflightRejectsAccountAndPosition(t *testing.T) {
	t.Parallel()

	t.Run("unsupported account", func(t *testing.T) {
		t.Parallel()

		source := testExecutionReadinessSource()
		source.account.MarginMode = exchange.MarginModePortfolio
		_, err := NewExecutionReadinessPreflight(source).Check(context.Background(), "BTCUSDT")
		if !errors.Is(err, ErrUnsupportedMarginMode) {
			t.Fatalf("Check() error = %v, want %v", err, ErrUnsupportedMarginMode)
		}
		if !reflect.DeepEqual(source.calls, []string{"account"}) {
			t.Fatalf("calls = %v, want [account]", source.calls)
		}
	})

	t.Run("open position", func(t *testing.T) {
		t.Parallel()

		source := testExecutionReadinessSource()
		source.position = &exchange.Position{
			Symbol:     "BTCUSDT",
			Side:       exchange.SideLong,
			Qty:        0.1,
			EntryPrice: 50000,
			OpenedAt:   time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC),
		}
		_, err := NewExecutionReadinessPreflight(source).Check(context.Background(), "BTCUSDT")
		if !errors.Is(err, ErrPositionAlreadyOpen) {
			t.Fatalf("Check() error = %v, want %v", err, ErrPositionAlreadyOpen)
		}
		if !reflect.DeepEqual(source.calls, []string{"account", "position"}) {
			t.Fatalf("calls = %v, want [account position]", source.calls)
		}
	})
}

func TestExecutionReadinessPreflightRejectsInvalidBalance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(*exchange.AccountBalance)
		wantErr error
	}{
		{
			name:    "missing time",
			change:  func(balance *exchange.AccountBalance) { balance.Time = time.Time{} },
			wantErr: ErrInvalidAccountBalance,
		},
		{
			name:    "negative available",
			change:  func(balance *exchange.AccountBalance) { balance.AvailableUSD = -1 },
			wantErr: ErrInvalidAccountBalance,
		},
		{
			name:    "nan equity",
			change:  func(balance *exchange.AccountBalance) { balance.EquityUSD = math.NaN() },
			wantErr: ErrInvalidAccountBalance,
		},
		{
			name:    "infinite wallet",
			change:  func(balance *exchange.AccountBalance) { balance.WalletUSD = math.Inf(1) },
			wantErr: ErrInvalidAccountBalance,
		},
		{
			name:    "zero available",
			change:  func(balance *exchange.AccountBalance) { balance.AvailableUSD = 0 },
			wantErr: ErrNoAvailableBalance,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := testExecutionReadinessSource()
			test.change(&source.balance)
			_, err := NewExecutionReadinessPreflight(source).Check(context.Background(), "BTCUSDT")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Check() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestExecutionReadinessPreflightRejectsInvalidPrice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*exchange.MarketPrice)
	}{
		{
			name:   "symbol mismatch",
			change: func(price *exchange.MarketPrice) { price.Symbol = "ETHUSDT" },
		},
		{
			name:   "missing time",
			change: func(price *exchange.MarketPrice) { price.Time = time.Time{} },
		},
		{
			name:   "zero last price",
			change: func(price *exchange.MarketPrice) { price.LastPrice = 0 },
		},
		{
			name:   "nan mark price",
			change: func(price *exchange.MarketPrice) { price.MarkPrice = math.NaN() },
		},
		{
			name:   "infinite index price",
			change: func(price *exchange.MarketPrice) { price.IndexPrice = math.Inf(1) },
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := testExecutionReadinessSource()
			test.change(&source.price)
			_, err := NewExecutionReadinessPreflight(source).Check(context.Background(), "BTCUSDT")
			if !errors.Is(err, ErrInvalidMarketPrice) {
				t.Fatalf("Check() error = %v, want %v", err, ErrInvalidMarketPrice)
			}
		})
	}
}

func TestExecutionReadinessPreflightDelegatesLeverageCheck(t *testing.T) {
	t.Parallel()

	source := testExecutionReadinessSource()
	source.leverage.Leverage = 101
	_, err := NewExecutionReadinessPreflight(source).Check(context.Background(), "BTCUSDT")
	if !errors.Is(err, ErrLeverageOutOfRange) {
		t.Fatalf("Check() error = %v, want %v", err, ErrLeverageOutOfRange)
	}
}

func TestExecutionReadinessPreflightRequiresSourceAndSymbol(t *testing.T) {
	t.Parallel()

	var preflight *ExecutionReadinessPreflight
	if _, err := preflight.Check(context.Background(), "BTCUSDT"); !errors.Is(err, ErrExecutionReadinessSourceRequired) {
		t.Fatalf("nil Check() error = %v, want %v", err, ErrExecutionReadinessSourceRequired)
	}
	if _, err := NewExecutionReadinessPreflight(nil).Check(context.Background(), "BTCUSDT"); !errors.Is(err, ErrExecutionReadinessSourceRequired) {
		t.Fatalf("Check() error = %v, want %v", err, ErrExecutionReadinessSourceRequired)
	}

	source := testExecutionReadinessSource()
	if _, err := NewExecutionReadinessPreflight(source).Check(context.Background(), ""); !errors.Is(err, ErrExecutionSymbolRequired) {
		t.Fatalf("empty symbol error = %v, want %v", err, ErrExecutionSymbolRequired)
	}
	if len(source.calls) != 0 {
		t.Fatalf("calls = %v, want none", source.calls)
	}
}

func TestExecutionReadinessPreflightPreservesSourceErrors(t *testing.T) {
	t.Parallel()

	stages := []string{"account", "position", "balance", "price", "leverage", "rules"}
	for _, stage := range stages {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()

			expected := errors.New(stage + " unavailable")
			source := testExecutionReadinessSource()
			source.errs[stage] = expected
			_, err := NewExecutionReadinessPreflight(source).Check(context.Background(), "BTCUSDT")
			if !errors.Is(err, expected) {
				t.Fatalf("Check() error = %v, want wrapped %v", err, expected)
			}
		})
	}
}

type executionReadinessSourceStub struct {
	account  exchange.AccountInfo
	position *exchange.Position
	balance  exchange.AccountBalance
	price    exchange.MarketPrice
	leverage exchange.LeverageInfo
	rules    exchange.InstrumentRules
	errs     map[string]error
	calls    []string
}

func testExecutionReadinessSource() *executionReadinessSourceStub {
	at := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	return &executionReadinessSourceStub{
		account: exchange.AccountInfo{
			UnifiedMarginStatus: 5,
			MarginMode:          exchange.MarginModeRegular,
			UpdatedAt:           at,
		},
		balance: exchange.AccountBalance{
			AvailableUSD: 1000,
			EquityUSD:    1200,
			WalletUSD:    1100,
			Time:         at,
		},
		price: exchange.MarketPrice{
			Symbol:     "BTCUSDT",
			LastPrice:  50000,
			MarkPrice:  50001,
			IndexPrice: 49999,
			Time:       at,
		},
		leverage: exchange.LeverageInfo{
			Symbol:   "BTCUSDT",
			Leverage: 10,
			Time:     at,
		},
		rules: exchange.InstrumentRules{
			Symbol:       "BTCUSDT",
			Status:       "Trading",
			MinLeverage:  1,
			MaxLeverage:  100,
			LeverageStep: 0.01,
		},
		errs: make(map[string]error),
	}
}

func (s *executionReadinessSourceStub) GetAccountInfo(context.Context) (exchange.AccountInfo, error) {
	s.calls = append(s.calls, "account")
	return s.account, s.errs["account"]
}

func (s *executionReadinessSourceStub) GetPosition(context.Context, string) (*exchange.Position, error) {
	s.calls = append(s.calls, "position")
	return s.position, s.errs["position"]
}

func (s *executionReadinessSourceStub) GetAccountBalance(context.Context) (exchange.AccountBalance, error) {
	s.calls = append(s.calls, "balance")
	return s.balance, s.errs["balance"]
}

func (s *executionReadinessSourceStub) GetLastPrice(context.Context, string) (exchange.MarketPrice, error) {
	s.calls = append(s.calls, "price")
	return s.price, s.errs["price"]
}

func (s *executionReadinessSourceStub) GetLeverage(context.Context, string) (exchange.LeverageInfo, error) {
	s.calls = append(s.calls, "leverage")
	return s.leverage, s.errs["leverage"]
}

func (s *executionReadinessSourceStub) GetInstrumentRules(context.Context, string) (exchange.InstrumentRules, error) {
	s.calls = append(s.calls, "rules")
	return s.rules, s.errs["rules"]
}
