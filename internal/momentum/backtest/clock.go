package backtest

import (
	"errors"
	"time"
)

// ErrWeeklyRange сообщает, что конец окна раньше его начала.
var ErrWeeklyRange = errors.New("weekly range end precedes start")

// FirstWeeklyBoundary возвращает первую границу понедельника 00:00 UTC в окне [start, end).
func FirstWeeklyBoundary(start, end time.Time) (time.Time, bool, error) {
	start = start.Round(0).UTC()
	end = end.Round(0).UTC()
	if end.Before(start) {
		return time.Time{}, false, ErrWeeklyRange
	}
	if end.Equal(start) {
		return time.Time{}, false, nil
	}

	candidate := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	daysUntilMonday := (int(time.Monday) - int(candidate.Weekday()) + 7) % 7
	candidate = candidate.AddDate(0, 0, daysUntilMonday)
	if candidate.Before(start) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	if !candidate.Before(end) {
		return time.Time{}, false, nil
	}
	return candidate, true, nil
}
