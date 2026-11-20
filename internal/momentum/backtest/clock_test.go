package backtest

import (
	"errors"
	"testing"
	"time"
)

// TestFirstWeeklyBoundary проверяет диапазоны и нормализацию календарной границы.
func TestFirstWeeklyBoundary(t *testing.T) {
	utc := time.UTC
	local := time.FixedZone("UTC+9", 9*60*60)
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	week := time.Date(2024, time.January, 1, 0, 0, 0, 0, utc)
	newYear := time.Date(2024, time.January, 1, 0, 0, 0, 0, utc)
	leap := time.Date(2024, time.February, 26, 0, 0, 0, 0, utc)

	tests := []struct {
		name       string
		start, end time.Time
		want       time.Time
		ok         bool
		wantErr    error
	}{
		{name: "sunday to monday", start: week.Add(-time.Nanosecond), end: week.Add(time.Hour), want: week, ok: true},
		{name: "exact start included", start: week, end: week.Add(time.Nanosecond), want: week, ok: true},
		{name: "nanosecond after boundary", start: week.Add(time.Nanosecond), end: week.AddDate(0, 0, 8), want: week.AddDate(0, 0, 7), ok: true},
		{name: "nanosecond before boundary", start: week.Add(-time.Nanosecond), end: week, want: time.Time{}, ok: false},
		{name: "exclusive end", start: week.Add(-time.Hour), end: week, want: time.Time{}, ok: false},
		{name: "empty", start: week, end: week, want: time.Time{}, ok: false},
		{name: "reversed", start: week.Add(time.Hour), end: week, want: time.Time{}, ok: false, wantErr: ErrWeeklyRange},
		{name: "no boundary", start: week.Add(time.Hour), end: week.Add(24 * time.Hour), want: time.Time{}, ok: false},
		{name: "several boundaries", start: week.Add(time.Hour), end: week.AddDate(0, 0, 20), want: week.AddDate(0, 0, 7), ok: true},
		{name: "year transition", start: newYear.Add(-time.Nanosecond), end: newYear.Add(time.Hour), want: newYear, ok: true},
		{name: "leap year", start: leap.Add(-time.Hour), end: leap.Add(time.Hour), want: leap, ok: true},
		{name: "month transition", start: time.Date(2024, time.March, 31, 12, 0, 0, 0, utc), end: time.Date(2024, time.April, 10, 0, 0, 0, 0, utc), want: time.Date(2024, time.April, 1, 0, 0, 0, 0, utc), ok: true},
		{name: "local equivalent", start: week.In(local), end: week.Add(time.Hour).In(local), want: week, ok: true},
		{name: "DST transition", start: time.Date(2024, time.March, 10, 1, 30, 0, 0, newYork), end: time.Date(2024, time.March, 11, 1, 0, 0, 0, newYork), want: time.Date(2024, time.March, 11, 0, 0, 0, 0, time.UTC), ok: true},
		{name: "resumed window", start: week.AddDate(0, 0, 9), end: week.AddDate(0, 0, 20), want: week.AddDate(0, 0, 14), ok: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok, err := FirstWeeklyBoundary(test.start, test.end)
			if !errors.Is(err, test.wantErr) || ok != test.ok || !got.Equal(test.want) {
				t.Fatalf("got %v, %v, %v; want %v, %v, %v", got, ok, err, test.want, test.ok, test.wantErr)
			}
			if ok && got.Location() != time.UTC {
				t.Fatalf("result location = %v; want UTC", got.Location())
			}
		})
	}
}
