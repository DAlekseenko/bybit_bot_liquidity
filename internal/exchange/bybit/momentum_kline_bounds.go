package bybit

import (
	"fmt"
	"time"
)

// dailyRangeBounds проверяет точные UTC-границы с исключающим концом диапазона.
func dailyRangeBounds(start, end time.Time) (startMs, endMs int64, err error) {
	const dayMs int64 = 86_400_000
	var bounds [2]int64
	for i, boundary := range [2]time.Time{start, end} {
		utc := boundary.UTC()
		if utc.Hour() != 0 || utc.Minute() != 0 || utc.Second() != 0 || utc.Nanosecond() != 0 {
			return 0, 0, fmt.Errorf("daily range: %w: boundary %d is not UTC midnight", ErrInvalidArgument, i)
		}
		ms := utc.UnixMilli()
		if !time.UnixMilli(ms).Equal(utc) {
			return 0, 0, fmt.Errorf("daily range: %w: boundary %d is not representable", ErrInvalidArgument, i)
		}
		if ms <= 0 {
			return 0, 0, fmt.Errorf("daily range: %w: boundary %d must be positive", ErrInvalidArgument, i)
		}
		bounds[i] = ms
	}
	if bounds[0] >= bounds[1] {
		return 0, 0, fmt.Errorf("daily range: %w: start must precede end", ErrInvalidArgument)
	}
	// Положительные индексы суток позволяют безопасно вычесть даже дальние даты.
	if bounds[1]/dayMs-bounds[0]/dayMs > 3660 {
		return 0, 0, fmt.Errorf("daily range: %w: range exceeds 3660 days", ErrInvalidArgument)
	}
	return bounds[0], bounds[1], nil
}
