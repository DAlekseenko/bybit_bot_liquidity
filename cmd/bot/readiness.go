package main

import (
	"context"
	"errors"
	"log/slog"

	"bybit_bot_liquidity/internal/execution"
)

type executionReadinessChecker interface {
	// Check выполняет read-only проверку готовности аккаунта.
	Check(ctx context.Context, symbol string) (execution.ExecutionReadiness, error)
}

type readinessMetrics interface {
	// SetReadiness сохраняет ограниченное состояние проверки.
	SetReadiness(ready bool)
}

// reportExecutionReadiness проверяет готовность и сообщает безопасный результат.
func reportExecutionReadiness(
	ctx context.Context,
	log *slog.Logger,
	enabled bool,
	checker executionReadinessChecker,
	symbol string,
	metrics readinessMetrics,
) {
	if !enabled {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	if checker == nil {
		setExecutionReadiness(metrics, false)
		log.Warn(
			"execution readiness failed",
			"symbol", symbol,
			"reason", classifyExecutionReadinessError(execution.ErrExecutionReadinessSourceRequired),
		)
		return
	}

	if _, err := checker.Check(ctx, symbol); err != nil {
		setExecutionReadiness(metrics, false)
		log.Warn(
			"execution readiness failed",
			"symbol", symbol,
			"reason", classifyExecutionReadinessError(err),
		)
		return
	}
	setExecutionReadiness(metrics, true)
	log.Info("execution readiness confirmed", "symbol", symbol)
}

// setExecutionReadiness обновляет метрику при наличии получателя.
func setExecutionReadiness(metrics readinessMetrics, ready bool) {
	if metrics != nil {
		metrics.SetReadiness(ready)
	}
}

// classifyExecutionReadinessError сводит ошибки проверки к безопасным категориям.
func classifyExecutionReadinessError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, execution.ErrExecutionReadinessSourceRequired):
		return "source_required"
	case errors.Is(err, execution.ErrExecutionSymbolRequired):
		return "symbol_required"
	case errors.Is(err, execution.ErrInvalidAccountInfo):
		return "invalid_account"
	case errors.Is(err, execution.ErrUnsupportedAccountMode):
		return "unsupported_account_mode"
	case errors.Is(err, execution.ErrUnsupportedMarginMode):
		return "unsupported_margin_mode"
	case errors.Is(err, execution.ErrPositionAlreadyOpen):
		return "position_already_open"
	case errors.Is(err, execution.ErrInvalidAccountBalance):
		return "invalid_balance"
	case errors.Is(err, execution.ErrNoAvailableBalance):
		return "no_available_balance"
	case errors.Is(err, execution.ErrInvalidMarketPrice):
		return "invalid_market_price"
	case errors.Is(err, execution.ErrInvalidLeverageInfo):
		return "invalid_leverage"
	case errors.Is(err, execution.ErrInvalidInstrumentRules):
		return "invalid_instrument_rules"
	case errors.Is(err, execution.ErrInstrumentNotTrading):
		return "instrument_not_trading"
	case errors.Is(err, execution.ErrLeverageOutOfRange):
		return "leverage_out_of_range"
	case errors.Is(err, execution.ErrLeverageStepMismatch):
		return "leverage_step_mismatch"
	default:
		return "source_error"
	}
}

var _ executionReadinessChecker = (*execution.ExecutionReadinessPreflight)(nil)
