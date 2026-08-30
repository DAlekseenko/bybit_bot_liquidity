package execution

import (
	"context"
	"errors"
	"fmt"

	"bybit_bot_liquidity/internal/exchange"
)

var (
	ErrAccountInfoSourceRequired = errors.New("account info source is required")
	ErrInvalidAccountInfo        = errors.New("invalid account info")
	ErrUnsupportedAccountMode    = errors.New("unsupported account mode")
	ErrUnsupportedMarginMode     = errors.New("unsupported margin mode")
)

// AccountInfoSource ограничивает preflight единственным read-only запросом.
type AccountInfoSource interface {
	GetAccountInfo(ctx context.Context) (exchange.AccountInfo, error)
}

// AccountPreflight проверяет совместимость аккаунта с ограничениями MVP.
type AccountPreflight struct {
	source AccountInfoSource
}

func NewAccountPreflight(source AccountInfoSource) *AccountPreflight {
	return &AccountPreflight{source: source}
}

// Check читает account info и отклоняет неподдерживаемые режимы без мутаций.
func (p *AccountPreflight) Check(ctx context.Context) (exchange.AccountInfo, error) {
	if p == nil || p.source == nil {
		return exchange.AccountInfo{}, ErrAccountInfoSourceRequired
	}

	info, err := p.source.GetAccountInfo(ctx)
	if err != nil {
		return exchange.AccountInfo{}, fmt.Errorf("account preflight: %w", err)
	}
	if err := validateAccountInfo(info); err != nil {
		return exchange.AccountInfo{}, fmt.Errorf("account preflight: %w", err)
	}
	return info, nil
}

func validateAccountInfo(info exchange.AccountInfo) error {
	if info.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: updated time is missing", ErrInvalidAccountInfo)
	}
	switch info.UnifiedMarginStatus {
	case 3, 4, 5, 6:
	default:
		return fmt.Errorf("%w: unifiedMarginStatus=%d", ErrUnsupportedAccountMode, info.UnifiedMarginStatus)
	}
	if info.MarginMode != exchange.MarginModeRegular {
		return fmt.Errorf("%w: marginMode=%q", ErrUnsupportedMarginMode, info.MarginMode)
	}
	return nil
}
