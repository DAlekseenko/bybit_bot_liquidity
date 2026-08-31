package execution

import (
	"errors"
	"testing"
)

func TestTradingGateFailsClosed(t *testing.T) {
	t.Parallel()
	if err := CheckTradingGate(GateConfig{}, GateState{}); !errors.Is(err, ErrReadinessRequired) {
		t.Fatalf("error = %v", err)
	}
	if err := CheckTradingGate(GateConfig{DryRun: true, MaxTradesPerDay: 1}, GateState{ReadinessConfirmed: true}); !errors.Is(err, ErrDryRunEnabled) {
		t.Fatalf("error = %v", err)
	}
	if err := CheckTradingGate(GateConfig{MaxTradesPerDay: 1}, GateState{ReadinessConfirmed: true}); err != nil {
		t.Fatal(err)
	}
}
