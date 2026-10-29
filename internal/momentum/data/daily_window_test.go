package data

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// commonSeriesInput строит три ряда с общим концом и заданной открытой свечой.
func commonSeriesInput(commonSymbols []string, count int, openLast bool) (map[string][]DailyClose, time.Time) {
	start := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	price, _ := model.ParsePrice("12.34")
	series := make(map[string][]DailyClose, len(commonSymbols))
	for _, symbol := range commonSymbols {
		series[symbol] = make([]DailyClose, count)
		for i := range series[symbol] {
			series[symbol][i] = DailyClose{symbol, start.AddDate(0, 0, i), price, !openLast || i < count-1}
		}
	}
	now := start.AddDate(0, 0, count)
	if openLast {
		now = start.AddDate(0, 0, count-1).Add(12 * time.Hour)
	}
	return series, now
}

// editCommonCase настраивает конфликт или ошибку для сценария композиции.
func editCommonCase(commonSymbols []string, edit string, series map[string][]DailyClose, now *time.Time) {
	switch edit {
	case "different-end", "different-end-third-error":
		for i := range series[commonSymbols[1]] {
			series[commonSymbols[1]][i].OpenTime = series[commonSymbols[1]][i].OpenTime.AddDate(0, 0, 1)
		}
		*now = now.Add(24 * time.Hour)
		if edit == "different-end-third-error" {
			series[commonSymbols[2]][0].Symbol = "OTHER"
		}
	case "multiple-errors":
		series[commonSymbols[0]][0].Symbol = "OTHER"
		series[commonSymbols[1]][0].Close = model.Zero()
	case "missing-extra":
		delete(series, commonSymbols[2])
		series["SOLUSDT"] = nil
	case "empty-row":
		series[commonSymbols[2]] = nil
	case "extra-row":
		series["SOLUSDT"] = nil
	case "zero-time":
		*now = time.Time{}
	}
}

// requireCommonFailure проверяет nil и точную typed-причину ошибки.
func requireCommonFailure(t *testing.T, got map[string][]DailyClose, err error, want WindowReason) {
	t.Helper()
	var reason WindowReason
	if got != nil || err != want || !errors.Is(err, want) || !errors.As(err, &reason) || reason != want {
		t.Fatalf("got=%v err=%v want=%v", got, err, want)
	}
}

// TestCommonDailyWindow проверяет общий выбор, детерминированные ошибки и ownership.
func TestCommonDailyWindow(t *testing.T) {
	commonSymbols := []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}
	tests := []struct {
		name                 string
		symbols              []string
		count                int
		openLast, historical bool
		edit                 string
		want                 WindowReason
	}{
		{"31 closed on all symbols", commonSymbols, 31, false, false, "", ""},
		{"latest 31 of 32 closed", commonSymbols, 32, false, false, "", ""},
		{"32nd row is open", commonSymbols, 32, true, false, "", ""},
		{"matching historical end", commonSymbols, 31, false, true, "", ""},
		{"different ends", commonSymbols, 31, false, false, "different-end", ReasonDifferentEnd},
		{"later row error precedes different end", commonSymbols, 31, false, false, "different-end-third-error", ReasonMixedSymbol},
		{"first required symbol error", commonSymbols, 31, false, false, "multiple-errors", ReasonMixedSymbol},
		{"reordered symbols choose first error", []string{commonSymbols[1], commonSymbols[0], commonSymbols[2]}, 31, false, false, "multiple-errors", ReasonInvalidPrice},
		{"missing key before extra", commonSymbols, 31, false, false, "missing-extra", ReasonMissingSymbol},
		{"extra key", commonSymbols, 31, false, false, "extra-row", ReasonInvalidInput},
		{"empty present row", commonSymbols, 31, false, false, "empty-row", ReasonInsufficient},
		{"empty required list", []string{}, 31, false, false, "", ReasonInvalidInput},
		{"empty symbol", []string{""}, 31, false, false, "", ReasonInvalidInput},
		{"lowercase symbol", []string{"btcusdt"}, 31, false, false, "", ReasonInvalidInput},
		{"leading-space symbol", []string{" BTCUSDT"}, 31, false, false, "", ReasonInvalidInput},
		{"trailing-space symbol", []string{"BTCUSDT "}, 31, false, false, "", ReasonInvalidInput},
		{"duplicate required symbol", []string{"BTCUSDT", "BTCUSDT"}, 31, false, false, "", ReasonInvalidInput},
		{"zero server time", commonSymbols, 31, false, false, "zero-time", ReasonInvalidInput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			series, now := commonSeriesInput(commonSymbols, tc.count, tc.openLast)
			if tc.historical {
				now = now.AddDate(0, 0, 365)
			}
			editCommonCase(commonSymbols, tc.edit, series, &now)
			beforeSymbols := slices.Clone(tc.symbols)
			before := make(map[string][]DailyClose, len(series))
			for symbol, rows := range series {
				before[symbol] = slices.Clone(rows)
			}
			got, err := CommonDailyWindow(tc.symbols, series, now)
			if !reflect.DeepEqual(series, before) || !slices.Equal(tc.symbols, beforeSymbols) {
				t.Fatal("call changed input")
			}
			if tc.want != "" {
				requireCommonFailure(t, got, err, tc.want)
				return
			}
			if err != nil || len(got) != len(tc.symbols) {
				t.Fatalf("result=%v err=%v", got, err)
			}
			last := tc.count - 1
			if tc.openLast {
				last--
			}
			first := last - 30
			for _, symbol := range tc.symbols {
				rows := got[symbol]
				if !slices.Equal(rows, before[symbol][first:last+1]) {
					t.Fatalf("wrong %s window: %v", symbol, rows)
				}
			}
			price, err := got[tc.symbols[0]][0].Close.WireDecimal()
			if err != nil || price != "12.34" {
				t.Fatalf("close=%s err=%v", price, err)
			}
			if tc.count == 31 && !tc.historical {
				delete(got, tc.symbols[0])
				got[tc.symbols[1]][0].Symbol = "CHANGED"
				if len(series) != len(tc.symbols) || series[tc.symbols[1]][0].Symbol != tc.symbols[1] {
					t.Fatal("result changed input")
				}
				series[tc.symbols[2]][0].Symbol = "CHANGED"
				delete(series, tc.symbols[1])
				if len(got) != 2 || got[tc.symbols[2]][0].Symbol != tc.symbols[2] {
					t.Fatal("input changed result")
				}
			}
		})
	}
}
