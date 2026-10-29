package data

import (
	"strings"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

const dailyWindowSize = 31

// DailyClose хранит минимальную проекцию закрытия одного дневного ряда.
type DailyClose struct {
	Symbol    string
	OpenTime  time.Time
	Close     model.Decimal
	Confirmed bool
}

// WindowReason обозначает проверяемую причину отказа дневного окна.
type WindowReason string

// Error возвращает стабильное описание причины окна.
func (r WindowReason) Error() string { return string(r) }

const (
	ReasonInvalidInput WindowReason = "invalid daily window input"
	ReasonMixedSymbol  WindowReason = "daily candle symbol mismatch"
	ReasonMisaligned   WindowReason = "daily candle is not UTC midnight"
	ReasonDuplicate    WindowReason = "duplicate daily candle timestamp"
	ReasonOutOfOrder   WindowReason = "daily candles are out of order"
	ReasonFuture       WindowReason = "daily candle is future or prematurely confirmed"
	ReasonUnconfirmed  WindowReason = "closed daily candle is unconfirmed"
	ReasonInvalidPrice WindowReason = "daily close is not positive"
	ReasonInsufficient WindowReason = "fewer than 31 confirmed daily candles"
	ReasonGap          WindowReason = "gap in selected daily window"
)

// SelectDailyWindow проверяет один ряд и возвращает последние 31 закрытых суток.
func SelectDailyWindow(symbol string, candles []DailyClose, serverTime time.Time) ([]DailyClose, error) {
	if symbol == "" || strings.TrimSpace(symbol) != symbol || strings.ToUpper(symbol) != symbol || serverTime.IsZero() {
		return nil, ReasonInvalidInput
	}
	seen := make(map[[3]int]struct{}, len(candles))
	confirmed := make([]DailyClose, 0, len(candles))
	var previous time.Time
	for _, candle := range candles {
		if candle.Symbol != symbol {
			return nil, ReasonMixedSymbol
		}
		open := candle.OpenTime
		year, month, day := open.UTC().Date()
		utc := open.UTC()
		if open.IsZero() || utc.Hour() != 0 || utc.Minute() != 0 || utc.Second() != 0 || utc.Nanosecond() != 0 {
			return nil, ReasonMisaligned
		}
		key := [3]int{year, int(month), day}
		if _, exists := seen[key]; exists {
			return nil, ReasonDuplicate
		}
		seen[key] = struct{}{}
		if !previous.IsZero() && !open.After(previous) {
			return nil, ReasonOutOfOrder
		}
		previous = open
		if open.After(serverTime) || (candle.Confirmed && (serverTime.Before(open) || serverTime.Sub(open) < 24*time.Hour)) {
			return nil, ReasonFuture
		}
		if !candle.Confirmed && !serverTime.Before(open) && serverTime.Sub(open) >= 24*time.Hour {
			return nil, ReasonUnconfirmed
		}
		cmp, err := candle.Close.Cmp(model.Zero())
		if err != nil || cmp <= 0 {
			return nil, ReasonInvalidPrice
		}
		if candle.Confirmed {
			confirmed = append(confirmed, candle)
		}
	}
	if len(confirmed) < dailyWindowSize {
		return nil, ReasonInsufficient
	}
	start := len(confirmed) - dailyWindowSize
	window := append([]DailyClose(nil), confirmed[start:]...)
	for i := 1; i < len(window); i++ {
		if window[i].OpenTime.Sub(window[i-1].OpenTime) != 24*time.Hour {
			return nil, ReasonGap
		}
	}
	return window, nil
}
