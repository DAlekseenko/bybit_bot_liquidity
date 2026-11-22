package execution

import (
	"errors"
	"testing"
)

// openingRebalance строит opening-ревизию 2 с одной покупкой.
func openingRebalance(t *testing.T) *Rebalance {
	in := leg("o1", LegIncrease)
	in.PlanRevision = 2
	r, err := NewRebalance(IntentBatch{Intents: []OrderIntent{in}})
	if err != nil {
		t.Fatal(err)
	}
	return &r
}

// TestNextReset проверяет порядок close → flat → plan → opening и запреты opening.
func TestNextReset(t *testing.T) {
	closing, err := NewRebalance(IntentBatch{Intents: []OrderIntent{leg("c1", LegReduce), leg("c2", LegReduce)}})
	if err != nil {
		t.Fatal(err)
	}
	filled := lifecycle(t, Evidence{Kind: EvidenceAccepted, OrderID: "o1"}, fill(t, "e1", "0.01"))
	unknown := lifecycle(t, Evidence{Kind: EvidenceSent}, Evidence{Kind: EvidenceTimeout})
	partial := lifecycle(t, fill(t, "e1", "0.004"), status(t, "PartiallyFilledCanceled", "0.004"))
	done := map[string]Lifecycle{"c1": filled, "c2": filled}
	for _, tc := range []struct {
		name   string
		in     ResetInput
		phase  ResetPhase
		send   string
		reason BlockReason
	}{
		{"close-all", ResetInput{Close: closing}, ResetClosing, "c1;c2;", ""},
		{"unknown-close", ResetInput{Close: closing, CloseStates: map[string]Lifecycle{"c1": unknown, "c2": filled}, BrokerFlat: true}, ResetClosing, "", ""},
		{"partial-close", ResetInput{Close: closing, CloseStates: map[string]Lifecycle{"c1": partial, "c2": filled}, BrokerFlat: true}, ResetBlocked, "", BlockReductionIncomplete},
		{"await-flat", ResetInput{Close: closing, CloseStates: done}, ResetAwaitingFlat, "", ""},
		{"plan-opening", ResetInput{Close: closing, CloseStates: done, BrokerFlat: true}, ResetPlanOpening, "", ""},
		{"opening", ResetInput{Close: closing, CloseStates: done, BrokerFlat: true, Opening: openingRebalance(t)}, ResetOpening, "o1;", ""},
		{"opening-gate", ResetInput{Close: closing, CloseStates: done, BrokerFlat: true, Opening: openingRebalance(t),
			IncreaseGate: errors.New("margin")}, ResetBlocked, "", BlockIncreaseGate},
		{"done", ResetInput{Close: closing, CloseStates: done, BrokerFlat: true, Opening: openingRebalance(t),
			OpeningStates: map[string]Lifecycle{"o1": filled}}, ResetDone, "", ""},
	} {
		for restart := 0; restart < 2; restart++ {
			step, err := NextReset(tc.in)
			if err != nil || step.Phase != tc.phase || links(Step{Send: step.Send}) != tc.send || step.Reason != tc.reason {
				t.Fatalf("%s: %+v %v", tc.name, step, err)
			}
		}
	}
	dust, _ := NewRebalance(IntentBatch{Intents: []OrderIntent{leg("c1", LegReduce)}, Skipped: []SkippedDelta{{Symbol: "ETHUSDT", ReduceOnly: true}}})
	if step, _ := NextReset(ResetInput{Close: dust, CloseStates: map[string]Lifecycle{"c1": filled}, BrokerFlat: true}); step.Phase != ResetBlocked {
		t.Fatalf("dust close must block opening: %+v", step)
	}
	if _, err := NextReset(ResetInput{Close: *openingRebalance(t)}); !errors.Is(err, ErrRebalance) {
		t.Fatalf("close phase with buys must be rejected: %v", err)
	}
	same := closing
	if _, err := NextReset(ResetInput{Close: closing, CloseStates: done, BrokerFlat: true, Opening: &same}); !errors.Is(err, ErrRebalance) {
		t.Fatalf("opening must be a new revision: %v", err)
	}
}
