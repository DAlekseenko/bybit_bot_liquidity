package model

import (
	"math/big"
	"testing"
)

// decimalRatioTest создаёт точный fixture, включая большие дроби.
func decimalRatioTest(t *testing.T, s string) Decimal {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("invalid fixture %q", s)
	}
	return Decimal{r}
}

// assertDecimalRatio проверяет канонические числитель и знаменатель.
func assertDecimalRatio(t *testing.T, got Decimal, want string) {
	t.Helper()
	w := decimalRatioTest(t, want)
	n, d, err := got.CanonicalRatio()
	if err != nil || n != w.value.Num().String() || d != w.value.Denom().String() {
		t.Fatalf("ratio=%s/%s, want %s: %v", n, d, want, err)
	}
}

// TestDecimalAddSubExact проверяет точность, границы и неизменность копий.
func TestDecimalAddSubExact(t *testing.T) {
	boundary := new(big.Int).Lsh(big.NewInt(1), maxDecimalBits-1).String()
	for _, tc := range []struct{ a, b, add, sub string }{
		{"0.1", "0.2", "3/10", "-1/10"},
		{"0.3", "0.1", "2/5", "1/5"},
		{"1", "-2", "-1", "3"},
		{"-2", "1", "-1", "-3"},
		{"-2", "-3", "-5", "1"},
		{"-3", "-3", "-6", "0/1"},
		{"0", "2", "2", "-2"},
		{"2", "0", "2", "2"},
		{"-3", "3", "0/1", "-6"},
		{"18014398509481984", "1", "18014398509481985", "18014398509481983"},
		{"1/3", "1/6", "1/2", "1/6"},
		{"1/3", "1/3", "2/3", "0/1"},
		{"1/6", "1/3", "1/2", "-1/6"},
		{boundary, "0", boundary, boundary},
		{"1/" + boundary, "0", "1/" + boundary, "1/" + boundary},
		{boundary, "-" + boundary, "0/1", "error"},
		{boundary, boundary, "error", "0/1"},
		{"-" + boundary, "-" + boundary, "error", "0/1"},
		{"-" + boundary, boundary, "0/1", "error"},
	} {
		a, b := decimalRatioTest(t, tc.a), decimalRatioTest(t, tc.b)
		ac, bc := a, b
		for i, op := range []func(Decimal, Decimal) (Decimal, error){Decimal.Add, Decimal.Sub} {
			want := []string{tc.add, tc.sub}[i]
			got, err := op(a, b)
			if want == "error" {
				if err != errDecimal || got.value != nil {
					t.Fatalf("op %d: numerator overflow accepted", i)
				}
			} else {
				if err != nil {
					t.Fatalf("op %d: %v", i, err)
				}
				assertDecimalRatio(t, got, want)
			}
			for _, saved := range []Decimal{a, ac} {
				assertDecimalRatio(t, saved, tc.a)
			}
			for _, saved := range []Decimal{b, bc} {
				assertDecimalRatio(t, saved, tc.b)
			}
		}
	}
}

// TestDecimalAddSubErrors проверяет invalid с обеих сторон и размер знаменателя.
func TestDecimalAddSubErrors(t *testing.T) {
	oversized := new(big.Int).Lsh(big.NewInt(1), maxDecimalBits)
	badNum := Decimal{new(big.Rat).SetInt(oversized)}
	badDen := Decimal{new(big.Rat).SetFrac(big.NewInt(1), oversized)}
	base := new(big.Int).Lsh(big.NewInt(1), maxDecimalBits/2+1)
	a := Decimal{new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Sub(base, big.NewInt(1)))}
	b := Decimal{new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Add(base, big.NewInt(1)))}
	for _, op := range []func(Decimal, Decimal) (Decimal, error){Decimal.Add, Decimal.Sub} {
		pairs := [][2]Decimal{{a, b}}
		for _, invalid := range []Decimal{{}, badNum, badDen} {
			pairs = append(pairs, [2]Decimal{invalid, Zero()}, [2]Decimal{Zero(), invalid})
		}
		for _, pair := range pairs {
			got, err := op(pair[0], pair[1])
			if err != errDecimal || got.value != nil {
				t.Fatal("expected exact errDecimal and Decimal{}")
			}
		}
	}
}

// TestDecimalAddSubAliasing проверяет цепочки и мутации результата и операндов.
func TestDecimalAddSubAliasing(t *testing.T) {
	for i, op := range []func(Decimal, Decimal) (Decimal, error){Decimal.Add, Decimal.Sub} {
		a, b := decimalRatioTest(t, "1/3"), decimalRatioTest(t, "1/6")
		ac, bc := a, b
		a0, b0 := new(big.Rat).Set(a.value), new(big.Rat).Set(b.value)
		first, err := op(a, b)
		if err != nil {
			t.Fatal(err)
		}
		second, err := op(first, bc)
		if err != nil {
			t.Fatal(err)
		}
		firstWant, secondWant := []string{"1/2", "1/6"}[i], []string{"2/3", "0/1"}[i]
		assertDecimalRatio(t, first, firstWant)
		assertDecimalRatio(t, second, secondWant)
		for _, saved := range []Decimal{a, ac} {
			assertDecimalRatio(t, saved, a0.RatString())
		}
		for _, saved := range []Decimal{b, bc} {
			assertDecimalRatio(t, saved, b0.RatString())
		}
		first.value.SetInt64(7)
		for _, saved := range []Decimal{a, ac} {
			assertDecimalRatio(t, saved, a0.RatString())
		}
		for _, saved := range []Decimal{b, bc} {
			assertDecimalRatio(t, saved, b0.RatString())
		}
		assertDecimalRatio(t, second, secondWant)
		for _, operand := range []Decimal{a, b} {
			operand.value.SetInt64(8)
			assertDecimalRatio(t, first, "7")
			assertDecimalRatio(t, second, secondWant)
		}
	}
}
