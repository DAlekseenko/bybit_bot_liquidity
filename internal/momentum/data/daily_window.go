package data

import (
	"strings"
	"time"
)

// ReasonMissingSymbol обозначает отсутствие обязательного ряда.
const ReasonMissingSymbol WindowReason = "missing daily series"

// ReasonDifferentEnd обозначает разные последние сутки рядов.
const ReasonDifferentEnd WindowReason = "daily series have different end dates"

// CommonDailyWindow согласует последние сутки подготовленных рядов символов.
func CommonDailyWindow(symbols []string, series map[string][]DailyClose, serverTime time.Time) (map[string][]DailyClose, error) {
	if len(symbols) == 0 || serverTime.IsZero() {
		return nil, ReasonInvalidInput
	}
	seen := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		if symbol == "" || symbol != strings.TrimSpace(symbol) || symbol != strings.ToUpper(symbol) {
			return nil, ReasonInvalidInput
		}
		if _, ok := seen[symbol]; ok {
			return nil, ReasonInvalidInput
		}
		seen[symbol] = struct{}{}
	}
	for _, symbol := range symbols {
		if _, ok := series[symbol]; !ok {
			return nil, ReasonMissingSymbol
		}
	}
	for symbol := range series {
		if _, ok := seen[symbol]; !ok {
			return nil, ReasonInvalidInput
		}
	}

	windows := make(map[string][]DailyClose, len(symbols))
	for _, symbol := range symbols {
		window, err := SelectDailyWindow(symbol, series[symbol], serverTime)
		if err != nil {
			return nil, err
		}
		windows[symbol] = window
	}
	commonEnd := windows[symbols[0]][dailyWindowSize-1].OpenTime
	for _, symbol := range symbols[1:] {
		end := windows[symbol][dailyWindowSize-1].OpenTime
		if !end.Equal(commonEnd) {
			return nil, ReasonDifferentEnd
		}
	}
	return windows, nil
}
