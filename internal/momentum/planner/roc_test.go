package planner

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
)

// rocFixture строит три равных дневных ряда с заданными конечными ценами.
func rocFixture(t *testing.T, count int, ends []string) (map[string][]data.DailyClose, time.Time) {
	t.Helper()
	symbols := []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	series := make(map[string][]data.DailyClose, len(symbols))
	for n, symbol := range symbols {
		series[symbol] = make([]data.DailyClose, count)
		for i := range series[symbol] {
			price, err := model.ParsePrice("10")
			if i == count-1 && n < len(ends) {
				price, err = model.ParsePrice(ends[n])
			}
			if err != nil {
				t.Fatal(err)
			}
			series[symbol][i] = data.DailyClose{Symbol: symbol, OpenTime: start.AddDate(0, 0, i), Close: price, Confirmed: true}
		}
	}
	return series, start.AddDate(0, 0, count)
}

// TestROC30 проверяет точные флаги и независимость результата.
func TestROC30(t *testing.T) {
	symbols := []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}
	series, now := rocFixture(t, 31, []string{"11", "9", "10"})
	before := make(map[string][]data.DailyClose, len(series))
	for k, rows := range series {
		before[k] = append([]data.DailyClose(nil), rows...)
	}
	inputSymbols := append([]string(nil), symbols...)
	got, err := ROC30(symbols, series, now)
	historical, historyErr := ROC30(symbols, series, now.AddDate(1, 0, 0))
	if historyErr != nil || !reflect.DeepEqual(got, historical) {
		t.Fatalf("historical=%v err=%v", historical, historyErr)
	}
	if !reflect.DeepEqual(series, before) || !reflect.DeepEqual(symbols, inputSymbols) {
		t.Fatal("call changed input")
	}
	if err != nil || !reflect.DeepEqual(got, map[string]bool{symbols[0]: true, symbols[1]: false, symbols[2]: false}) {
		t.Fatalf("flags=%v err=%v", got, err)
	}
	got[symbols[0]] = false
	next, err := ROC30(symbols, series, now)
	if err != nil || !next[symbols[0]] || len(next) != len(symbols) {
		t.Fatalf("repeat=%v err=%v", next, err)
	}
	series, now = rocFixture(t, 33, []string{"11", "9", "10"})
	series[symbols[0]][0].Close, _ = model.ParsePrice("20")
	series[symbols[0]][31].Close, _ = model.ParsePrice("11")
	series[symbols[1]][31].Close, _ = model.ParsePrice("9")
	series[symbols[2]][31].Close, _ = model.ParsePrice("10")
	series[symbols[0]][32].Close, _ = model.ParsePrice("9")
	for _, symbol := range symbols {
		series[symbol][32].Confirmed = false
	}
	got, err = ROC30(symbols, series, now.AddDate(0, 0, -1).Add(12*time.Hour))
	if err != nil || !got[symbols[0]] || got[symbols[1]] {
		t.Fatalf("window=%v err=%v", got, err)
	}
	series, now = rocFixture(t, 31, []string{"100000000000000001", "99999999999999999", "10"})
	for _, symbol := range symbols[:2] {
		series[symbol][0].Close, _ = model.ParsePrice("100000000000000000")
	}
	got, err = ROC30(symbols, series, now)
	if err != nil || !got[symbols[0]] || got[symbols[1]] {
		t.Fatalf("precision=%v err=%v", got, err)
	}
	series, now = rocFixture(t, 31, []string{"4.2", "2", "3"})
	series[symbols[0]][0].Close, _ = model.ParsePrice("3.1")
	got, err = ROC30(symbols, series, now)
	if err != nil || !got[symbols[0]] {
		t.Fatalf("fraction=%v err=%v", got, err)
	}
}

// TestROC30Errors проверяет передачу ошибок и отсутствие частичного результата.
func TestROC30Errors(t *testing.T) {
	symbols := []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}
	tests := []struct {
		name string
		edit func(map[string][]data.DailyClose, *time.Time)
		want data.WindowReason
	}{
		{"invalid symbols", func(_ map[string][]data.DailyClose, _ *time.Time) {}, data.ReasonInvalidInput},
		{"missing", func(s map[string][]data.DailyClose, _ *time.Time) { delete(s, symbols[2]) }, data.ReasonMissingSymbol},
		{"extra", func(s map[string][]data.DailyClose, _ *time.Time) { s["SOLUSDT"] = nil }, data.ReasonInvalidInput},
		{"short", func(s map[string][]data.DailyClose, _ *time.Time) { s[symbols[0]] = nil }, data.ReasonInsufficient},
		{"zero price", func(s map[string][]data.DailyClose, _ *time.Time) { s[symbols[0]][4].Close = model.Zero() }, data.ReasonInvalidPrice},
		{"negative price", func(s map[string][]data.DailyClose, _ *time.Time) {
			s[symbols[0]][4].Close, _ = model.ParseAmount("-1")
		}, data.ReasonInvalidPrice},
		{"uninitialized price", func(s map[string][]data.DailyClose, _ *time.Time) { s[symbols[0]][4].Close = model.Decimal{} }, data.ReasonInvalidPrice},
		{"malformed middle", func(s map[string][]data.DailyClose, _ *time.Time) { s[symbols[0]][4].Symbol = "OTHER" }, data.ReasonMixedSymbol},
		{"gap", func(s map[string][]data.DailyClose, n *time.Time) {
			for i := 5; i < 31; i++ {
				s[symbols[0]][i].OpenTime = s[symbols[0]][i].OpenTime.Add(24 * time.Hour)
			}
			*n = n.Add(24 * time.Hour)
		}, data.ReasonGap},
		{"unconfirmed", func(s map[string][]data.DailyClose, _ *time.Time) { s[symbols[0]][30].Confirmed = false }, data.ReasonUnconfirmed},
		{"different end", func(s map[string][]data.DailyClose, n *time.Time) {
			for i := range s[symbols[1]] {
				s[symbols[1]][i].OpenTime = s[symbols[1]][i].OpenTime.Add(24 * time.Hour)
			}
			*n = n.Add(48 * time.Hour)
		}, data.ReasonDifferentEnd},
		{"late error", func(s map[string][]data.DailyClose, _ *time.Time) { s[symbols[2]][5].Close = model.Zero() }, data.ReasonInvalidPrice},
		{"zero time", func(_ map[string][]data.DailyClose, n *time.Time) { *n = time.Time{} }, data.ReasonInvalidInput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			series, now := rocFixture(t, 31, []string{"11", "11", "11"})
			tc.edit(series, &now)
			before := make(map[string][]data.DailyClose, len(series))
			for k, rows := range series {
				before[k] = append([]data.DailyClose(nil), rows...)
			}
			inputSymbols := symbols
			if tc.name == "invalid symbols" {
				inputSymbols = []string{"btcusdt"}
			}
			got, err := ROC30(inputSymbols, series, now)
			var reason data.WindowReason
			if got != nil || err != tc.want || !errors.Is(err, tc.want) || !errors.As(err, &reason) || reason != tc.want || !reflect.DeepEqual(series, before) {
				t.Fatalf("result=%v err=%v want=%v", got, err, tc.want)
			}
		})
	}
}
