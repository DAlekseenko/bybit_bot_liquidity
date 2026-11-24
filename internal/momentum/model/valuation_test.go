package model

import (
	"errors"
	"testing"
	"time"
)

// accountInput строит вход оценки счёта с одной позицией BTCUSDT.
func accountInput(t *testing.T, wallet string, position LongPosition, leverage, price string, observedAt time.Time) AccountValuationInput {
	t.Helper()
	return AccountValuationInput{
		WalletUSDT: valuationDecimal(t, wallet),
		Positions:  []AccountPositionInput{{Symbol: "BTCUSDT", Position: position, Leverage: valuationDecimal(t, leverage)}},
		Marks:      []LongMarkObservation{{Symbol: "BTCUSDT", ObservedAt: observedAt, Price: valuationDecimal(t, price)}},
		AsOf:       valuationAt(), MaxAge: time.Minute,
	}
}

// mustAccountValuation рассчитывает оценку счёта и падает при ошибке.
func mustAccountValuation(t *testing.T, input AccountValuationInput) AccountValuation {
	t.Helper()
	got, err := ProjectAccountValuation(input)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// accountSum складывает значения и падает при ошибке арифметики.
func accountSum(t *testing.T, values ...Decimal) Decimal {
	t.Helper()
	total := Zero()
	for _, value := range values {
		var err error
		if total, err = total.Add(value); err != nil {
			t.Fatal(err)
		}
	}
	return total
}

// accountDiff вычитает значения и падает при ошибке арифметики.
func accountDiff(t *testing.T, a, b Decimal) Decimal {
	t.Helper()
	diff, err := a.Sub(b)
	if err != nil {
		t.Fatal(err)
	}
	return diff
}

// TestAccountValuationMarginAndEquity проверяет equity, isolated margin и отсутствие notional в equity.
func TestAccountValuationMarginAndEquity(t *testing.T) {
	position := LongPosition{Quantity: valuationDecimal(t, "2"), CostBasisUSDT: valuationDecimal(t, "200")}
	got := mustAccountValuation(t, accountInput(t, "999.8", position, "2", "110", valuationAt()))
	requireValuationRatio(t, got.UnrealizedPnLUSDT, "20/1")
	requireValuationRatio(t, got.EquityUSDT, "5099/5")
	requireValuationRatio(t, got.UsedMarginUSDT, "100/1")
	requireValuationRatio(t, got.FreeMarginUSDT, "4599/5")
	requireValuationRatio(t, got.Positions[0].NotionalUSDT, "220/1")
	if !got.Fresh || !got.SizingAllowed {
		t.Fatalf("fresh=%v sizing=%v", got.Fresh, got.SizingAllowed)
	}
	if equity, err := got.SizingEquity(); err != nil || equity.value.Cmp(got.EquityUSDT.value) != 0 {
		t.Fatalf("sizing equity %v, %v", equity, err)
	}
}

// TestAccountValuationBlocksSizing проверяет блокировку sizing при stale mark и неположительном equity.
func TestAccountValuationBlocksSizing(t *testing.T) {
	position := LongPosition{Quantity: valuationDecimal(t, "1"), CostBasisUSDT: valuationDecimal(t, "100")}
	for _, test := range []struct {
		name, wallet, price string
		observedAt          time.Time
		fresh               bool
		want                error
	}{
		{"stale", "1000", "100", valuationAt().Add(-time.Minute - time.Nanosecond), false, ErrAccountMarkStale},
		{"zero equity", "50", "50", valuationAt(), true, ErrAccountEquityNotPositive},
		{"negative equity", "-1", "100", valuationAt(), true, ErrAccountEquityNotPositive},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := mustAccountValuation(t, accountInput(t, test.wallet, position, "1", test.price, test.observedAt))
			if got.Fresh != test.fresh || got.SizingAllowed {
				t.Fatalf("fresh=%v sizing=%v", got.Fresh, got.SizingAllowed)
			}
			if _, err := got.SizingEquity(); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

// TestAccountValuationRejectsInvalidInput проверяет fail-closed отказ на неполных данных.
func TestAccountValuationRejectsInvalidInput(t *testing.T) {
	position := LongPosition{Quantity: valuationDecimal(t, "1"), CostBasisUSDT: valuationDecimal(t, "100")}
	for _, test := range []struct {
		name   string
		mutate func(*AccountValuationInput)
		want   error
	}{
		{"missing mark", func(in *AccountValuationInput) { in.Marks = nil }, ErrAccountMarkMissing},
		{"future mark", func(in *AccountValuationInput) { in.Marks[0].ObservedAt = in.AsOf.Add(time.Nanosecond) }, ErrInvalidAccountValuation},
		{"duplicate symbol", func(in *AccountValuationInput) { in.Positions = append(in.Positions, in.Positions[0]) }, ErrInvalidAccountValuation},
		{"leverage below one", func(in *AccountValuationInput) { in.Positions[0].Leverage = valuationDecimal(t, "0.5") }, ErrInvalidAccountValuation},
		{"zero max age", func(in *AccountValuationInput) { in.MaxAge = 0 }, ErrInvalidAccountValuation},
		{"missing wallet", func(in *AccountValuationInput) { in.WalletUSDT = Decimal{} }, ErrInvalidAccountValuation},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := accountInput(t, "1000", position, "1", "100", valuationAt())
			test.mutate(&input)
			if _, err := ProjectAccountValuation(input); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

// TestAccountValuationEquityIdentity проверяет Δequity = realized + ΔUPL − fees + funding + transfers.
func TestAccountValuationEquityIdentity(t *testing.T) {
	type step struct {
		name, mark, transfer, fundingRate string
		fill                              *ConfirmedFill
	}
	fill := func(id string, side FillSide, qty, price, fee string) *ConfirmedFill {
		return &ConfirmedFill{ExecutionID: id, Symbol: "BTCUSDT", Side: side, ExecutedAt: valuationAt(),
			Quantity: valuationDecimal(t, qty), ExecutionPrice: valuationDecimal(t, price), FeeUSDT: valuationDecimal(t, fee)}
	}
	steps := []step{
		{name: "open", mark: "101", fill: fill("e1", FillBuy, "2", "100", "0.2")},
		{name: "mark move", mark: "110"},
		{name: "funding", mark: "110", fundingRate: "0.0001"},
		{name: "transfer", mark: "110", transfer: "50"},
		{name: "partial close", mark: "120", fill: fill("e2", FillSell, "1", "120", "0.12")},
		{name: "full close", mark: "90", fill: fill("e3", FillSell, "1", "90", "0.09")},
	}
	wallet, initial := valuationDecimal(t, "1000"), valuationDecimal(t, "1000")
	flows := Zero()
	position := LongPosition{Quantity: Zero(), CostBasisUSDT: Zero()}
	before := mustAccountValuation(t, accountInput(t, "1000", position, "1", "100", valuationAt()))
	for _, s := range steps {
		realized, fees, funding, transfer := Zero(), Zero(), Zero(), Zero()
		if s.fill != nil {
			posting, err := ProjectLongFill(position, *s.fill)
			if err != nil {
				t.Fatal(err)
			}
			position, realized, fees = posting.Position, posting.RealizedPnLUSDT, posting.Fill.FeeUSDT
		}
		if s.fundingRate != "" {
			posting, err := ProjectLongFunding(position, LongFundingAssessment{FundingID: "f1", Symbol: "BTCUSDT",
				Event: FundingEvent{FundingTime: valuationAt(), Rate: valuationDecimal(t, s.fundingRate)}, MarkPrice: valuationDecimal(t, s.mark)})
			if err != nil {
				t.Fatal(err)
			}
			funding = posting.CashDeltaUSDT
		}
		if s.transfer != "" {
			transfer = valuationDecimal(t, s.transfer)
		}
		cash := accountSum(t, accountDiff(t, realized, fees), funding, transfer)
		wallet, flows = accountSum(t, wallet, cash), accountSum(t, flows, cash)
		input := accountInput(t, "0", position, "1", s.mark, valuationAt())
		input.WalletUSDT = wallet
		after := mustAccountValuation(t, input)

		deltaEquity := accountDiff(t, after.EquityUSDT, before.EquityUSDT)
		deltaUPL := accountDiff(t, after.UnrealizedPnLUSDT, before.UnrealizedPnLUSDT)
		if want := accountSum(t, cash, deltaUPL); deltaEquity.value.Cmp(want.value) != 0 {
			t.Fatalf("%s: Δequity %s, want %s", s.name, deltaEquity.value, want.value)
		}
		if want := accountSum(t, after.WalletUSDT, after.Positions[0].NotionalUSDT); position.Quantity.value.Sign() > 0 &&
			after.EquityUSDT.value.Cmp(want.value) == 0 {
			t.Fatalf("%s: notional added to equity", s.name)
		}
		before = after
	}
	requireValuationRatio(t, before.UnrealizedPnLUSDT, "0/1")
	requireValuationRatio(t, before.EquityUSDT, "132446/125")
	requireValuationRatio(t, before.EquityUSDT, func() string {
		n, d, _ := accountSum(t, initial, flows).CanonicalRatio()
		return n + "/" + d
	}())
}
