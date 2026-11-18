package model

import (
	"errors"
	"time"
)

// ErrFundingCoverage обозначает недопустимые входные моменты сверки funding.
var ErrFundingCoverage = errors.New("invalid funding coverage input")

// FundingCoverage содержит пропущенные и неожиданные моменты funding в UTC.
type FundingCoverage struct {
	Missing, Unexpected []time.Time
}

// CompareFundingCoverage сравнивает ожидаемые и наблюдённые моменты funding.
func CompareFundingCoverage(expected []time.Time, observed []FundingEvent) (FundingCoverage, error) {
	if len(expected) == 0 {
		return FundingCoverage{}, ErrFundingCoverage
	}
	for i, stamp := range expected {
		if !validFundingCoverageTime(stamp) || i > 0 && !expected[i-1].UTC().Before(stamp) {
			return FundingCoverage{}, ErrFundingCoverage
		}
	}
	for i, event := range observed {
		stamp := event.FundingTime
		if !validFundingCoverageTime(stamp) || i > 0 && !observed[i-1].FundingTime.UTC().Before(stamp) {
			return FundingCoverage{}, ErrFundingCoverage
		}
	}

	var result FundingCoverage
	i, j := 0, 0
	for i < len(expected) && j < len(observed) {
		want, got := expected[i].UTC(), observed[j].FundingTime.UTC()
		switch want.Compare(got) {
		case -1:
			result.Missing = append(result.Missing, want)
			i++
		case 1:
			result.Unexpected = append(result.Unexpected, got)
			j++
		default:
			i++
			j++
		}
	}
	for _, stamp := range expected[i:] {
		result.Missing = append(result.Missing, stamp.UTC())
	}
	for _, event := range observed[j:] {
		result.Unexpected = append(result.Unexpected, event.FundingTime.UTC())
	}
	return result, nil
}

// validFundingCoverageTime проверяет точность и диапазон UTC-момента.
func validFundingCoverageTime(stamp time.Time) bool {
	year := stamp.UTC().Year()
	return year >= 1970 && year <= 294276 && stamp.Nanosecond()%int(time.Millisecond) == 0
}
