package execution

// GateConfig содержит аналитические ограничения торгового gate.
type GateConfig struct {
	DryRun          bool
	EmergencyStop   bool
	MaxTradesPerDay int
}

// GateState содержит входы gate, подтверждённые внешним read-only процессом.
type GateState struct {
	ReadinessConfirmed bool
	TradesToday        int
}

// GateDecision обозначает результат аналитической проверки gate.
type GateDecision string

// GateDeniedDryRun означает, что включён безопасный dry-run режим.
const GateDeniedDryRun GateDecision = "denied_dry_run"

// GateDeniedEmergencyStop означает, что активирован аварийный стоп.
const GateDeniedEmergencyStop GateDecision = "denied_emergency_stop"

// GateDeniedReadiness означает, что готовность не подтверждена.
const GateDeniedReadiness GateDecision = "denied_readiness"

// GateDeniedInvalidLimit означает, что дневной лимит задан некорректно.
const GateDeniedInvalidLimit GateDecision = "denied_invalid_limit"

// GateDeniedInvalidCount означает, что счётчик сделок отрицательный.
const GateDeniedInvalidCount GateDecision = "denied_invalid_count"

// GateDeniedDailyLimit означает, что дневной лимит исчерпан.
const GateDeniedDailyLimit GateDecision = "denied_daily_limit"

// GateNeedsHumanConfirmation означает, что дальше требуется отдельное подтверждение человека.
const GateNeedsHumanConfirmation GateDecision = "needs_human_confirmation"

// EvaluateTradingGate оценивает входы без I/O; итог не даёт полномочий на исполнение.
func EvaluateTradingGate(cfg GateConfig, state GateState) GateDecision {
	switch {
	case cfg.DryRun:
		return GateDeniedDryRun
	case cfg.EmergencyStop:
		return GateDeniedEmergencyStop
	case !state.ReadinessConfirmed:
		return GateDeniedReadiness
	case cfg.MaxTradesPerDay <= 0:
		return GateDeniedInvalidLimit
	case state.TradesToday < 0:
		return GateDeniedInvalidCount
	case state.TradesToday >= cfg.MaxTradesPerDay:
		return GateDeniedDailyLimit
	default:
		return GateNeedsHumanConfirmation
	}
}
