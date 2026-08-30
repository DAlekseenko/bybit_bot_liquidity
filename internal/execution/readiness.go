package execution

import (
	"context"
	"errors"
	"fmt"
	"math"

	"bybit_bot_liquidity/internal/exchange"
)

var (
	ErrExecutionReadinessSourceRequired = errors.New("execution readiness source is required")
	ErrExecutionSymbolRequired          = errors.New("execution symbol is required")
	ErrInvalidAccountBalance            = errors.New("invalid account balance")
	ErrNoAvailableBalance               = errors.New("no available balance")
	ErrInvalidMarketPrice               = errors.New("invalid market price")
)

// ExecutionReadinessSource предоставляет read-only входы будущего executor.
type ExecutionReadinessSource interface {
	AccountInfoSource
	LeverageSource
	GetAccountBalance(ctx context.Context) (exchange.AccountBalance, error)
	GetLastPrice(ctx context.Context, symbol string) (exchange.MarketPrice, error)
	GetPosition(ctx context.Context, symbol string) (*exchange.Position, error)
}

// ExecutionReadiness содержит проверенные входы будущего торгового пути.
type ExecutionReadiness struct {
	Symbol   string
	Account  exchange.AccountInfo
	Balance  exchange.AccountBalance
	Price    exchange.MarketPrice
	Leverage exchange.LeverageInfo
}

// ExecutionReadinessPreflight собирает read-only входы без отправки заявок.
type ExecutionReadinessPreflight struct {
	source ExecutionReadinessSource
}

// NewExecutionReadinessPreflight создаёт агрегированную проверку готовности.
func NewExecutionReadinessPreflight(source ExecutionReadinessSource) *ExecutionReadinessPreflight {
	return &ExecutionReadinessPreflight{source: source}
}

// Check проверяет account, позицию, баланс, цену и текущее leverage.
func (p *ExecutionReadinessPreflight) Check(ctx context.Context, symbol string) (ExecutionReadiness, error) {
	if p == nil || p.source == nil {
		return ExecutionReadiness{}, ErrExecutionReadinessSourceRequired
	}
	if symbol == "" {
		return ExecutionReadiness{}, ErrExecutionSymbolRequired
	}

	account, err := NewAccountPreflight(p.source).Check(ctx)
	if err != nil {
		return ExecutionReadiness{}, fmt.Errorf("execution readiness account: %w", err)
	}

	position, err := p.source.GetPosition(ctx, symbol)
	if err != nil {
		return ExecutionReadiness{}, fmt.Errorf("execution readiness position: %w", err)
	}
	if position != nil {
		return ExecutionReadiness{}, ErrPositionAlreadyOpen
	}

	balance, err := p.source.GetAccountBalance(ctx)
	if err != nil {
		return ExecutionReadiness{}, fmt.Errorf("execution readiness balance: %w", err)
	}
	if err := validateReadinessBalance(balance); err != nil {
		return ExecutionReadiness{}, fmt.Errorf("execution readiness balance: %w", err)
	}

	price, err := p.source.GetLastPrice(ctx, symbol)
	if err != nil {
		return ExecutionReadiness{}, fmt.Errorf("execution readiness price: %w", err)
	}
	if err := validateReadinessPrice(price, symbol); err != nil {
		return ExecutionReadiness{}, fmt.Errorf("execution readiness price: %w", err)
	}

	leverage, err := NewLeveragePreflight(p.source).Check(ctx, symbol)
	if err != nil {
		return ExecutionReadiness{}, fmt.Errorf("execution readiness leverage: %w", err)
	}

	return ExecutionReadiness{
		Symbol:   symbol,
		Account:  account,
		Balance:  balance,
		Price:    price,
		Leverage: leverage,
	}, nil
}

func validateReadinessBalance(balance exchange.AccountBalance) error {
	if balance.Time.IsZero() ||
		!nonNegativeFinite(balance.AvailableUSD) ||
		!nonNegativeFinite(balance.EquityUSD) ||
		!nonNegativeFinite(balance.WalletUSD) {
		return ErrInvalidAccountBalance
	}
	if balance.AvailableUSD == 0 {
		return ErrNoAvailableBalance
	}
	return nil
}

func validateReadinessPrice(price exchange.MarketPrice, symbol string) error {
	if price.Symbol != symbol || price.Time.IsZero() ||
		!positiveFinite(price.LastPrice) ||
		!positiveFinite(price.MarkPrice) ||
		!positiveFinite(price.IndexPrice) {
		return ErrInvalidMarketPrice
	}
	return nil
}

func nonNegativeFinite(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
