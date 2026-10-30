package planner

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"bybit_bot_liquidity/internal/momentum/model"
)

// sizingDecimal создаёт точное значение, включая рациональные fixtures.
func sizingDecimal(t *testing.T, s string) model.Decimal {
	t.Helper()
	parts := strings.Split(s, "/")
	d, err := model.ParseAmount(parts[0])
	if len(parts) == 2 && err == nil {
		d, err = d.Quo(sizingDecimal(t, parts[1]))
	}
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// sizingRat копирует фактическое рациональное значение без общих указателей.
func sizingRat(t *testing.T, d model.Decimal) *big.Rat {
	t.Helper()
	n, den, err := d.CanonicalRatio()
	r, ok := new(big.Rat).SetString(n + "/" + den)
	if err != nil || !ok {
		t.Fatalf("invalid ratio: %v", err)
	}
	return r
}

// TestBaselineQuantity проверяет Qty, максимальность и неизменность входов.
func TestBaselineQuantity(t *testing.T) {
	cases := [][6]string{
		{"3000", "100", "0", "0.001", "0.001", "29.940"},
		{"3000", "100", "10", "0.001", "0.001", "29.950"},
		{"3000", "100", "40", "0.001", "0.001", "29.960"},
		{"3003", "100", "30", "0.001", "0.001", "30"},
		{"3002.9", "100", "30", "0.001", "0.001", "29.999"},
		{"3003.1", "100", "30", "0.001", "0.001", "30"},
		{"3000", "100", "40", "0", "0.007", "29.995"},
		{"3000", "100", "0", "2", "0.001", "6"},
		{"1/3", "1", "0", "0", "0.001", "0.333"},
		{"1", "3", "0", "0", "0.001", "0.333"},
		{"3000", "100", "0", "0.001", "0.007", "29.939"},
		{"3000", "100", "0", "0.001", "100", "0"},
		{"3000", "100", "10.0005", "0.001", "0.001", "29.950"},
		{"100000000000000001", "1", "0", "0", "1", "100000000000000001"},
		{"0", "100", "0", "0.001", "0.001", "0"},
		{"0", "100", "40", "0", "0.001", "0"},
		{"4", "100", "40", "0.001", "0.001", "0"},
		{"3000", "100", "30000", "0.001", "0.001", "0"},
	}
	for _, tc := range cases {
		var args [5]model.Decimal
		var before [5]*big.Rat
		for i := range args {
			args[i] = sizingDecimal(t, tc[i])
			before[i] = sizingRat(t, args[i])
		}
		qty, err := BaselineQuantity(args[0], args[1], args[2], args[3], args[4])
		repeat, repeatErr := BaselineQuantity(args[0], args[1], args[2], args[3], args[4])
		floor, floorErr := model.FloorQty(qty, args[4])
		if err != nil || repeatErr != nil || floorErr != nil || sizingRat(t, qty).Cmp(sizingRat(t, sizingDecimal(t, tc[5]))) != 0 || sizingRat(t, repeat).Cmp(sizingRat(t, qty)) != 0 || sizingRat(t, floor).Cmp(sizingRat(t, qty)) != 0 {
			t.Fatalf("case=%v err=%v repeat=%v floor=%v", tc, err, repeatErr, floorErr)
		}
		// cost оценивает исходное неравенство независимо от кусочного алгоритма.
		cost := func(q *big.Rat) *big.Rat {
			delta := new(big.Rat).Abs(new(big.Rat).Sub(q, before[2]))
			fees := new(big.Rat).Mul(before[3], new(big.Rat).Add(delta, q))
			return new(big.Rat).Mul(before[1], new(big.Rat).Add(q, fees))
		}
		q := sizingRat(t, qty)
		if cost(q).Cmp(before[0]) > 0 || cost(new(big.Rat).Add(q, before[4])).Cmp(before[0]) <= 0 {
			t.Fatalf("not maximal: %v", tc)
		}
		for i, arg := range args {
			if sizingRat(t, arg).Cmp(before[i]) != 0 {
				t.Fatalf("mutated operand %d", i)
			}
		}
	}
}

// TestBaselineQuantityErrors проверяет причины, overflow и неизменность valid входов.
func TestBaselineQuantityErrors(t *testing.T) {
	base := [5]model.Decimal{sizingDecimal(t, "3000"), sizingDecimal(t, "100"), sizingDecimal(t, "40"), sizingDecimal(t, "0.001"), sizingDecimal(t, "0.001")}
	type failure struct {
		args [5]model.Decimal
		want error
	}
	cases := []failure{}
	for i := range base {
		for _, invalid := range []model.Decimal{{}, sizingDecimal(t, "-1")} {
			args := base
			args[i] = invalid
			cases = append(cases, failure{args, ReasonInvalidSizingInput})
		}
	}
	for _, edit := range []struct {
		index int
		value string
	}{{1, "0"}, {4, "0"}, {4, "1/3"}} {
		args := base
		args[edit.index] = sizingDecimal(t, edit.value)
		cases = append(cases, failure{args, ReasonInvalidSizingInput})
	}
	for _, budget := range []string{"3.999", "0"} {
		args := base
		args[0] = sizingDecimal(t, budget)
		cases = append(cases, failure{args, ReasonInfeasibleReserve})
	}
	large, factor := sizingDecimal(t, "1"), sizingDecimal(t, "1"+strings.Repeat("0", 100))
	for i := 0; i < 12; i++ {
		var err error
		if large, err = large.Mul(factor); err != nil {
			t.Fatal(err)
		}
	}
	price := sizingDecimal(t, "0."+strings.Repeat("0", 63)+"1")
	_, overflow := large.Quo(price)
	if overflow == nil {
		t.Fatal("overflow fixture must fail")
	}
	cases = append(cases, failure{[5]model.Decimal{large, price, base[2], base[3], base[4]}, overflow})
	for i, tc := range cases {
		args, want := tc.args, tc.want
		before := make(map[int]*big.Rat)
		for j, arg := range args {
			if _, _, err := arg.CanonicalRatio(); err == nil {
				before[j] = sizingRat(t, arg)
			}
		}
		qty, err := BaselineQuantity(args[0], args[1], args[2], args[3], args[4])
		var reason BaselineSizingReason
		if qty != (model.Decimal{}) || err != want || !errors.Is(err, want) || (want != overflow && (!errors.As(err, &reason) || reason != want || err.Error() != string(reason))) {
			t.Fatalf("case=%d qty=%v err=%v want=%v", i, qty, err, want)
		}
		for j, value := range before {
			if sizingRat(t, args[j]).Cmp(value) != 0 {
				t.Fatalf("error mutated operand %d", j)
			}
		}
	}
}
