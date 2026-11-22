package execution

import (
	"errors"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// fill строит подтверждённый fill BUY BTCUSDT.
func fill(t *testing.T, id, qty string) Evidence {
	return Evidence{Kind: EvidenceFill, OrderID: "o1", Fill: model.ConfirmedFill{ExecutionID: id, Symbol: "BTCUSDT", Side: model.FillBuy,
		ExecutedAt: time.Unix(1700000000, 0).UTC(), Quantity: dec(t, qty), ExecutionPrice: dec(t, "90000"), FeeUSDT: dec(t, "0.1")}}
}

// status строит статус биржи с cumExecQty.
func status(t *testing.T, s, cum string) Evidence {
	return Evidence{Kind: EvidenceStatus, OrderID: "o1", Status: s, CumExecQty: dec(t, cum)}
}

// apply применяет цепочку свидетельств и останавливает тест при ошибке.
func apply(t *testing.T, l Lifecycle, evidence ...Evidence) Lifecycle {
	t.Helper()
	for i, e := range evidence {
		next, _, err := l.Apply(e)
		if err != nil {
			t.Fatalf("evidence %d %s: %v", i, e.Kind, err)
		}
		l = next
	}
	return l
}

// newOrder создаёт заявку на 0.01.
func newOrder(t *testing.T) Lifecycle {
	l, err := NewLifecycle("link", dec(t, "0.01"))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestLifecycleHappyPath проверяет переходы до FILLED только по fills.
func TestLifecycleHappyPath(t *testing.T) {
	l := apply(t, newOrder(t), Evidence{Kind: EvidenceSent}, Evidence{Kind: EvidenceAccepted, OrderID: "o1"})
	if l.State() != StateAcknowledged || l.OrderID() != "o1" {
		t.Fatalf("state=%s", l.State())
	}
	l = apply(t, l, status(t, "Filled", "0.01"))
	if l.State() == StateFilled || !l.AwaitingFills() || l.Settled() {
		t.Fatal("exchange Filled without confirmed fills must not be terminal")
	}
	l = apply(t, l, fill(t, "e1", "0.004"))
	if l.State() != StatePartiallyFilled {
		t.Fatalf("partial=%s", l.State())
	}
	next, changed, err := l.Apply(fill(t, "e1", "0.004"))
	if err != nil || changed || next.State() != StatePartiallyFilled {
		t.Fatal("duplicate fill must be a no-op")
	}
	l = apply(t, l, fill(t, "e2", "0.006"))
	if l.State() != StateFilled || !l.Settled() {
		t.Fatalf("filled=%s settled=%v", l.State(), l.Settled())
	}
}

// TestLifecycleCancelAndUnknown проверяет cancel после partial, timeout и отсутствие в endpoint.
func TestLifecycleCancelAndUnknown(t *testing.T) {
	l := apply(t, newOrder(t), Evidence{Kind: EvidenceSent}, Evidence{Kind: EvidenceTimeout})
	if l.State() != StateUnknown {
		t.Fatalf("timeout=%s", l.State())
	}
	if next, changed, _ := l.Apply(Evidence{Kind: EvidenceNotFound}); changed || next.State() != StateUnknown {
		t.Fatal("absence in one endpoint must not change state")
	}
	l = apply(t, l, status(t, "PartiallyFilledCanceled", "0.004"))
	if l.State() != StateCancelled || !l.AwaitingFills() || l.Settled() {
		t.Fatalf("cancel with unconfirmed executed qty: %s awaiting=%v", l.State(), l.AwaitingFills())
	}
	l = apply(t, l, fill(t, "e1", "0.004"), Evidence{Kind: EvidenceTimeout})
	f, _ := l.Filled().WireDecimal()
	if l.State() != StateCancelled || f != "0.004" || !l.Settled() {
		t.Fatalf("cancel after partial lost fills: %s %s", l.State(), f)
	}
	l = apply(t, l, status(t, "New", "0"))
	if l.State() != StateCancelled {
		t.Fatal("terminal state must not be reverted by a stale status")
	}
}

// TestLifecycleConflicts проверяет отказ на противоречивых свидетельствах.
func TestLifecycleConflicts(t *testing.T) {
	base := apply(t, newOrder(t), Evidence{Kind: EvidenceAccepted, OrderID: "o1"}, fill(t, "e1", "0.004"))
	changed := fill(t, "e1", "0.004")
	changed.Fill.ExecutionPrice = dec(t, "1")
	for name, e := range map[string]Evidence{
		"other-order":     {Kind: EvidenceAccepted, OrderID: "o2"},
		"fill-other":      {Kind: EvidenceFill, OrderID: "o2", Fill: fill(t, "e9", "0.006").Fill},
		"fill-no-order":   {Kind: EvidenceFill, Fill: fill(t, "e9", "0.006").Fill},
		"changed-fill":    changed,
		"overfill":        fill(t, "e2", "0.007"),
		"reject-filled":   status(t, "Rejected", "0.004"),
		"filled-mismatch": status(t, "Filled", "0.004"),
		"over-cum":        status(t, "PartiallyFilled", "0.02"),
		"unknown-status":  status(t, "Weird", "0"),
		"unknown-kind":    {Kind: "X"},
	} {
		next, ch, err := base.Apply(e)
		if err == nil || ch || next.State() != base.State() {
			t.Fatalf("%s: want conflict, got %v", name, err)
		}
		if name != "unknown-status" && name != "unknown-kind" && !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	rejected := apply(t, newOrder(t), Evidence{Kind: EvidenceSent}, status(t, "Rejected", "0"))
	if rejected.State() != StateRejected || !rejected.Settled() {
		t.Fatalf("rejected=%s", rejected.State())
	}
	if _, _, err := rejected.Apply(fill(t, "late", "0.001")); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatal("fill after reject must conflict")
	}
	if _, err := NewLifecycle("", dec(t, "1")); err == nil {
		t.Fatal("empty link must be rejected")
	}
}

// TestLifecycleFillBindsOrder проверяет, что fill до подтверждения привязывает заявку.
func TestLifecycleFillBindsOrder(t *testing.T) {
	l := apply(t, newOrder(t), Evidence{Kind: EvidenceSent}, fill(t, "e1", "0.004"))
	if l.OrderID() != "o1" {
		t.Fatalf("fill must bind order id, got %q", l.OrderID())
	}
	if _, _, err := l.Apply(Evidence{Kind: EvidenceAccepted, OrderID: "o2"}); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("ack for another order after fill: %v", err)
	}
}
