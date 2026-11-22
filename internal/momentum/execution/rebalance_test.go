package execution

import (
	"errors"
	"testing"
)

// leg строит заявку ревизии 1 с указанной ролью.
func leg(link string, l Leg) OrderIntent {
	return OrderIntent{OrderLinkID: link, PlanHash: "h", PlanRevision: 1, Leg: l}
}

// lifecycle проводит заявку на 0.01 через свидетельства.
func lifecycle(t *testing.T, evidence ...Evidence) Lifecycle {
	return apply(t, newOrder(t), evidence...)
}

// links возвращает orderLinkId заявок шага.
func links(s Step) string {
	out := ""
	for _, in := range s.Send {
		out += in.OrderLinkID + ";"
	}
	return out
}

// TestRebalanceOrdering проверяет, что увеличения ждут подтверждённых сокращений.
func TestRebalanceOrdering(t *testing.T) {
	r, err := NewRebalance(IntentBatch{Intents: []OrderIntent{leg("r1", LegReduce), leg("r2", LegReduce), leg("i1", LegIncrease)}})
	if err != nil {
		t.Fatal(err)
	}
	filled := lifecycle(t, Evidence{Kind: EvidenceAccepted, OrderID: "o1"}, fill(t, "e1", "0.01"))
	unknown := lifecycle(t, Evidence{Kind: EvidenceSent}, Evidence{Kind: EvidenceTimeout})
	partial := lifecycle(t, fill(t, "e1", "0.004"), status(t, "PartiallyFilledCanceled", "0.004"))
	for _, tc := range []struct {
		name   string
		states map[string]Lifecycle
		gate   error
		phase  Phase
		send   string
		reason BlockReason
	}{
		{"start", nil, nil, PhaseReducing, "r1;r2;", ""},
		{"unknown-reduction", map[string]Lifecycle{"r1": unknown}, nil, PhaseReducing, "r2;", ""},
		{"unknown-blocks-buys", map[string]Lifecycle{"r1": unknown, "r2": filled}, nil, PhaseReducing, "", ""},
		{"partial-reduction", map[string]Lifecycle{"r1": partial, "r2": filled}, nil, PhaseBlocked, "", BlockReductionIncomplete},
		{"increase", map[string]Lifecycle{"r1": filled, "r2": filled}, nil, PhaseIncreasing, "i1;", ""},
		{"margin-shortage", map[string]Lifecycle{"r1": filled, "r2": filled}, errors.New("insufficient margin"), PhaseBlocked, "", BlockIncreaseGate},
		{"increase-in-flight", map[string]Lifecycle{"r1": filled, "r2": filled, "i1": unknown}, nil, PhaseIncreasing, "", ""},
		{"increase-partial", map[string]Lifecycle{"r1": filled, "r2": filled, "i1": partial}, nil, PhaseBlocked, "", BlockIncreaseIncomplete},
		{"done", map[string]Lifecycle{"r1": filled, "r2": filled, "i1": filled}, nil, PhaseDone, "", ""},
	} {
		for restart := 0; restart < 2; restart++ { // тот же durable вход после рестарта даёт тот же шаг
			step, err := r.Next(tc.states, tc.gate)
			if err != nil || step.Phase != tc.phase || links(step) != tc.send || step.Reason != tc.reason {
				t.Fatalf("%s: %+v %v", tc.name, step, err)
			}
		}
	}
	if _, err := r.Next(map[string]Lifecycle{"r1": {}, "r2": filled}, nil); !errors.Is(err, ErrRebalance) {
		t.Fatalf("zero lifecycle must not release increases: %v", err)
	}
	if (Lifecycle{}).State() != StateUnknown || (Lifecycle{}).Settled() {
		t.Fatal("zero lifecycle must be unknown and unsettled")
	}
	if _, err := r.Next(map[string]Lifecycle{"foreign": filled}, nil); !errors.Is(err, ErrRebalance) {
		t.Fatalf("foreign order must be rejected: %v", err)
	}
}

// TestNewRebalance проверяет одну ревизию, порядок ролей и пустой план без заявок.
func TestNewRebalance(t *testing.T) {
	other := leg("x", LegIncrease)
	other.PlanRevision = 2
	for name, batch := range map[string][]OrderIntent{
		"mixed-revision":   {leg("a", LegReduce), other},
		"reduce-after-buy": {leg("a", LegIncrease), leg("b", LegReduce)},
		"unknown-leg":      {leg("a", "")},
		"missing-link":     {leg("", LegReduce)},
	} {
		if _, err := NewRebalance(IntentBatch{Intents: batch}); !errors.Is(err, ErrRebalance) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	r, err := NewRebalance(IntentBatch{})
	if step, nextErr := r.Next(nil, nil); err != nil || nextErr != nil || step.Phase != PhaseDone || len(step.Send) != 0 {
		t.Fatalf("unchanged quantities must produce no orders: %+v %v %v", step, err, nextErr)
	}
}
