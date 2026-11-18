package model

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"math"
	"strings"
	"testing"
	"time"
)

// TestTradePlanCodecRoundTrip проверяет полное значение, точность и независимые копии.
func TestTradePlanCodecRoundTrip(t *testing.T) {
	in := validSnapshotInput(t)
	in.Prices["ETH"] = testDecimal(t, "95367431640625.000000000000000001")
	in.Positions["ETH"] = testDecimal(t, "-0.000000000000000000001")
	var err error
	in.Equity, err = testDecimal(t, "-2").Quo(testDecimal(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	in.CapturedAt = time.Date(10000, 1, 1, 5, 0, 0, 123456789, time.FixedZone("offset", 3600))
	s, err := NewPortfolioSnapshot(in)
	if err != nil {
		t.Fatal(err)
	}
	input := validPlanInput(t, s)
	third := input.Targets[0].Quantity
	input.Targets = []Target{{Symbol: "ETH", Quantity: Zero()}, {Symbol: "BTC", Quantity: third}}
	input.Metadata.ProfileID = "opaque\x00\xff"
	input.Metadata.Revision = math.MaxUint64
	input.Metadata.PreviousHash = strings.Repeat("b", 64)
	input.Metadata.EquityCutoffAt = input.Metadata.EquityCutoffAt.Add(time.Nanosecond)
	p, err := NewTradePlan(input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeTradePlan(p)
	if err != nil {
		t.Fatal(err)
	}
	saved := bytes.Clone(encoded)
	got, err := DecodeTradePlan(encoded)
	if err != nil || got.Hash() != p.Hash() || got.Metadata() != p.Metadata() ||
		got.Snapshot().Hash() != s.Hash() || got.Snapshot().CapturedAt() != in.CapturedAt.UTC() ||
		!equalPlanCodecDecimal(got.Snapshot().Equity(), in.Equity) ||
		!maps.EqualFunc(got.Snapshot().Prices(), in.Prices, equalPlanCodecDecimal) ||
		!maps.EqualFunc(got.Snapshot().Positions(), in.Positions, equalPlanCodecDecimal) {
		t.Fatalf("complete plan did not round-trip: %v", err)
	}
	targets := got.Targets()
	if len(targets) != 2 || targets[0].Symbol != "BTC" || !equalPlanCodecDecimal(targets[0].Quantity, third) ||
		targets[1].Symbol != "ETH" || !equalPlanCodecDecimal(targets[1].Quantity, Zero()) {
		t.Fatal("targets changed after decode")
	}
	encoded[0] = 0
	input.Targets[0], input.Targets[1] = input.Targets[1], input.Targets[0]
	reordered, err := NewTradePlan(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, immutable := range []TradePlan{got, reordered} {
		again, err := EncodeTradePlan(immutable)
		if err != nil || !bytes.Equal(again, saved) {
			t.Fatalf("canonical bytes changed: %v", err)
		}
	}
}

// TestTradePlanCodecInvalid отвергает повреждённые и неканонические представления.
func TestTradePlanCodecInvalid(t *testing.T) {
	s, err := NewPortfolioSnapshot(validSnapshotInput(t))
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewTradePlan(validPlanInput(t, s))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeTradePlan(p)
	if err != nil {
		t.Fatal(err)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(raw)); digest != "04a5555ce152d923ba685970719dbe710cdb60e79e80fb74940c7de9efd98426" {
		t.Errorf("v1 fixture SHA-256 = %s", digest)
	}
	if encoded, err := EncodeTradePlan(TradePlan{}); encoded != nil || !errors.Is(err, errModel) {
		t.Fatalf("zero encode: %q, %v", encoded, err)
	}
	for i := range raw {
		if decoded, err := DecodeTradePlan(raw[:i]); decoded.hash != "" || !errors.Is(err, errModel) {
			t.Fatalf("accepted truncated framing at %d", i)
		}
	}

	captured, err := s.CapturedAt().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	invalidNanos := bytes.Clone(captured)
	copy(invalidNanos[9:13], []byte{0x3b, 0x9a, 0xca, 0x00})
	invalidOffset := bytes.Clone(captured)
	invalidOffset[14] = 0xfe
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"trailing", append(bytes.Clone(raw), 'x')},
		{"version", bytes.Replace(raw, []byte(tradePlanEncodingV1), []byte("momentum/trade-plan-payload/v2"), 1)},
		{"noncanonical-length", append([]byte("0"), raw...)},
		{"huge-length", append([]byte("999999999999999999999999999999:"), raw...)},
		{"mismatched-hash", bytes.Replace(raw, []byte(p.Hash()), []byte(strings.Repeat("b", 64)), 1)},
		{"different-valid-value", bytes.Replace(raw, []byte("7:profile"), []byte("7:altered"), 1)},
		{"noncanonical-ratio", bytes.Replace(raw, []byte("3:1/3"), []byte("3:2/6"), 1)},
		{"zero-denominator", bytes.Replace(raw, []byte("3:1/3"), []byte("3:1/0"), 1)},
		{"oversized-numerator", bytes.Replace(raw, []byte("3:1/3"), []byte("1237:"+strings.Repeat("9", 1235)+"/1"), 1)},
		{"invalid-utc-nanos", bytes.Replace(raw, captured, invalidNanos, 1)},
		{"invalid-utc-offset", bytes.Replace(raw, captured, invalidOffset, 1)},
		{"duplicate-map-key", bytes.Replace(raw, []byte("1:13:BTC3:1/1"), []byte("1:23:BTC3:1/13:BTC3:1/1"), 1)},
		{"bad-target-count", bytes.Replace(raw, []byte("0:1:13:BTC3:1/3"), []byte("0:2:993:BTC3:1/3"), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if bytes.Equal(tc.raw, raw) {
				t.Fatal("fixture was not mutated")
			}
			if decoded, err := DecodeTradePlan(tc.raw); decoded.hash != "" || !errors.Is(err, errModel) {
				t.Fatalf("accepted malformed plan: %v", err)
			}
		})
	}
}

// equalPlanCodecDecimal сравнивает точное значение дроби, а не внутреннее представление.
func equalPlanCodecDecimal(a, b Decimal) bool {
	cmp, err := a.Cmp(b)
	return err == nil && cmp == 0
}
