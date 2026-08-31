package execution

import (
	"errors"
	"time"
)

var (
	ErrDryRunEnabled      = errors.New("trading gate: dry run is enabled")
	ErrReadinessRequired  = errors.New("trading gate: execution readiness is required")
	ErrEmergencyStop      = errors.New("trading gate: emergency stop is enabled")
	ErrDailyLimitExceeded = errors.New("trading gate: daily limit exceeded")
)

// GateConfig contains only explicit authorizations for an order flow. Its zero
// value denies execution.
type GateConfig struct {
	DryRun          bool
	EmergencyStop   bool
	MaxTradesPerDay int
}

// GateState is supplied by the orchestration layer after read-only checks.
type GateState struct {
	ReadinessConfirmed bool
	TradesToday        int
	AsOf               time.Time
}

// Check allows an order only when every guard is explicitly satisfied.
func CheckTradingGate(cfg GateConfig, state GateState) error {
	if cfg.DryRun {
		return ErrDryRunEnabled
	}
	if cfg.EmergencyStop {
		return ErrEmergencyStop
	}
	if !state.ReadinessConfirmed {
		return ErrReadinessRequired
	}
	if cfg.MaxTradesPerDay <= 0 || state.TradesToday >= cfg.MaxTradesPerDay {
		return ErrDailyLimitExceeded
	}
	return nil
}
