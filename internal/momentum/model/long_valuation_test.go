package model

import (
	"errors"
	"math/big"
	"testing"
	"time"
)

// valuationDecimal разбирает точное значение для теста переоценки.
func valuationDecimal(t *testing.T, amount string) Decimal {
	t.Helper()
	value, err := ParseAmount(amount)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// valuationAt строит фиксированное время UTC для тестов.
func valuationAt() time.Time {
	return time.Date(2026, 11, 19, 9, 0, 0, 123456789, time.UTC)
}

// valuationInput возвращает корректную позицию, наблюдение и asOf.
func valuationInput(t *testing.T, quantity, basis, price string) (LongPosition, LongMarkObservation, time.Time) {
	t.Helper()
	at := valuationAt()
	position := LongPosition{Quantity: valuationDecimal(t, quantity), CostBasisUSDT: valuationDecimal(t, basis)}
	mark := LongMarkObservation{Symbol: "BTCUSDT", ObservedAt: at, Price: valuationDecimal(t, price)}
	return position, mark, at
}

// requireValuationRatio проверяет рациональное значение без округления.
func requireValuationRatio(t *testing.T, value Decimal, want string) {
	t.Helper()
	numerator, denominator, err := value.CanonicalRatio()
	if err != nil || numerator+"/"+denominator != want {
		t.Fatalf("got %s/%s, %v; want %s", numerator, denominator, err, want)
	}
}

// TestProjectLongValuationScenarios проверяет прибыль, убыток, ноль и flat.
func TestProjectLongValuationScenarios(t *testing.T) {
	for _, test := range []struct {
		name, quantity, basis, price, notional, pnl string
	}{
		{"profit", "2", "200", "110", "220/1", "20/1"},
		{"loss", "2", "200", "90", "180/1", "-20/1"},
		{"breakeven", "2", "200", "100", "200/1", "0/1"},
		{"flat", "0", "0", "100", "0/1", "0/1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			position, mark, asOf := valuationInput(t, test.quantity, test.basis, test.price)
			got, err := ProjectLongValuation(position, mark, asOf, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			requireValuationRatio(t, got.NotionalUSDT, test.notional)
			requireValuationRatio(t, got.UnrealizedPnLUSDT, test.pnl)
			if got.Position != position || got.Mark.Symbol != mark.Symbol || got.Mark.Price.value.Cmp(mark.Price.value) != 0 {
				t.Fatal("valuation changed supplied position or mark")
			}
		})
	}
}

// TestProjectLongValuationExactness проверяет рациональную арифметику и повторяемость.
func TestProjectLongValuationExactness(t *testing.T) {
	position, mark, asOf := valuationInput(t, "0.1", "0.02", "0.3")
	first, err := ProjectLongValuation(position, mark, asOf, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	requireValuationRatio(t, first.NotionalUSDT, "3/100")
	requireValuationRatio(t, first.UnrealizedPnLUSDT, "1/100")
	second, err := ProjectLongValuation(position, mark, asOf, time.Second)
	if err != nil || first.NotionalUSDT.value.Cmp(second.NotionalUSDT.value) != 0 || first.UnrealizedPnLUSDT.value.Cmp(second.UnrealizedPnLUSDT.value) != 0 {
		t.Fatalf("repeat differs: %#v %v", second, err)
	}

	third := Decimal{value: new(big.Rat).SetFrac(big.NewInt(1), big.NewInt(3))}
	position = LongPosition{Quantity: valuationDecimal(t, "1"), CostBasisUSDT: third}
	mark = LongMarkObservation{Symbol: "BTCUSDT", ObservedAt: valuationAt(), Price: third}
	got, err := ProjectLongValuation(position, mark, mark.ObservedAt, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	requireValuationRatio(t, got.NotionalUSDT, "1/3")
	requireValuationRatio(t, got.UnrealizedPnLUSDT, "0/1")

	position, mark, asOf = valuationInput(t, "9007199254740993", "1", "3")
	got, err = ProjectLongValuation(position, mark, asOf, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	requireValuationRatio(t, got.NotionalUSDT, "27021597764222979/1")
}

// TestProjectLongValuationTimeNormalization проверяет UTC, точную границу возраста и наносекунды.
func TestProjectLongValuationTimeNormalization(t *testing.T) {
	position, mark, asOf := valuationInput(t, "1", "1", "1")
	zone := time.FixedZone("offset", 3*60*60)
	mark.ObservedAt = mark.ObservedAt.In(zone)
	asOf = asOf.Add(time.Second).In(zone)
	originalMark, originalAsOf := mark, asOf
	got, err := ProjectLongValuation(position, mark, asOf, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mark.ObservedAt.Location() != time.UTC || got.AsOf.Location() != time.UTC || got.Mark.ObservedAt.Nanosecond() != 123456789 {
		t.Fatalf("times not normalized precisely: %#v", got)
	}
	if mark != originalMark || asOf != originalAsOf {
		t.Fatal("valuation mutated mark or asOf input")
	}
	if _, err := ProjectLongValuation(position, mark, asOf.Add(time.Nanosecond), time.Second); !errors.Is(err, ErrLongMarkStale) {
		t.Fatalf("expected stale just beyond boundary, got %v", err)
	}
	if _, err := ProjectLongValuation(position, mark, asOf.Add(-2*time.Second), time.Second); !errors.Is(err, ErrInvalidLongValuation) {
		t.Fatalf("expected future observation rejection, got %v", err)
	}
	old := mark.ObservedAt.AddDate(-1000, 0, 0)
	mark.ObservedAt = old
	if _, err := ProjectLongValuation(position, mark, asOf, time.Duration(1<<63-1)); !errors.Is(err, ErrLongMarkStale) {
		t.Fatalf("age beyond maxDuration must remain stale: %v", err)
	}
	if _, err := ProjectLongValuation(position, mark, asOf, time.Second); !errors.Is(err, ErrLongMarkStale) {
		t.Fatalf("large age bypassed short maxAge: %v", err)
	}
}

// TestProjectLongValuationInvalidInputs проверяет нулевой результат для неверных аргументов и позиции.
func TestProjectLongValuationInvalidInputs(t *testing.T) {
	position, mark, asOf := valuationInput(t, "1", "2", "3")
	for name, mutate := range map[string]func(*LongPosition, *LongMarkObservation, *time.Time, *time.Duration){
		"symbol-empty":  func(_ *LongPosition, m *LongMarkObservation, _ *time.Time, _ *time.Duration) { m.Symbol = "" },
		"symbol-space":  func(_ *LongPosition, m *LongMarkObservation, _ *time.Time, _ *time.Duration) { m.Symbol = " BTCUSDT" },
		"price-zero":    func(_ *LongPosition, m *LongMarkObservation, _ *time.Time, _ *time.Duration) { m.Price = Zero() },
		"price-invalid": func(_ *LongPosition, m *LongMarkObservation, _ *time.Time, _ *time.Duration) { m.Price = Decimal{} },
		"mark-time": func(_ *LongPosition, m *LongMarkObservation, _ *time.Time, _ *time.Duration) {
			m.ObservedAt = time.Time{}
		},
		"as-of":            func(_ *LongPosition, _ *LongMarkObservation, a *time.Time, _ *time.Duration) { *a = time.Time{} },
		"max-age-zero":     func(_ *LongPosition, _ *LongMarkObservation, _ *time.Time, d *time.Duration) { *d = 0 },
		"max-age-negative": func(_ *LongPosition, _ *LongMarkObservation, _ *time.Time, d *time.Duration) { *d = -time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			p, m, a, age := position, mark, asOf, time.Second
			mutate(&p, &m, &a, &age)
			got, err := ProjectLongValuation(p, m, a, age)
			if !errors.Is(err, ErrInvalidLongValuation) || got != (LongValuation{}) {
				t.Fatalf("expected invalid input and zero result, got %#v %v", got, err)
			}
		})
	}
	for _, invalid := range []LongPosition{
		{Quantity: Decimal{}, CostBasisUSDT: Zero()},
		{Quantity: valuationDecimal(t, "1"), CostBasisUSDT: Zero()},
		{Quantity: Zero(), CostBasisUSDT: valuationDecimal(t, "1")},
		{Quantity: valuationDecimal(t, "-1"), CostBasisUSDT: valuationDecimal(t, "-1")},
	} {
		got, err := ProjectLongValuation(invalid, mark, asOf, time.Second)
		if !errors.Is(err, ErrInvalidLedgerPosition) || got != (LongValuation{}) {
			t.Fatalf("expected invalid position and zero result, got %#v %v", got, err)
		}
	}
}

// TestProjectLongValuationArithmeticLimits проверяет отдельные ошибки Mul и Sub.
func TestProjectLongValuationArithmeticLimits(t *testing.T) {
	large := Decimal{value: new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), maxDecimalBits/2+1))}
	mark := LongMarkObservation{Symbol: "BTCUSDT", ObservedAt: valuationAt(), Price: large}
	position := LongPosition{Quantity: large, CostBasisUSDT: valuationDecimal(t, "1")}
	got, err := ProjectLongValuation(position, mark, mark.ObservedAt, time.Second)
	if !errors.Is(err, ErrLongValuationArithmetic) || got != (LongValuation{}) {
		t.Fatalf("expected Mul overflow and zero result, got %#v %v", got, err)
	}

	denomA := new(big.Int).Lsh(big.NewInt(1), maxDecimalBits/2)
	denomB := new(big.Int).Exp(big.NewInt(3), big.NewInt(maxDecimalBits/2), nil)
	small := Decimal{value: new(big.Rat).SetFrac(big.NewInt(1), denomA)}
	basis := Decimal{value: new(big.Rat).SetFrac(big.NewInt(1), denomB)}
	mark.Price = small
	position = LongPosition{Quantity: valuationDecimal(t, "1"), CostBasisUSDT: basis}
	got, err = ProjectLongValuation(position, mark, mark.ObservedAt, time.Second)
	if !errors.Is(err, ErrLongValuationArithmetic) || got != (LongValuation{}) {
		t.Fatalf("expected Sub overflow and zero result, got %#v %v", got, err)
	}
}
