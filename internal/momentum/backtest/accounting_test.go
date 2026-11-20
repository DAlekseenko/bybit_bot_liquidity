package backtest

import (
	"errors"
	"maps"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// TestProjectAccountingStepMultiSymbol проверяет funding старых позиций до итоговой equity.
func TestProjectAccountingStepMultiSymbol(t *testing.T) {
	in := accountingFixture(t)
	in.Funding = []model.LongFundingAssessment{
		{FundingID: "f-btc", Symbol: "BTCUSDT", Event: model.FundingEvent{FundingTime: in.AsOf, Rate: amount(t, "0.01")}, MarkPrice: amount(t, "110")},
		{FundingID: "f-eth", Symbol: "ETHUSDT", Event: model.FundingEvent{FundingTime: in.AsOf, Rate: amount(t, "-0.02")}, MarkPrice: amount(t, "50")},
	}
	got, err := ProjectAccountingStep(in)
	if err != nil {
		t.Fatal(err)
	}
	assertDecimal(t, got.FundingCashDeltaUSDT, "0.8")
	assertDecimal(t, got.WalletUSDT, "100.8")
	assertDecimal(t, got.UnrealizedPnLUSDT, "-6")
	assertDecimal(t, got.EquityUSDT, "94.8")
	if len(got.FundingPostings) != 2 || got.FundingPostings[0].Assessment.Symbol != "BTCUSDT" || got.FundingPostings[1].Assessment.Symbol != "ETHUSDT" {
		t.Fatalf("noncanonical postings: %+v", got.FundingPostings)
	}
	if len(got.Valuations) != 3 || got.Valuations[0].Position.Quantity != in.Positions["BTCUSDT"].Quantity {
		t.Fatalf("old positions not valued: %+v", got.Valuations)
	}
	for _, valuation := range got.Valuations {
		if !reflect.DeepEqual(valuation.Position, in.Positions[valuation.Mark.Symbol]) {
			t.Fatalf("valuation changed the old position: %+v", valuation)
		}
	}
	if got.AsOf.Location() != time.UTC {
		t.Fatalf("AsOf is not UTC: %v", got.AsOf)
	}
}

// TestProjectAccountingStepCases покрывает нулевые состояния, порядок, идемпотентность и ошибки формы.
func TestProjectAccountingStepCases(t *testing.T) {
	base := accountingFixture(t)
	base.Funding = []model.LongFundingAssessment{funding(t, &base, "BTCUSDT", "btc", "0.01"), funding(t, &base, "ETHUSDT", "eth", "-0.02")}
	flat := accountingFixture(t)
	flat.WalletUSDT = amount(t, "-5")
	for symbol := range flat.Positions {
		flat.Positions[symbol] = model.LongPosition{Quantity: amount(t, "0"), CostBasisUSDT: amount(t, "0")}
	}
	for symbol, mark := range flat.Marks {
		mark.ObservedAt = flat.AsOf.Add(-time.Hour)
		flat.Marks[symbol] = mark
	}
	gotFlat, err := ProjectAccountingStep(flat)
	if err != nil {
		t.Fatalf("signed flat result: %+v, %v", gotFlat, err)
	}
	assertDecimal(t, gotFlat.EquityUSDT, "-5")
	flat.Marks["BTCUSDT"] = model.LongMarkObservation{Symbol: "BTCUSDT", ObservedAt: flat.AsOf.Add(-time.Hour), Price: amount(t, "2")}
	if _, err := ProjectAccountingStep(flat); err != nil {
		t.Fatalf("exact maxAge boundary: %v", err)
	}
	ordered := base
	ordered.Symbols = []string{"HYPEUSDT", "ETHUSDT", "BTCUSDT"}
	ordered.AsOf = ordered.AsOf.In(time.FixedZone("west", -7*3600))
	ordered.Positions, ordered.Marks = map[string]model.LongPosition{}, map[string]model.LongMarkObservation{}
	for _, symbol := range ordered.Symbols {
		ordered.Positions[symbol], ordered.Marks[symbol] = base.Positions[symbol], base.Marks[symbol]
	}
	ordered.Funding = []model.LongFundingAssessment{base.Funding[1], base.Funding[0]}
	first, err := ProjectAccountingStep(ordered)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ProjectAccountingStep(ordered)
	baseResult, baseErr := ProjectAccountingStep(base)
	if err != nil || baseErr != nil || !reflect.DeepEqual(first, second) || !reflect.DeepEqual(first, baseResult) || !reflect.DeepEqual(first.Valuations[0].Position, base.Positions["BTCUSDT"]) {
		t.Fatalf("unstable projection: %+v / %+v, %v", first, second, err)
	}
	zeroRate := accountingFixture(t)
	zeroRate.Funding = []model.LongFundingAssessment{funding(t, &zeroRate, "BTCUSDT", "zero", "0")}
	zeroResult, err := ProjectAccountingStep(zeroRate)
	if err != nil || len(zeroResult.FundingPostings) != 1 {
		t.Fatalf("zero rate posting: %+v, %v", zeroResult, err)
	}
	assertDecimal(t, zeroResult.FundingCashDeltaUSDT, "0")
	bad := []struct {
		name string
		edit func(*AccountingInput)
	}{
		{"missing-position", func(i *AccountingInput) { delete(i.Positions, "BTCUSDT") }},
		{"extra-position", func(i *AccountingInput) { i.Positions["XUSDT"] = i.Positions["BTCUSDT"] }},
		{"missing-mark", func(i *AccountingInput) { delete(i.Marks, "BTCUSDT") }},
		{"extra-mark", func(i *AccountingInput) { i.Marks["XUSDT"] = i.Marks["BTCUSDT"] }},
		{"wrong-mark-symbol", func(i *AccountingInput) { m := i.Marks["BTCUSDT"]; m.Symbol = "ETHUSDT"; i.Marks["BTCUSDT"] = m }},
		{"invalid-mark", func(i *AccountingInput) { m := i.Marks["BTCUSDT"]; m.Price = model.Decimal{}; i.Marks["BTCUSDT"] = m }},
		{"invalid-wallet", func(i *AccountingInput) { i.WalletUSDT = model.Decimal{} }},
		{"invalid-position", func(i *AccountingInput) { i.Positions["BTCUSDT"] = model.LongPosition{} }},
		{"future-mark", func(i *AccountingInput) {
			m := i.Marks["BTCUSDT"]
			m.ObservedAt = i.AsOf.Add(time.Nanosecond)
			i.Marks["BTCUSDT"] = m
		}},
		{"stale-mark", func(i *AccountingInput) {
			m := i.Marks["BTCUSDT"]
			m.ObservedAt = i.AsOf.Add(-2 * time.Hour)
			i.Marks["BTCUSDT"] = m
		}},
		{"zero-age", func(i *AccountingInput) { i.MarkMaxAge = 0 }},
		{"negative-age", func(i *AccountingInput) { i.MarkMaxAge = -time.Second }},
		{"zero-time", func(i *AccountingInput) { i.AsOf = time.Time{} }},
		{"wrong-funding-time", func(i *AccountingInput) {
			f := funding(t, i, "BTCUSDT", "f1", "0.01")
			f.Event.FundingTime = f.Event.FundingTime.Add(time.Second)
			i.Funding = []model.LongFundingAssessment{f}
		}},
		{"unknown-funding-symbol", func(i *AccountingInput) {
			f := funding(t, i, "NOPEUSDT", "f1", "0.01")
			i.Funding = []model.LongFundingAssessment{f}
		}},
		{"duplicate-id", func(i *AccountingInput) {
			f := funding(t, i, "BTCUSDT", "f1", "0.01")
			g := funding(t, i, "ETHUSDT", "f1", "0.01")
			i.Funding = []model.LongFundingAssessment{f, g}
		}},
		{"duplicate-identical-event", func(i *AccountingInput) {
			f := funding(t, i, "BTCUSDT", "f1", "0.01")
			i.Funding = []model.LongFundingAssessment{f, f}
		}},
		{"duplicate-symbol", func(i *AccountingInput) {
			f := funding(t, i, "BTCUSDT", "f1", "0.01")
			f2 := funding(t, i, "BTCUSDT", "f2", "0.01")
			i.Funding = []model.LongFundingAssessment{f, f2}
		}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			i := accountingFixture(t)
			tc.edit(&i)
			got, err := ProjectAccountingStep(i)
			if err == nil || !reflect.DeepEqual(got, AccountingResult{}) {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

// accountingFixture строит полный трёхсимвольный вход с точными числами.
func accountingFixture(t *testing.T) AccountingInput {
	t.Helper()
	asOf := time.Date(2026, 11, 20, 12, 0, 0, 0, time.FixedZone("east", 3*3600))
	return AccountingInput{Symbols: []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}, AsOf: asOf, MarkMaxAge: time.Hour, WalletUSDT: amount(t, "100"),
		Positions: map[string]model.LongPosition{"BTCUSDT": {Quantity: amount(t, "2"), CostBasisUSDT: amount(t, "210")}, "ETHUSDT": {Quantity: amount(t, "3"), CostBasisUSDT: amount(t, "150")}, "HYPEUSDT": {Quantity: amount(t, "1"), CostBasisUSDT: amount(t, "45")}},
		Marks:     map[string]model.LongMarkObservation{"BTCUSDT": {Symbol: "BTCUSDT", ObservedAt: asOf, Price: amount(t, "100")}, "ETHUSDT": {Symbol: "ETHUSDT", ObservedAt: asOf, Price: amount(t, "50")}, "HYPEUSDT": {Symbol: "HYPEUSDT", ObservedAt: asOf, Price: amount(t, "49")}}}
}

// funding создаёт событие funding для переданного символа и времени тестового шага.
func funding(t *testing.T, in *AccountingInput, symbol, id, rate string) model.LongFundingAssessment {
	t.Helper()
	price := in.Marks[symbol].Price
	return model.LongFundingAssessment{FundingID: id, Symbol: symbol, Event: model.FundingEvent{FundingTime: in.AsOf, Rate: amount(t, rate)}, MarkPrice: price}
}

// amount разбирает точное число и завершает тест при некорректном литерале.
func amount(t *testing.T, value string) model.Decimal {
	t.Helper()
	got, err := model.ParseAmount(value)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// assertDecimal сравнивает exact рациональные значения через каноническое представление.
func assertDecimal(t *testing.T, got model.Decimal, want string) {
	t.Helper()
	left, ln, err := got.CanonicalRatio()
	if err != nil {
		t.Fatal(err)
	}
	right, rn, err := amount(t, want).CanonicalRatio()
	if err != nil || left != right || ln != rn {
		t.Fatalf("got %s/%s, want %s/%s", left, ln, right, rn)
	}
}

// TestProjectAccountingStepModelErrors проверяет исходные ошибки модели и нулевой итог.
func TestProjectAccountingStepModelErrors(t *testing.T) {
	in := accountingFixture(t)
	in.Funding = []model.LongFundingAssessment{funding(t, &in, "BTCUSDT", "f", "0.01")}
	in.Funding[0].MarkPrice = model.Decimal{}
	got, err := ProjectAccountingStep(in)
	if !errors.Is(err, model.ErrInvalidFundingAssessment) || !reflect.DeepEqual(got, AccountingResult{}) {
		t.Fatalf("model error lost: %+v, %v", got, err)
	}
}

// TestProjectAccountingStepArithmeticError сохраняет ошибку модели и исходные входы.
func TestProjectAccountingStepArithmeticError(t *testing.T) {
	in := accountingFixture(t)
	large := largeAccountingDecimal(t)
	in.Positions["BTCUSDT"] = model.LongPosition{Quantity: large, CostBasisUSDT: large}
	mark := in.Marks["BTCUSDT"]
	mark.Price = large
	in.Marks["BTCUSDT"] = mark
	beforePositions, beforeMarks, beforeSymbols := maps.Clone(in.Positions), maps.Clone(in.Marks), append([]string(nil), in.Symbols...)
	got, err := ProjectAccountingStep(in)
	if !errors.Is(err, model.ErrLongValuationArithmetic) || !reflect.DeepEqual(got, AccountingResult{}) || !reflect.DeepEqual(in.Positions, beforePositions) || !reflect.DeepEqual(in.Marks, beforeMarks) || !reflect.DeepEqual(in.Symbols, beforeSymbols) {
		t.Fatalf("arithmetic failure changed result or input: %+v, %v", got, err)
	}
}

// largeAccountingDecimal создаёт допустимый Decimal у предела точности модели.
func largeAccountingDecimal(t *testing.T) model.Decimal {
	t.Helper()
	unit := amount(t, "9999999999999999999999999999999999999999999999999999999999999999")
	value := unit
	for range 30 {
		next, err := value.Mul(unit)
		if err != nil {
			return value
		}
		value = next
	}
	t.Fatal("decimal did not reach its arithmetic limit")
	return model.Decimal{}
}
