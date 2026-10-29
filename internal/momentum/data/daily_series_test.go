package data

import (
	"errors"
	"slices"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// dailySeriesFixture строит подтверждённый ряд с исходной точной ценой.
func dailySeriesFixture(start time.Time, price model.Decimal, count int) []DailyClose {
	candles := make([]DailyClose, count)
	for i := range candles {
		candles[i] = DailyClose{"BTCUSDT", start.AddDate(0, 0, i), price, true}
	}
	return candles
}

// requireWindowFailure проверяет nil и типизированную причину отказа.
func requireWindowFailure(t *testing.T, got []DailyClose, err error, want WindowReason) {
	t.Helper()
	var reason WindowReason
	if got != nil || !errors.Is(err, want) || !errors.As(err, &reason) || reason != want {
		t.Fatalf("got=%v err=%v want=%q", got, err, want)
	}
}

// TestSelectDailyWindow проверяет выбор, валидацию всех записей и независимость slice.
func TestSelectDailyWindow(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	price, err := model.ParsePrice("1.230000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	negative, err := model.ParseAmount("-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		count, first int
		lag          time.Duration
		edit         func([]DailyClose)
		want         WindowReason
	}{
		{"31 closed", 31, 0, 0, nil, ""},
		{"latest of 36", 36, 5, 0, nil, ""},
		{"historical", 31, 0, 365 * 24 * time.Hour, nil, ""},
		{"open at start", 32, 0, -24 * time.Hour, func(c []DailyClose) { c[31].Confirmed = false }, ""},
		{"open midday", 32, 0, -12 * time.Hour, func(c []DailyClose) { c[31].Confirmed = false }, ""},
		{"open end minus ns", 32, 0, -time.Nanosecond, func(c []DailyClose) { c[31].Confirmed = false }, ""},
		{"confirmed exact end", 32, 1, 0, nil, ""},
		{"unconfirmed exact end", 32, 0, 0, func(c []DailyClose) { c[31].Confirmed = false }, ReasonUnconfirmed},
		{"premature true", 31, 0, -time.Nanosecond, nil, ReasonFuture},
		{"zero open", 31, 0, 0, func(c []DailyClose) { c[0].OpenTime = time.Time{} }, ReasonMisaligned},
		{"misaligned", 31, 0, 0, func(c []DailyClose) { c[0].OpenTime = start.Add(time.Hour) }, ReasonMisaligned},
		{"nanoseconds", 31, 0, 0, func(c []DailyClose) { c[0].OpenTime = start.Add(time.Nanosecond) }, ReasonMisaligned},
		{"mixed", 31, 0, 0, func(c []DailyClose) { c[0].Symbol = "ETHUSDT" }, ReasonMixedSymbol},
		{"mixed before alignment", 31, 0, 0, func(c []DailyClose) {
			c[0].Symbol = "ETHUSDT"
			c[0].OpenTime = time.Time{}
		}, ReasonMixedSymbol},
		{"nonadjacent duplicate", 31, 0, 0, func(c []DailyClose) { c[2].OpenTime = start }, ReasonDuplicate},
		{"duplicate location", 31, 0, 0, func(c []DailyClose) {
			c[2].OpenTime = start.In(time.FixedZone("other", 3600))
		}, ReasonDuplicate},
		{"decreasing unique", 31, 0, 0, func(c []DailyClose) { c[2].OpenTime = start.AddDate(0, 0, -1) }, ReasonOutOfOrder},
		{"future false", 31, 0, 0, func(c []DailyClose) {
			c[30] = DailyClose{"BTCUSDT", start.AddDate(0, 0, 32), price, false}
		}, ReasonFuture},
		{"future before price", 31, 0, -time.Nanosecond, func(c []DailyClose) { c[30].Close = model.Zero() }, ReasonFuture},
		{"historical false", 31, 0, 0, func(c []DailyClose) { c[0].Confirmed = false }, ReasonUnconfirmed},
		{"unconfirmed before price", 31, 0, 0, func(c []DailyClose) {
			c[0].Confirmed = false
			c[0].Close = model.Zero()
		}, ReasonUnconfirmed},
		{"zero price", 31, 0, 0, func(c []DailyClose) { c[0].Close = model.Zero() }, ReasonInvalidPrice},
		{"negative price", 31, 0, 0, func(c []DailyClose) { c[0].Close = negative }, ReasonInvalidPrice},
		{"unset price", 31, 0, 0, func(c []DailyClose) { c[0].Close = model.Decimal{} }, ReasonInvalidPrice},
		{"malformed old", 32, 0, 0, func(c []DailyClose) { c[0].Close = model.Decimal{} }, ReasonInvalidPrice},
		{"malformed open", 32, 0, -time.Hour, func(c []DailyClose) {
			c[31].Confirmed = false
			c[31].Close = model.Zero()
		}, ReasonInvalidPrice},
		{"insufficient", 30, 0, 0, nil, ReasonInsufficient},
		{"gap before window", 32, 1, 48 * time.Hour, func(c []DailyClose) {
			for i := 1; i < len(c); i++ {
				c[i].OpenTime = c[i].OpenTime.Add(24 * time.Hour)
			}
		}, ""},
		{"gap in window", 31, 0, 48 * time.Hour, func(c []DailyClose) {
			for i := 20; i < len(c); i++ {
				c[i].OpenTime = c[i].OpenTime.Add(24 * time.Hour)
			}
		}, ReasonGap},
		{"UTC midnight location", 31, 0, 0, func(c []DailyClose) {
			c[0].OpenTime = start.In(time.FixedZone("other", 3600))
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := dailySeriesFixture(start, price, tc.count)
			if tc.edit != nil {
				tc.edit(input)
			}
			before := slices.Clone(input)
			now := start.AddDate(0, 0, tc.count).Add(tc.lag)
			got, err := SelectDailyWindow("BTCUSDT", input, now)
			if !slices.Equal(input, before) {
				t.Fatal("call changed input")
			}
			if tc.want != "" {
				requireWindowFailure(t, got, err, tc.want)
				return
			}
			if err != nil || !slices.Equal(got, before[tc.first:tc.first+31]) {
				t.Fatalf("wrong window: got=%v err=%v", got, err)
			}
			got[0].Close = model.Zero()
			if !slices.Equal(input, before) {
				t.Fatal("result mutation changed input")
			}
			input[tc.first+30].Symbol = "CHANGED"
			if got[30].Symbol != "BTCUSDT" {
				t.Fatal("input mutation changed result")
			}
		})
	}
	for _, symbol := range []string{"", " btcusdt", "BTCUSDT ", "btcusdt"} {
		got, err := SelectDailyWindow(symbol, dailySeriesFixture(start, price, 31), start.AddDate(0, 0, 31))
		requireWindowFailure(t, got, err, ReasonInvalidInput)
	}
	got, err := SelectDailyWindow("BTCUSDT", nil, start)
	requireWindowFailure(t, got, err, ReasonInsufficient)
	got, err = SelectDailyWindow("BTCUSDT", []DailyClose{}, start)
	requireWindowFailure(t, got, err, ReasonInsufficient)
	got, err = SelectDailyWindow("BTCUSDT", dailySeriesFixture(start, price, 31), time.Time{})
	requireWindowFailure(t, got, err, ReasonInvalidInput)
}
