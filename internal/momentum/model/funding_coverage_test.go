package model

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"
)

// TestCompareFundingCoverage проверяет точное сравнение и владение результатом.
func TestCompareFundingCoverage(t *testing.T) {
	epoch := time.UnixMilli(0).UTC()
	east := time.FixedZone("east", 7*3600)
	west := time.FixedZone("west", -3*3600)
	// at задаёт час относительно epoch.
	at := func(hours time.Duration) time.Time { return epoch.Add(hours * time.Hour) }
	zero, err := ParseRate("0")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := ParseRate("-0.00031")
	if err != nil {
		t.Fatal(err)
	}
	expected := []time.Time{at(1).In(east), at(3).In(east), at(8), at(14).In(west), at(19)}
	observed := []FundingEvent{
		{FundingTime: at(1).In(west), Rate: zero},
		{FundingTime: at(5).In(east), Rate: signed},
		{FundingTime: at(8).In(east), Rate: signed},
		{FundingTime: at(10).In(west), Rate: zero},
	}
	oldExpected, oldObserved := slices.Clone(expected), slices.Clone(observed)
	want := FundingCoverage{Missing: []time.Time{at(3), at(14), at(19)}, Unexpected: []time.Time{at(5), at(10)}}
	got, err := CompareFundingCoverage(expected, observed)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
	for _, group := range [][]time.Time{got.Missing, got.Unexpected} {
		for _, stamp := range group {
			if stamp.Location() != time.UTC {
				t.Fatalf("non-UTC stamp: %v", stamp)
			}
		}
		group[0] = epoch
	}
	again, err := CompareFundingCoverage(expected, observed)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatalf("repeated result changed: %+v, %v", again, err)
	}
	if !reflect.DeepEqual(expected, oldExpected) || !reflect.DeepEqual(observed, oldObserved) {
		t.Fatal("input changed")
	}
}

// TestCompareFundingCoverageEdges проверяет крайние допустимые значения и пустой ряд.
func TestCompareFundingCoverageEdges(t *testing.T) {
	epoch := time.UnixMilli(0).UTC()
	upper := time.Date(294276, time.December, 31, 23, 59, 59, 999000000, time.UTC)
	east := time.FixedZone("east", 3600)
	schedule := []time.Time{epoch.In(east), upper.In(east)}
	events := []FundingEvent{{FundingTime: epoch}, {FundingTime: upper}}
	got, err := CompareFundingCoverage(schedule, events)
	if err != nil || !reflect.DeepEqual(got, FundingCoverage{}) {
		t.Fatalf("match should be zero: %+v, %v", got, err)
	}
	got, err = CompareFundingCoverage(schedule[:1], events)
	wantTail := FundingCoverage{Unexpected: []time.Time{upper}}
	if err != nil || !reflect.DeepEqual(got, wantTail) {
		t.Fatalf("unexpected tail: got %+v, %v; want %+v", got, err, wantTail)
	}
	for _, empty := range [][]FundingEvent{nil, {}} {
		got, err := CompareFundingCoverage(schedule, empty)
		want := FundingCoverage{Missing: []time.Time{epoch, upper}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("empty observations: got %+v, %v; want %+v", got, err, want)
		}
	}
}

// TestCompareFundingCoverageInvalid проверяет типизированный отказ без частичных результатов.
func TestCompareFundingCoverageInvalid(t *testing.T) {
	epoch := time.UnixMilli(0).UTC()
	next := epoch.Add(time.Millisecond)
	later := next.Add(time.Millisecond)
	east := time.FixedZone("east", 3600)
	west := time.FixedZone("west", -3600)
	above := time.Date(294277, time.January, 1, 0, 0, 0, 0, time.UTC).In(west)
	valid := []time.Time{epoch, later}
	tests := []struct {
		name     string
		expected []time.Time
		observed []FundingEvent
	}{
		{"nil schedule", nil, nil},
		{"empty schedule", []time.Time{}, nil},
		{"expected duplicate", []time.Time{epoch, epoch.In(east)}, nil},
		{"expected descending", []time.Time{later, next}, nil},
		{"expected before epoch", []time.Time{epoch.Add(-time.Millisecond).In(east)}, nil},
		{"expected submillisecond", []time.Time{epoch, later.Add(time.Nanosecond)}, []FundingEvent{{FundingTime: next}}},
		{"expected above upper", []time.Time{epoch, above}, nil},
		{"observed duplicate", valid, []FundingEvent{{FundingTime: next}, {FundingTime: next.In(east)}}},
		{"observed descending", valid, []FundingEvent{{FundingTime: later}, {FundingTime: next}}},
		{"observed before epoch", valid, []FundingEvent{{FundingTime: epoch.Add(-time.Millisecond).In(east)}}},
		{"observed submillisecond", valid, []FundingEvent{{FundingTime: next}, {FundingTime: later.Add(time.Nanosecond)}}},
		{"observed above upper", valid, []FundingEvent{{FundingTime: next}, {FundingTime: above}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CompareFundingCoverage(test.expected, test.observed)
			if !errors.Is(err, ErrFundingCoverage) || !reflect.DeepEqual(got, FundingCoverage{}) {
				t.Fatalf("got %+v, %v; want zero result and ErrFundingCoverage", got, err)
			}
		})
	}
}
