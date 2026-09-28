package execution

import "testing"

// TestEvaluateTradingGate проверяет запреты, границы и приоритет gate.
func TestEvaluateTradingGate(t *testing.T) {
	cases := []struct {
		name  string
		cfg   GateConfig
		state GateState
		want  GateDecision
	}{
		{"zero values", GateConfig{}, GateState{}, GateDeniedReadiness},
		{"dry-run", GateConfig{DryRun: true, MaxTradesPerDay: 2}, GateState{ReadinessConfirmed: true}, GateDeniedDryRun},
		{"emergency stop", GateConfig{EmergencyStop: true, MaxTradesPerDay: 2}, GateState{ReadinessConfirmed: true}, GateDeniedEmergencyStop},
		{"readiness missing", GateConfig{MaxTradesPerDay: 2}, GateState{}, GateDeniedReadiness},
		{"zero limit", GateConfig{MaxTradesPerDay: 0}, GateState{ReadinessConfirmed: true}, GateDeniedInvalidLimit},
		{"negative limit", GateConfig{MaxTradesPerDay: -1}, GateState{ReadinessConfirmed: true}, GateDeniedInvalidLimit},
		{"negative count", GateConfig{MaxTradesPerDay: 2}, GateState{ReadinessConfirmed: true, TradesToday: -1}, GateDeniedInvalidCount},
		{"count equals limit", GateConfig{MaxTradesPerDay: 2}, GateState{ReadinessConfirmed: true, TradesToday: 2}, GateDeniedDailyLimit},
		{"count exceeds limit", GateConfig{MaxTradesPerDay: 2}, GateState{ReadinessConfirmed: true, TradesToday: 3}, GateDeniedDailyLimit},
		{"count below limit", GateConfig{MaxTradesPerDay: 2}, GateState{ReadinessConfirmed: true, TradesToday: 1}, GateNeedsHumanConfirmation},
		{"dry-run precedes every blocker", GateConfig{DryRun: true, EmergencyStop: true}, GateState{TradesToday: -1}, GateDeniedDryRun},
		{"emergency precedes readiness", GateConfig{EmergencyStop: true}, GateState{}, GateDeniedEmergencyStop},
		{"readiness precedes invalid limit", GateConfig{}, GateState{}, GateDeniedReadiness},
	}
	for _, tc := range cases {
		if got := EvaluateTradingGate(tc.cfg, tc.state); got != tc.want {
			t.Errorf("%s: EvaluateTradingGate() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
