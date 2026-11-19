package model

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fundingAmount разбирает точное значение для теста.
func fundingAmount(t *testing.T, raw string) Decimal {
	t.Helper()
	d, err := ParseAmount(raw)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// fundingFixture задаёт фактическую позицию и событие вне сетки 8 часов.
func fundingFixture(t *testing.T) (LongPosition, LongFundingAssessment) {
	t.Helper()
	return LongPosition{fundingAmount(t, "10"), fundingAmount(t, "70000")},
		LongFundingAssessment{"funding-1", "BTCUSDT",
			FundingEvent{time.Date(2026, 11, 18, 5, 13, 7, 123456789, time.FixedZone("east", 10800)), fundingAmount(t, "0.0001")},
			fundingAmount(t, "8000")}
}

// TestProjectLongFunding проверяет знак, точность, UTC и сохранность позиции.
func TestProjectLongFunding(t *testing.T) {
	p, a := fundingFixture(t)
	for _, tc := range []struct{ name, rate, quantity, basis, want string }{
		{"payment", "0.0001", "10", "70000", "-8"},
		{"receipt", "-0.0001", "10", "70000", "8"},
		{"zero-rate", "0", "10", "70000", "0"},
		{"zero-position", "0.0001", "0", "0", "0"},
		{"precision", "0.00000000000000000001", "9007199254740993", "1", "-0.72057594037927944"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			position, input := p, a
			input.Event.Rate = fundingAmount(t, tc.rate)
			position.Quantity, position.CostBasisUSDT = fundingAmount(t, tc.quantity), fundingAmount(t, tc.basis)
			originalPosition, originalInput := position, input
			got, err := ProjectLongFunding(position, input)
			if err != nil {
				t.Fatal(err)
			}
			cmp, err := got.CashDeltaUSDT.Cmp(fundingAmount(t, tc.want))
			if err != nil || cmp != 0 {
				t.Fatalf("cash mismatch: %+v %v", got, err)
			}
			expectedAssessment := originalInput
			expectedAssessment.Event.FundingTime = expectedAssessment.Event.FundingTime.Round(0).UTC()
			if !reflect.DeepEqual(position, originalPosition) || !reflect.DeepEqual(input, originalInput) ||
				!reflect.DeepEqual(got.Position, position) || !reflect.DeepEqual(got.Assessment, expectedAssessment) ||
				!got.Assessment.Event.FundingTime.Equal(input.Event.FundingTime) || got.Assessment.Event.FundingTime.Location() != time.UTC {
				t.Fatal("position, input or exact UTC timestamp changed")
			}
			again, err := ProjectLongFunding(position, input)
			if err != nil || !reflect.DeepEqual(again, got) {
				t.Fatal("pure retry differs")
			}
		})
	}
	third, err := fundingAmount(t, "1").Quo(fundingAmount(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	p.Quantity, p.CostBasisUSDT = third, third
	a.MarkPrice, a.Event.Rate = fundingAmount(t, "1"), fundingAmount(t, "1")
	got, err := ProjectLongFunding(p, a)
	if err != nil {
		t.Fatal(err)
	}
	n, d, err := got.CashDeltaUSDT.CanonicalRatio()
	if err != nil || n != "-1" || d != "3" {
		t.Fatalf("ratio %s/%s %v", n, d, err)
	}
}

// TestProjectLongFundingInvalid проверяет нулевой результат при любой ошибке.
func TestProjectLongFundingInvalid(t *testing.T) {
	p, a := fundingFixture(t)
	invalid := []struct {
		name   string
		mutate func(*LongPosition, *LongFundingAssessment)
		want   error
	}{
		{"empty-id", func(_ *LongPosition, a *LongFundingAssessment) { a.FundingID = "" }, ErrInvalidFundingAssessment},
		{"leading-id", func(_ *LongPosition, a *LongFundingAssessment) { a.FundingID = " id" }, ErrInvalidFundingAssessment},
		{"trailing-id", func(_ *LongPosition, a *LongFundingAssessment) { a.FundingID = "id " }, ErrInvalidFundingAssessment},
		{"empty-symbol", func(_ *LongPosition, a *LongFundingAssessment) { a.Symbol = "" }, ErrInvalidFundingAssessment},
		{"leading-symbol", func(_ *LongPosition, a *LongFundingAssessment) { a.Symbol = " BTCUSDT" }, ErrInvalidFundingAssessment},
		{"trailing-symbol", func(_ *LongPosition, a *LongFundingAssessment) { a.Symbol = "BTCUSDT " }, ErrInvalidFundingAssessment},
		{"time", func(_ *LongPosition, a *LongFundingAssessment) { a.Event.FundingTime = time.Time{} }, ErrInvalidFundingAssessment},
		{"rate", func(_ *LongPosition, a *LongFundingAssessment) { a.Event.Rate = Decimal{} }, ErrInvalidFundingAssessment},
		{"mark", func(_ *LongPosition, a *LongFundingAssessment) { a.MarkPrice = Decimal{} }, ErrInvalidFundingAssessment},
		{"zero-mark", func(_ *LongPosition, a *LongFundingAssessment) { a.MarkPrice = Zero() }, ErrInvalidFundingAssessment},
		{"negative-mark", func(_ *LongPosition, a *LongFundingAssessment) { a.MarkPrice = fundingAmount(t, "-1") }, ErrInvalidFundingAssessment},
		{"qty", func(p *LongPosition, _ *LongFundingAssessment) { p.Quantity = Decimal{} }, ErrInvalidLedgerPosition},
		{"basis", func(p *LongPosition, _ *LongFundingAssessment) { p.CostBasisUSDT = Decimal{} }, ErrInvalidLedgerPosition},
		{"negative-qty", func(p *LongPosition, _ *LongFundingAssessment) { p.Quantity = fundingAmount(t, "-1") }, ErrInvalidLedgerPosition},
		{"negative-basis", func(p *LongPosition, _ *LongFundingAssessment) { p.CostBasisUSDT = fundingAmount(t, "-1") }, ErrInvalidLedgerPosition},
		{"basis-without-qty", func(p *LongPosition, _ *LongFundingAssessment) { p.Quantity = Zero() }, ErrInvalidLedgerPosition},
		{"qty-without-basis", func(p *LongPosition, _ *LongFundingAssessment) { p.CostBasisUSDT = Zero() }, ErrInvalidLedgerPosition},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			position, input := p, a
			tc.mutate(&position, &input)
			got, err := ProjectLongFunding(position, input)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, FundingPosting{}) {
				t.Fatalf("got %+v %v", got, err)
			}
		})
	}
	huge := fundingAmount(t, strings.Repeat("9", 100))
	for i := 0; i < 3; i++ {
		var err error
		huge, err = huge.Mul(huge)
		if err != nil {
			t.Fatal(err)
		}
	}
	p.Quantity, p.CostBasisUSDT, a.MarkPrice = huge, huge, huge
	got, err := ProjectLongFunding(p, a)
	if !errors.Is(err, ErrFundingArithmetic) || !reflect.DeepEqual(got, FundingPosting{}) {
		t.Fatalf("overflow %+v %v", got, err)
	}
	p.Quantity, p.CostBasisUSDT, a.Event.Rate = Zero(), Zero(), huge
	got, err = ProjectLongFunding(p, a)
	if err != nil || !reflect.DeepEqual(got.Position, p) || got.CashDeltaUSDT.value.Sign() != 0 {
		t.Fatalf("flat position must bypass overflowing arithmetic: %+v %v", got, err)
	}
	p.Quantity, p.CostBasisUSDT, a.Event.Rate = huge, huge, fundingAmount(t, "0.0001")
	a.Event.Rate = Zero()
	got, err = ProjectLongFunding(p, a)
	if err != nil {
		t.Fatal(err)
	}
	cmp, err := got.CashDeltaUSDT.Cmp(Zero())
	if err != nil || cmp != 0 || !reflect.DeepEqual(got.Position, p) ||
		!got.Assessment.Event.FundingTime.Equal(a.Event.FundingTime) || got.Assessment.Event.FundingTime.Location() != time.UTC {
		t.Fatal("zero rate must bypass overflowing notional and preserve normalized data")
	}
	p.Quantity, p.CostBasisUSDT = fundingAmount(t, "1"), fundingAmount(t, "1")
	a.Event.Rate = huge
	got, err = ProjectLongFunding(p, a)
	if !errors.Is(err, ErrFundingArithmetic) || !reflect.DeepEqual(got, FundingPosting{}) {
		t.Fatalf("rate overflow %+v %v", got, err)
	}
}
