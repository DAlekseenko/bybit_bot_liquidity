package model

import (
	"strings"
	"testing"
)

// decimalTest разбирает fixture и завершает тест при ошибке.
func decimalTest(t *testing.T, s string) Decimal {
	t.Helper()
	d, err := ParseAmount(s)
	if err != nil {
		t.Fatalf("ParseAmount(%q): %v", s, err)
	}
	return d
}

// checkTestError проверяет, завершилась ли операция ожидаемой ошибкой.
func checkTestError(t *testing.T, err error, wantError bool) {
	t.Helper()
	if (err != nil) != wantError {
		t.Fatalf("error=%v, wantError=%t", err, wantError)
	}
}

// TestDecimalParsing проверяет строгую грамматику, пределы и знаковые домены.
func TestDecimalParsing(t *testing.T) {
	for _, s := range []string{"0", "-0", "1", "-1.25", "0.1000", "1." + strings.Repeat("0", 64), strings.Repeat("1", 63) + "." + strings.Repeat("1", 64)} {
		if _, err := ParseAmount(s); err != nil {
			t.Errorf("ParseAmount(%q): %v", s, err)
		}
	}
	for _, s := range []string{"", "-", "+1", ".5", "1.", "01", "-01", " 1", "1 ", "1e2", "NaN", "Inf", "1.2.3", "١"} {
		if _, err := ParseAmount(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	for _, s := range []string{strings.Repeat("1", 129), "0." + strings.Repeat("1", 65)} {
		_, err := ParseAmount(s)
		checkTestError(t, err, true)
	}
	for _, s := range []string{"0", "-1"} {
		_, err := ParsePrice(s)
		checkTestError(t, err, true)
	}
	_, err := ParseQty("-0.1")
	checkTestError(t, err, true)
	_, err = ParseRate("-0.01")
	checkTestError(t, err, false)
}

// TestDecimalArithmeticSerialization проверяет точность, wire и неизменяемость operands.
func TestDecimalArithmeticSerialization(t *testing.T) {
	third, err := decimalTest(t, "1").Quo(decimalTest(t, "3"))
	checkTestError(t, err, false)
	n, den, err := third.CanonicalRatio()
	if err != nil || n != "1" || den != "3" {
		t.Fatalf("ratio = %s/%s, %v", n, den, err)
	}
	_, err = third.WireDecimal()
	checkTestError(t, err, true)
	_, err = third.MarshalText()
	checkTestError(t, err, true)
	_, err = third.MarshalJSON()
	checkTestError(t, err, true)
	for input, want := range map[string]string{"1/8": "0.125", "0.1000": "0.1", "-0": "0"} {
		d, err := ParseAmount(input)
		if input == "1/8" {
			d, err = decimalTest(t, "1").Quo(decimalTest(t, "8"))
		}
		checkTestError(t, err, false)
		got, err := d.WireDecimal()
		if err != nil || got != want {
			t.Errorf("%s wire = %q, %v; want %q", input, got, err, want)
		}
		b, err := d.MarshalJSON()
		if err != nil || string(b) != `"`+want+`"` {
			t.Errorf("JSON = %s, %v", b, err)
		}
	}
	_, err = (Decimal{}).WireDecimal()
	checkTestError(t, err, true)
	_, err = decimalTest(t, "1").Quo(Zero())
	checkTestError(t, err, true)
	left, right := decimalTest(t, "1.20"), decimalTest(t, "3")
	_, err = left.Mul(right)
	checkTestError(t, err, false)
	got, _ := left.WireDecimal()
	if got != "1.2" {
		t.Fatalf("operand mutated: %s", got)
	}
}

// TestFloorQtyAndMinNotional проверяет округление шага и границы notional.
func TestFloorQtyAndMinNotional(t *testing.T) {
	for _, tc := range []struct{ raw, step, want string }{{"1.2349", "0.005", "1.23"}, {"0.0049", "0.005", "0"}, {"0.299", "0.01", "0.29"}} {
		got, err := FloorQty(decimalTest(t, tc.raw), decimalTest(t, tc.step))
		checkTestError(t, err, false)
		s, err := got.WireDecimal()
		if err != nil || s != tc.want {
			t.Errorf("floor %s/%s = %s, %v", tc.raw, tc.step, s, err)
		}
	}
	qty, err := FloorQty(decimalTest(t, "1.239"), decimalTest(t, "0.01"))
	checkTestError(t, err, false)
	for min, want := range map[string]bool{"123": true, "123.0001": false, "0": true} {
		ok, err := MeetsMinNotional(qty, decimalTest(t, "100"), decimalTest(t, min))
		if err != nil || ok != want {
			t.Errorf("min %s: got %v, %v", min, ok, err)
		}
	}
	for _, tc := range [][2]string{{"-1", "0.1"}, {"1", "0"}, {"1", "-0.1"}} {
		_, err := FloorQty(decimalTest(t, tc[0]), decimalTest(t, tc[1]))
		checkTestError(t, err, true)
	}
	base, large := decimalTest(t, "1"+strings.Repeat("0", 120)), decimalTest(t, "1")
	for i := 0; i < 10; i++ {
		large, err = large.Mul(base)
		checkTestError(t, err, false)
	}
	floored, err := FloorQty(large, decimalTest(t, "0."+strings.Repeat("0", 63)+"1"))
	checkTestError(t, err, false)
	if cmp, err := floored.Cmp(large); err != nil || cmp != 0 {
		t.Fatalf("large floor differs: cmp=%d err=%v", cmp, err)
	}
	for _, tc := range [][3]string{{"1", "-1", "0"}, {"-1", "1", "0"}, {"0", "1", "-1"}} {
		_, err := MeetsMinNotional(decimalTest(t, tc[0]), decimalTest(t, tc[1]), decimalTest(t, tc[2]))
		checkTestError(t, err, true)
	}
}

// TestDecimalBitLimit проверяет отказ при превышении ограничения результата.
func TestDecimalBitLimit(t *testing.T) {
	tiny := decimalTest(t, "0."+strings.Repeat("0", 63)+"1")
	d := decimalTest(t, "1")
	var err error
	for i := 0; i < 20; i++ {
		d, err = d.Quo(tiny)
		if err != nil {
			return
		}
	}
	t.Fatal("oversized rational accepted")
}
