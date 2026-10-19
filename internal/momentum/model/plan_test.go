package model

import (
	"testing"
	"time"
)

// validPlanInput возвращает вход первой ревизии с ценой BTC.
func validPlanInput(t *testing.T, snapshot PortfolioSnapshot) PlanInput {
	t.Helper()
	quantity, err := testDecimal(t, "1").Quo(testDecimal(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	return PlanInput{Snapshot: snapshot, Metadata: PlanMetadata{
		StrategyID: "strategy", RunID: "run", ProfileID: "profile", ModelID: "model", FormulaID: "formula",
		ConfigHash:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ScheduledAt:    time.Date(2026, 1, 2, 4, 0, 0, 0, time.FixedZone("schedule", 3600)),
		EquityCutoffAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.FixedZone("cutoff", -18000)), Revision: 1,
	}, Targets: []Target{{Symbol: "BTC", Quantity: quantity}}}
}

// TestTradePlanImmutableCanonical проверяет копии, точность, порядок и ревизию.
func TestTradePlanImmutableCanonical(t *testing.T) {
	snapshotInput := validSnapshotInput(t)
	snapshotInput.Prices["ETH"] = testDecimal(t, "2")
	snapshot, err := NewPortfolioSnapshot(snapshotInput)
	if err != nil {
		t.Fatal(err)
	}
	in := validPlanInput(t, snapshot)
	in.Targets = append(in.Targets, Target{Symbol: "ETH", Quantity: in.Targets[0].Quantity})
	p, err := NewTradePlan(in)
	if err != nil {
		t.Fatal(err)
	}
	hash := p.Hash()
	in.Targets[0].Quantity = testDecimal(t, "9")
	returned := p.Targets()
	returned[0].Quantity = testDecimal(t, "8")
	meta := p.Metadata()
	meta.StrategyID = "changed"
	if p.Hash() != hash || p.Metadata().StrategyID != "strategy" || len(p.Targets()) != 2 {
		t.Fatal("plan input or returned values changed the plan")
	}
	n, d, _ := p.Targets()[0].Quantity.CanonicalRatio()
	if n != "1" || d != "3" {
		t.Fatalf("target lost exact 1/3: %s/%s", n, d)
	}
	if p.Metadata().ScheduledAt.Location() != time.UTC || p.Metadata().EquityCutoffAt.Location() != time.UTC {
		t.Fatal("plan times were not normalized to UTC")
	}
	reordered := validPlanInput(t, snapshot)
	reordered.Targets = []Target{{Symbol: "ETH", Quantity: p.Targets()[1].Quantity}, p.Targets()[0]}
	if reorderedPlan, err := NewTradePlan(reordered); err != nil || reorderedPlan.Hash() != hash {
		t.Fatal("target order changed the plan hash")
	}
	next := validPlanInput(t, snapshot)
	next.Metadata.Revision, next.Metadata.PreviousHash = 2, hash
	nextPlan, err := NewTradePlan(next)
	if err != nil || nextPlan.Hash() == hash || p.Hash() != hash || p.Metadata().Revision != 1 {
		t.Fatal("new revision changed its predecessor")
	}
}

// TestTradePlanHashTracksFields проверяет hash целей, snapshot и метаданных.
func TestTradePlanHashTracksFields(t *testing.T) {
	baseSnapshot := validSnapshotInput(t)
	baseSnapshot.Prices["ETH"] = testDecimal(t, "2")
	snapshot, err := NewPortfolioSnapshot(baseSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	base := validPlanInput(t, snapshot)
	original, err := NewTradePlan(base)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*PlanInput){
		"target":   func(in *PlanInput) { in.Targets[0].Quantity = testDecimal(t, "2") },
		"strategy": func(in *PlanInput) { in.Metadata.StrategyID = "other" },
		"config": func(in *PlanInput) {
			in.Metadata.ConfigHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"revision": func(in *PlanInput) { in.Metadata.Revision = 2; in.Metadata.PreviousHash = original.Hash() },
		"snapshot price": func(in *PlanInput) {
			changed := baseSnapshot
			changed.Prices["BTC"] = testDecimal(t, "2")
			in.Snapshot, err = NewPortfolioSnapshot(changed)
			if err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := base
			in.Targets = append([]Target(nil), base.Targets...)
			change(&in)
			got, err := NewTradePlan(in)
			if err != nil || got.Hash() == original.Hash() {
				t.Fatal("changed plan field did not change its hash")
			}
		})
	}
}

// TestTradePlanRejectsInvalid проверяет обязательные поля, snapshot и targets.
func TestTradePlanRejectsInvalid(t *testing.T) {
	snapshot, err := NewPortfolioSnapshot(validSnapshotInput(t))
	if err != nil {
		t.Fatal(err)
	}
	base := validPlanInput(t, snapshot)
	for name, change := range map[string]func(*PlanInput){
		"strategy":              func(in *PlanInput) { in.Metadata.StrategyID = " " },
		"run":                   func(in *PlanInput) { in.Metadata.RunID = "  " },
		"profile":               func(in *PlanInput) { in.Metadata.ProfileID = "  " },
		"model":                 func(in *PlanInput) { in.Metadata.ModelID = "  " },
		"formula":               func(in *PlanInput) { in.Metadata.FormulaID = "  " },
		"config hash":           func(in *PlanInput) { in.Metadata.ConfigHash = "ABC" },
		"zero scheduled":        func(in *PlanInput) { in.Metadata.ScheduledAt = time.Time{} },
		"zero cutoff":           func(in *PlanInput) { in.Metadata.EquityCutoffAt = time.Time{} },
		"zero revision":         func(in *PlanInput) { in.Metadata.Revision = 0 },
		"first link":            func(in *PlanInput) { in.Metadata.PreviousHash = "a" },
		"missing revision link": func(in *PlanInput) { in.Metadata.Revision = 2 },
		"invalid revision link": func(in *PlanInput) {
			in.Metadata.Revision = 2
			in.Metadata.PreviousHash = "A" + in.Metadata.ConfigHash[1:]
		},
		"zero plan":         func(in *PlanInput) { *in = PlanInput{} },
		"invalid quantity":  func(in *PlanInput) { in.Targets[0].Quantity = Decimal{} },
		"negative quantity": func(in *PlanInput) { in.Targets[0].Quantity = testDecimal(t, "-1") },
		"empty symbol":      func(in *PlanInput) { in.Targets[0].Symbol = " " },
		"duplicate symbol":  func(in *PlanInput) { in.Targets = append(in.Targets, in.Targets[0]) },
		"missing price":     func(in *PlanInput) { in.Targets[0].Symbol = "ETH" },
		"zero snapshot":     func(in *PlanInput) { in.Snapshot = PortfolioSnapshot{} },
		"snapshot hash mismatch": func(in *PlanInput) {
			in.Snapshot.hash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := base
			in.Targets = append([]Target(nil), base.Targets...)
			change(&in)
			if _, err := NewTradePlan(in); err == nil {
				t.Fatal("invalid plan input accepted")
			}
		})
	}
}
