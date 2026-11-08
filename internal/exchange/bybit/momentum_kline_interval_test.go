package bybit

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// TestMomentumKlineIntervals проверяет интервал, UTC и точность всех полей.
func TestMomentumKlineIntervals(t *testing.T) {
	for _, tc := range []struct {
		interval string
		step     int64
	}{{"D", 86400000}, {"60", 3600000}} {
		rows := [][]string{dailyPageRow(3 * tc.step), dailyPageRow(2 * tc.step)}
		rows[1][5], rows[1][6] = "9007199254740993.00000001", "0.000000000000000003"
		before := [][]string{append([]string(nil), rows[0]...), append([]string(nil), rows[1]...)}
		got, err := parseMomentumKlinePage("BTCUSDT", tc.interval, 4*tc.step, klineResult{"linear", "BTCUSDT", rows})
		if err != nil || len(got) != 2 {
			t.Fatalf("%s: %v, %v", tc.interval, got, err)
		}
		for i, candle := range got {
			if candle.IntervalCode != tc.interval || candle.Symbol != "BTCUSDT" || !candle.Confirmed ||
				candle.OpenTime != time.UnixMilli(int64(3-i)*tc.step).UTC() || candle.OpenTime.Location() != time.UTC {
				t.Fatalf("%s candle %d: %+v", tc.interval, i, candle)
			}
			for j, actual := range []model.Decimal{candle.Open, candle.High, candle.Low, candle.Close, candle.Volume, candle.Turnover} {
				want, parseErr := model.ParseAmount(rows[i][j+1])
				cmp, cmpErr := actual.Cmp(want)
				if parseErr != nil || cmpErr != nil || cmp != 0 {
					t.Fatalf("%s field %d: %v, %v", tc.interval, j, parseErr, cmpErr)
				}
			}
		}
		if !reflect.DeepEqual(rows, before) {
			t.Fatal("input mutated")
		}
	}
}

// TestMomentumHourlyBounds проверяет точную границу часа и переполнение.
func TestMomentumHourlyBounds(t *testing.T) {
	const hour int64 = 3600000
	const lastHour = math.MaxInt64 / hour * hour
	for _, tc := range []struct {
		open, server int64
		confirmed    bool
	}{{hour, 2*hour - 1, false}, {hour, 2 * hour, true}, {hour, 2*hour + 1, true},
		{hour, hour + 1, false}, {2 * hour, hour, false}, {lastHour, math.MaxInt64, false},
		{lastHour - hour, math.MaxInt64, true}} {
		got, err := parseMomentumKlinePage("BTCUSDT", "60", tc.server, klineResult{"linear", "BTCUSDT", [][]string{dailyPageRow(tc.open)}})
		if err != nil || len(got) != 1 || got[0].Confirmed != tc.confirmed {
			t.Fatalf("%+v: %v, %v", tc, got, err)
		}
	}
}

// TestMomentumHourlyRejects проверяет selector и специфичные часовые границы.
func TestMomentumHourlyRejects(t *testing.T) {
	for _, interval := range []string{"", "1", "d", " 60", "60 ", "W"} {
		got, err := parseMomentumKlinePage("BTCUSDT", interval, 7200000, klineResult{"linear", "BTCUSDT", nil})
		if got != nil || !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("interval %q: %v, %v", interval, got, err)
		}
	}
	for _, value := range []string{"0", "-3600000", "3600001", "bad", "9223372036854775808", "10800000", "14400000"} {
		row := dailyPageRow(7200000)
		row[0] = value
		got, err := parseMomentumKlinePage("BTCUSDT", "60", 14400000, klineResult{"linear", "BTCUSDT", [][]string{dailyPageRow(10800000), row}})
		if err == nil || got != nil {
			t.Fatalf("timestamp %q: %v, %v", value, got, err)
		}
	}
	for _, size := range []int{0, 200, 201} {
		rows := make([][]string, size)
		for i := range rows {
			rows[i] = dailyPageRow(int64(201-i) * 3600000)
		}
		got, err := parseMomentumKlinePage("BTCUSDT", "60", 202*3600000, klineResult{"linear", "BTCUSDT", rows})
		if size <= 200 && (err != nil || got == nil || len(got) != size) || size > 200 && (err == nil || got != nil) {
			t.Fatalf("size %s: %v, %v", strconv.Itoa(size), got, err)
		}
	}
}

// TestMomentumHourlyFields проверяет общий decimal parser на часовом пути.
func TestMomentumHourlyFields(t *testing.T) {
	for field, values := range map[int][]string{
		1: {"bad", "0", "-1", "NaN", "Infinity", "1" + strings.Repeat("0", 128)},
		2: {"bad", "0", "-1", "NaN", "Infinity"}, 3: {"bad", "0", "-1", "NaN", "Infinity"},
		4: {"bad", "0", "-1", "NaN", "Infinity"}, 5: {"bad", "-1", "NaN", "Infinity"},
		6: {"bad", "-1", "NaN", "Infinity"},
	} {
		for _, value := range values {
			row := dailyPageRow(3600000)
			row[field] = value
			before := append([]string(nil), row...)
			got, err := parseMomentumKlinePage("BTCUSDT", "60", 10800000, klineResult{"linear", "BTCUSDT", [][]string{dailyPageRow(7200000), row}})
			if err == nil || got != nil || !reflect.DeepEqual(row, before) {
				t.Fatalf("field %d=%q: %v, %v", field, value, got, err)
			}
		}
	}
}

// TestMomentumHourlyRowWidth проверяет ширину каждой часовой строки.
func TestMomentumHourlyRowWidth(t *testing.T) {
	for _, row := range [][]string{dailyPageRow(3600000)[:6], append(dailyPageRow(3600000), "extra")} {
		got, err := parseMomentumKlinePage("BTCUSDT", "60", 7200000,
			klineResult{"linear", "BTCUSDT", [][]string{row}})
		if err == nil || got != nil {
			t.Fatalf("width %d: %v, %v", len(row), got, err)
		}
	}
}
