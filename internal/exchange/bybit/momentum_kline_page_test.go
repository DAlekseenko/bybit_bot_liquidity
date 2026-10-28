package bybit

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// dailyPageRow создаёт независимую строку с точными значениями всех шести полей.
func dailyPageRow(openMs int64) []string {
	return []string{strconv.FormatInt(openMs, 10), "9007199254740993.00000001", "12.34", "0.000000000000000001", "7.89", "0", "0"}
}

// TestParseDailyKlinePage проверяет точность, порядок, пустую страницу и лимит.
func TestParseDailyKlinePage(t *testing.T) {
	rows := [][]string{dailyPageRow(3 * 86400000), dailyPageRow(2 * 86400000)}
	rows[1][5] = "9007199254740993.00000001"
	rows[1][6] = "0.000000000000000003"
	before := [][]string{append([]string(nil), rows[0]...), append([]string(nil), rows[1]...)}
	got, err := parseDailyKlinePage("BTCUSDT", 4*86400000, klineResult{"linear", "BTCUSDT", rows})
	if err != nil || len(got) != 2 {
		t.Fatalf("page = %v, %v", got, err)
	}
	for i, candle := range got {
		if candle.Symbol != "BTCUSDT" {
			t.Fatalf("symbol %d = %q", i, candle.Symbol)
		}
		if candle.OpenTime != time.UnixMilli(int64(3-i)*86400000).UTC() || candle.OpenTime.Location() != time.UTC {
			t.Fatalf("time %d = %v", i, candle.OpenTime)
		}
		if !candle.Confirmed {
			t.Fatalf("candle %d = %+v", i, candle)
		}
		for j, actual := range []model.Decimal{candle.Open, candle.High, candle.Low, candle.Close, candle.Volume, candle.Turnover} {
			want, parseErr := model.ParseAmount(rows[i][j+1])
			cmp, cmpErr := actual.Cmp(want)
			if parseErr != nil || cmpErr != nil || cmp != 0 {
				t.Fatalf("candle %d field %d lost precision: %v, %v", i, j, parseErr, cmpErr)
			}
		}
	}
	if !reflect.DeepEqual(rows, before) {
		t.Fatal("input mutated")
	}
	for _, size := range []int{0, 200, 201} {
		list := make([][]string, size)
		for i := range list {
			list[i] = dailyPageRow(int64(201-i) * 86400000)
		}
		got, err := parseDailyKlinePage("BTCUSDT", 202*86400000, klineResult{"linear", "BTCUSDT", list})
		if size <= 200 && (err != nil || got == nil || len(got) != size) || size > 200 && (err == nil || got != nil) {
			t.Fatalf("size %d: %v, %v", size, got, err)
		}
	}
}

// TestParseDailyKlinePageInvalid проверяет отказ без частичного результата.
func TestParseDailyKlinePageInvalid(t *testing.T) {
	for _, rows := range [][][]string{nil, {dailyPageRow(86400000)}} {
		for _, symbol := range []string{"", "btcusdt", " BTCUSDT", "BTCUSDT "} {
			got, err := parseDailyKlinePage(symbol, 86400000, klineResult{"linear", "BTCUSDT", rows})
			if got != nil || !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("symbol %q: %v, %v", symbol, got, err)
			}
		}
		for _, identity := range [][2]string{{"", "BTCUSDT"}, {"spot", "BTCUSDT"}, {"linear", ""}, {"linear", "ETHUSDT"}} {
			got, err := parseDailyKlinePage("BTCUSDT", 86400000, klineResult{identity[0], identity[1], rows})
			if err == nil || got != nil {
				t.Fatalf("identity %v: %v, %v", identity, got, err)
			}
		}
		for _, serverMs := range []int64{0, -1} {
			got, err := parseDailyKlinePage("BTCUSDT", serverMs, klineResult{"linear", "BTCUSDT", rows})
			if err == nil || got != nil {
				t.Fatalf("server %d: %v, %v", serverMs, got, err)
			}
		}
	}
	mutations := map[int][]string{
		0: {"bad", "0", "-86400000", "9223372036854775808", "86400001", "259200000", "345600000"},
		1: {"bad", "0", "-1"},
		2: {"bad", "0", "-1"},
		3: {"bad", "0", "-1"},
		4: {"bad", "0", "-1"},
		5: {"bad", "-1"},
		6: {"bad", "-1"},
	}
	for field, values := range mutations {
		for _, value := range values {
			row := dailyPageRow(2 * 86400000)
			row[field] = value
			rows := [][]string{dailyPageRow(3 * 86400000), row}
			before := append([]string(nil), row...)
			got, err := parseDailyKlinePage("BTCUSDT", 4*86400000, klineResult{"linear", "BTCUSDT", rows})
			if err == nil || got != nil {
				t.Fatalf("field %d=%q: %v, %v", field, value, got, err)
			}
			if !reflect.DeepEqual(row, before) {
				t.Fatalf("field %d=%q: input mutated", field, value)
			}
		}
	}
	for _, row := range [][]string{dailyPageRow(86400000)[:6], append(dailyPageRow(86400000), "extra")} {
		got, err := parseDailyKlinePage("BTCUSDT", 4*86400000, klineResult{"linear", "BTCUSDT", [][]string{dailyPageRow(3 * 86400000), row}})
		if err == nil || got != nil {
			t.Fatalf("width %d: %v, %v", len(row), got, err)
		}
	}
}

// TestDailyKlineConfirmed проверяет границы суток без переполнения int64.
func TestDailyKlineConfirmed(t *testing.T) {
	const day int64 = 86400000
	const lastMidnight = math.MaxInt64 / day * day
	for _, tc := range []struct {
		name         string
		open, server int64
		confirmed    bool
	}{
		{"before close", day, 2*day - 1, false},
		{"at close", day, 2 * day, true},
		{"after close", day, 2*day + 1, true},
		{"current day", day, day + 1, false},
		{"future", 2 * day, day, false},
		{"max midnight", lastMidnight, math.MaxInt64, false},
		{"previous max day", lastMidnight - day, math.MaxInt64, true},
	} {
		got, err := parseDailyKlinePage("BTCUSDT", tc.server, klineResult{"linear", "BTCUSDT", [][]string{dailyPageRow(tc.open)}})
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: %v, %v", tc.name, got, err)
		}
		if got[0].Confirmed != tc.confirmed {
			t.Fatalf("%s Confirmed = %v", tc.name, got[0].Confirmed)
		}
	}
}
