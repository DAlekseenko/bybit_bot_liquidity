package model

import (
	"maps"
	"testing"
	"time"
)

// testDecimal разбирает точное значение для тестов.
func testDecimal(t *testing.T, value string) Decimal {
	t.Helper()
	d, err := ParseAmount(value)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// validSnapshotInput возвращает минимальный корректный вход.
func validSnapshotInput(t *testing.T) SnapshotInput {
	t.Helper()
	return SnapshotInput{Environment: "paper", AccountID: "account", ConfigVersion: "v1", CapturedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600)), Equity: Zero(), Prices: map[string]Decimal{"BTC": testDecimal(t, "1")}, Positions: map[string]Decimal{"BTC": testDecimal(t, "-2")}}
}

// TestSnapshotImmutableCanonicalHash проверяет значения, копирование и канонический порядок.
func TestSnapshotImmutableCanonicalHash(t *testing.T) {
	in := validSnapshotInput(t)
	in.Prices["ETH"] = testDecimal(t, "2")
	in.Positions["ETH"] = testDecimal(t, "3")
	s, err := NewPortfolioSnapshot(in)
	if err != nil {
		t.Fatal(err)
	}
	hash := s.Hash()
	in.Prices["BTC"] = testDecimal(t, "4")
	delete(in.Positions, "BTC")
	prices, positions := s.Prices(), s.Positions()
	prices["BTC"], positions["BTC"] = testDecimal(t, "5"), testDecimal(t, "6")
	delete(prices, "ETH")
	priceNumerator, priceDenominator, _ := s.Prices()["BTC"].CanonicalRatio()
	if priceNumerator != "1" || priceDenominator != "1" || len(s.Prices()) != 2 {
		t.Fatal("input or returned price map changed snapshot")
	}
	positionNumerator, positionDenominator, _ := s.Positions()["BTC"].CanonicalRatio()
	if positionNumerator != "-2" || positionDenominator != "1" || len(s.Positions()) != 2 || s.Hash() != hash {
		t.Fatal("input or returned position map changed snapshot")
	}
	reordered := validSnapshotInput(t)
	reordered.Prices = map[string]Decimal{"ETH": testDecimal(t, "2"), "BTC": testDecimal(t, "1")}
	reordered.Positions = map[string]Decimal{"ETH": testDecimal(t, "3"), "BTC": testDecimal(t, "-2")}
	other, err := NewPortfolioSnapshot(reordered)
	if err != nil || other.Hash() != hash {
		t.Fatal("map insertion order changed snapshot hash")
	}
}

// TestSnapshotHashTracksIdentityAndValues проверяет значимые поля hash.
func TestSnapshotHashTracksIdentityAndValues(t *testing.T) {
	base := validSnapshotInput(t)
	snapshot, err := NewPortfolioSnapshot(base)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*SnapshotInput){
		"price":          func(in *SnapshotInput) { in.Prices["BTC"] = testDecimal(t, "2") },
		"position":       func(in *SnapshotInput) { in.Positions["BTC"] = testDecimal(t, "2") },
		"account":        func(in *SnapshotInput) { in.AccountID = "other" },
		"config version": func(in *SnapshotInput) { in.ConfigVersion = "v2" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.Prices, changed.Positions = maps.Clone(base.Prices), maps.Clone(base.Positions)
			change(&changed)
			got, err := NewPortfolioSnapshot(changed)
			if err != nil || got.Hash() == snapshot.Hash() {
				t.Fatal("changed snapshot field did not change hash")
			}
		})
	}
}

// TestSnapshotPreservesSignedRationals проверяет signed значения без округления.
func TestSnapshotPreservesSignedRationals(t *testing.T) {
	in := validSnapshotInput(t)
	in.Equity = testDecimal(t, "-7")
	position, err := testDecimal(t, "-1").Quo(testDecimal(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	in.Positions["BTC"] = position
	s, err := NewPortfolioSnapshot(in)
	if err != nil {
		t.Fatal(err)
	}
	if n, d, _ := s.Equity().CanonicalRatio(); n != "-7" || d != "1" {
		t.Fatal("signed equity changed")
	}
	if n, d, _ := s.Positions()["BTC"].CanonicalRatio(); n != "-1" || d != "3" {
		t.Fatal("signed rational position changed")
	}
	if s.CapturedAt().Location() != time.UTC || s.CapturedAt().Hour() != 2 {
		t.Fatal("captured time was not normalized to UTC")
	}
}

// TestSnapshotRejectsInvalidInput проверяет все обязательные поля и значения.
func TestSnapshotRejectsInvalidInput(t *testing.T) {
	for name, change := range map[string]func(*SnapshotInput){
		"empty environment":    func(in *SnapshotInput) { in.Environment = "" },
		"unknown environment":  func(in *SnapshotInput) { in.Environment = "unknown" },
		"live environment":     func(in *SnapshotInput) { in.Environment = "live" },
		"empty account":        func(in *SnapshotInput) { in.AccountID = "  " },
		"empty config version": func(in *SnapshotInput) { in.ConfigVersion = "" },
		"zero time":            func(in *SnapshotInput) { in.CapturedAt = time.Time{} },
		"invalid equity":       func(in *SnapshotInput) { in.Equity = Decimal{} },
		"invalid price":        func(in *SnapshotInput) { in.Prices["BTC"] = Decimal{} },
		"invalid position":     func(in *SnapshotInput) { in.Positions["BTC"] = Decimal{} },
		"zero price":           func(in *SnapshotInput) { in.Prices["BTC"] = Zero() },
		"negative price":       func(in *SnapshotInput) { in.Prices["BTC"] = testDecimal(t, "-1") },
		"empty symbol":         func(in *SnapshotInput) { in.Prices[" "] = testDecimal(t, "1") },
	} {
		t.Run(name, func(t *testing.T) {
			in := validSnapshotInput(t)
			change(&in)
			if _, err := NewPortfolioSnapshot(in); err == nil {
				t.Fatal("invalid snapshot input accepted")
			}
		})
	}
	if _, err := NewPortfolioSnapshot(SnapshotInput{}); err == nil {
		t.Fatal("zero snapshot input accepted")
	}
}
