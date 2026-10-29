package bybit

import (
	"errors"
	"math"
	"testing"
	"time"
)

// TestDailyRangeBounds проверяет календарные границы и точные исключающие Unix-ms.
func TestDailyRangeBounds(t *testing.T) {
	start := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	maxTime := time.UnixMilli(math.MaxInt64).UTC()
	year, month, day := maxTime.Date()
	lastMidnight := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	lastMs := int64(math.MaxInt64 - math.MaxInt64%86_400_000)
	zone := time.FixedZone("UTC+3", 3*60*60)
	valid := []struct {
		name           string
		start, end     time.Time
		startMs, endMs int64
	}{
		{"one day", start, end, 1704067200000, 1704153600000},
		{"3660 days", start, start.AddDate(0, 0, 3660), 1704067200000, 2020291200000},
		{"non UTC locations", start.In(zone), end.In(zone), 1704067200000, 1704153600000},
		{"last representable midnights", lastMidnight.AddDate(0, 0, -1), lastMidnight, lastMs - 86_400_000, lastMs},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			gotStart, gotEnd, err := dailyRangeBounds(tc.start, tc.end)
			if err != nil || gotStart != tc.startMs || gotEnd != tc.endMs {
				t.Fatalf("got (%d, %d, %v), want (%d, %d, nil)", gotStart, gotEnd, err, tc.startMs, tc.endMs)
			}
		})
	}
	invalid := []struct {
		name  string
		value time.Time
	}{
		{"zero", time.Time{}},
		{"epoch", time.Unix(0, 0)},
		{"negative", time.Date(1969, time.December, 31, 0, 0, 0, 0, time.UTC)},
		{"local midnight", time.Date(2024, time.January, 1, 0, 0, 0, 0, zone)},
		{"hour", start.Add(time.Hour)},
		{"minute", start.Add(time.Minute)},
		{"second", start.Add(time.Second)},
		{"one nanosecond", start.Add(time.Nanosecond)},
		{"sub millisecond", start.Add(999999 * time.Nanosecond)},
		{"beyond int64 milliseconds", lastMidnight.AddDate(0, 0, 1)},
		{"far future", time.Date(600000000, time.January, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			assertInvalidDailyBounds(t, tc.value, end)
			assertInvalidDailyBounds(t, start, tc.value)
		})
	}
	for _, days := range []int{0, -1, 3661, 10000} {
		assertInvalidDailyBounds(t, start, start.AddDate(0, 0, days))
	}
	// Соседние дальние даты имеют положительные, но переполненные Unix-ms.
	// Без roundtrip они пройдут проверки порядка и короткой длины диапазона.
	far := time.Date(600000000, time.January, 1, 0, 0, 0, 0, time.UTC)
	farEnd := far.AddDate(0, 0, 1)
	if far.UnixMilli() <= 0 || farEnd.UnixMilli()-far.UnixMilli() != 86_400_000 {
		t.Fatal("far-future fixture must isolate the roundtrip check")
	}
	assertInvalidDailyBounds(t, far, farEnd)
	wrapped := time.UnixMilli(far.UnixMilli()).UTC()
	wrappedYear, wrappedMonth, wrappedDay := wrapped.Date()
	wrappedMidnight := time.Date(wrappedYear, wrappedMonth, wrappedDay, 0, 0, 0, 0, time.UTC)
	assertInvalidDailyBounds(t, far, wrappedMidnight.AddDate(0, 0, 1))
	assertInvalidDailyBounds(t, wrappedMidnight, far)
	assertInvalidDailyBounds(t, lastMidnight, lastMidnight.AddDate(0, 0, 1))
	assertInvalidDailyBounds(t, lastMidnight.AddDate(0, 0, 1), lastMidnight.AddDate(0, 0, 2))
}

// assertInvalidDailyBounds проверяет sentinel и отсутствие частичного результата.
func assertInvalidDailyBounds(t *testing.T, start, end time.Time) {
	t.Helper()
	startMs, endMs, err := dailyRangeBounds(start, end)
	if !errors.Is(err, ErrInvalidArgument) || startMs != 0 || endMs != 0 {
		t.Fatalf("got (%d, %d, %v), want (0, 0, ErrInvalidArgument)", startMs, endMs, err)
	}
}
