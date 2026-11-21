package backtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

type journalWireAmount struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
}
type journalWirePosition struct {
	Quantity      journalWireAmount
	CostBasisUSDT journalWireAmount
}
type journalWireValuation struct {
	Symbol, ObservedAt                         string
	Position                                   journalWirePosition
	MarkPrice, NotionalUSDT, UnrealizedPnLUSDT journalWireAmount
}
type journalWireAccounting struct {
	WalletUSDT, FundingCashDeltaUSDT, UnrealizedPnLUSDT, EquityUSDT journalWireAmount
	Valuations                                                      []journalWireValuation
}
type journalWireFunding struct {
	FundingID, Symbol string
	Rate, MarkPrice   journalWireAmount
	Before            journalWirePosition
	CashDeltaUSDT     journalWireAmount
}
type journalWireRow struct {
	ID, At, Role, CycleID, PlanHash string
	Funding                         *journalWireFunding
	Accounting                      *journalWireAccounting
}

// journalAccountingFixture создаёт синтетическую оценку с двумя фактическими funding.
func journalAccountingFixture(t *testing.T) (AccountingResult, []string) {
	t.Helper()
	input := accountingFixture(t)
	input.Funding = []model.LongFundingAssessment{funding(t, &input, "BTCUSDT", "btc", "0.01"), funding(t, &input, "ETHUSDT", "eth", "-0.02")}
	result, err := ProjectAccountingStep(input)
	if err != nil {
		t.Fatal(err)
	}
	return result, input.Symbols
}

// TestJournalAccountingProjection проверяет funding до оценки и точные вычисленные поля.
func TestJournalAccountingProjection(t *testing.T) {
	value, symbols := journalAccountingFixture(t)
	run := strings.Repeat("a", 64)
	got, err := ProjectAccountingJournalSegment(run, "", "", "hourly", value, symbols)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID, At, Role string
		Funding      *struct {
			FundingID, Symbol string
			CashDeltaUSDT     struct{ Numerator, Denominator string }
		}
		Accounting *struct {
			EquityUSDT struct{ Numerator, Denominator string }
			Valuations []json.RawMessage
		}
	}
	if err := json.Unmarshal(got.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Role != "funding" || rows[1].Role != "funding" || rows[2].Role != "hourly" || rows[0].Funding.FundingID != "btc" || rows[0].Funding.CashDeltaUSDT.Numerator != "-2" || rows[0].Funding.CashDeltaUSDT.Denominator != "1" || rows[2].Accounting.EquityUSDT.Numerator != "95" || rows[2].Accounting.EquityUSDT.Denominator != "1" || len(rows[2].Accounting.Valuations) != 3 {
		t.Fatalf("unexpected journal %s", got.Bytes())
	}
	other, err := ProjectAccountingJournalSegment(run, "", "", "hourly", value, symbols)
	if err != nil || !bytes.Equal(got.Bytes(), other.Bytes()) {
		t.Fatal("repeat changed")
	}
	exposed := got.Bytes()
	exposed[0] = 'x'
	if !bytes.Equal(got.Bytes(), other.Bytes()) {
		t.Fatal("output aliases")
	}
	zone := time.FixedZone("local", 10800)
	value.AsOf = value.AsOf.In(zone)
	for i := range value.Valuations {
		value.Valuations[i].AsOf = value.Valuations[i].AsOf.In(zone)
		value.Valuations[i].Mark.ObservedAt = value.Valuations[i].Mark.ObservedAt.In(zone)
	}
	for i := range value.FundingPostings {
		value.FundingPostings[i].Assessment.Event.FundingTime = value.FundingPostings[i].Assessment.Event.FundingTime.In(zone)
	}
	equivalent, err := ProjectAccountingJournalSegment(run, "", "", "hourly", value, symbols)
	if err != nil || !bytes.Equal(got.Bytes(), equivalent.Bytes()) {
		t.Fatal("timezone changed")
	}
}

// TestJournalAmountRatio проверяет точность неограниченной wire-грамматикой дроби.
func TestJournalAmountRatio(t *testing.T) {
	third, err := amount(t, "1").Quo(amount(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	negative, err := model.Zero().Sub(third)
	if err != nil {
		t.Fatal(err)
	}
	large := largeAccountingDecimal(t)
	for _, test := range []struct {
		value model.Decimal
		want  string
	}{
		{third, `{"numerator":"1","denominator":"3"}`},
		{negative, `{"numerator":"-1","denominator":"3"}`},
		{model.Zero(), `{"numerator":"0","denominator":"1"}`},
	} {
		raw, err := json.Marshal(journalAmount{test.value})
		if err != nil || string(raw) != test.want {
			t.Fatalf("ratio %s %v", raw, err)
		}
	}
	if _, err := json.Marshal(journalAmount{large}); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(journalAmount{}); err == nil {
		t.Fatal("invalid accepted")
	}
}

// TestJournalAccountingFailures проверяет поздние ошибки без частичного участка.
func TestJournalAccountingFailures(t *testing.T) {
	run := strings.Repeat("a", 64)
	for _, test := range []struct {
		name   string
		mutate func(*AccountingResult)
	}{
		{"zero time", func(v *AccountingResult) { v.AsOf = time.Time{} }},
		{"missing valuation", func(v *AccountingResult) { v.Valuations = v.Valuations[:2] }},
		{"late decimal", func(v *AccountingResult) { v.Valuations[2].UnrealizedPnLUSDT = model.Decimal{} }},
		{"total decimal", func(v *AccountingResult) { v.EquityUSDT = model.Decimal{} }},
		{"funding decimal", func(v *AccountingResult) { v.FundingPostings[1].Assessment.Event.Rate = model.Decimal{} }},
		{"funding time", func(v *AccountingResult) { v.FundingPostings[1].Assessment.Event.FundingTime = v.AsOf.Add(time.Second) }},
		{"unknown symbol", func(v *AccountingResult) { v.FundingPostings[1].Assessment.Symbol = "UNKNOWN" }},
		{"duplicate id", func(v *AccountingResult) { v.FundingPostings[1].Assessment.FundingID = "btc" }},
		{"reordered valuations", func(v *AccountingResult) { v.Valuations[0], v.Valuations[2] = v.Valuations[2], v.Valuations[0] }},
		{"future mark", func(v *AccountingResult) { v.Valuations[2].Mark.ObservedAt = v.AsOf.Add(time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, symbols := journalAccountingFixture(t)
			test.mutate(&value)
			result, err := ProjectAccountingJournalSegment(run, "", "", "hourly", value, symbols)
			if !errors.Is(err, ErrReplayJournal) || !reflect.DeepEqual(result, ReplayJournalSegment{}) {
				t.Fatalf("partial %v", err)
			}
		})
	}
	value, symbols := journalAccountingFixture(t)
	for _, role := range []string{"initial", "posttrade", "terminal", "other"} {
		_, err := ProjectAccountingJournalSegment(run, "", "", role, value, symbols)
		if !errors.Is(err, ErrReplayJournal) {
			t.Fatalf("role %s accepted", role)
		}
	}
}

// TestJournalExactWireFields проверяет raw-поля, идентичность и точные отметки времени.
func TestJournalExactWireFields(t *testing.T) {
	value, symbols := journalAccountingFixture(t)
	value.AsOf = value.AsOf.Add(123 * time.Nanosecond)
	for i := range value.Valuations {
		value.Valuations[i].AsOf = value.AsOf
		value.Valuations[i].Mark.ObservedAt = value.AsOf.Add(-time.Nanosecond)
	}
	for i := range value.FundingPostings {
		value.FundingPostings[i].Assessment.Event.FundingTime = value.AsOf
	}
	run := strings.Repeat("b", 64)
	segment, err := ProjectAccountingJournalSegment(run, "", "", "hourly", value, symbols)
	if err != nil {
		t.Fatal(err)
	}
	var rows []journalWireRow
	if err := json.Unmarshal(segment.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].ID != baselineIdentity([]string{run, "funding", "btc"}) || rows[2].ID != baselineIdentity([]string{run, "hourly", journalTime(value.AsOf)}) {
		t.Fatalf("unexpected identities: %+v", rows)
	}
	first := rows[0].Funding
	if first == nil || first.FundingID != "btc" || first.Symbol != "BTCUSDT" || first.Rate != journalWireRatio(t, value.FundingPostings[0].Assessment.Event.Rate) || first.MarkPrice != journalWireRatio(t, value.FundingPostings[0].Assessment.MarkPrice) || first.Before.Quantity != journalWireRatio(t, value.FundingPostings[0].Position.Quantity) || first.Before.CostBasisUSDT != journalWireRatio(t, value.FundingPostings[0].Position.CostBasisUSDT) || first.CashDeltaUSDT != journalWireRatio(t, value.FundingPostings[0].CashDeltaUSDT) {
		t.Fatalf("funding raw fields lost: %+v", first)
	}
	accounting := rows[2].Accounting
	if accounting == nil || accounting.WalletUSDT != journalWireRatio(t, value.WalletUSDT) || accounting.FundingCashDeltaUSDT != journalWireRatio(t, value.FundingCashDeltaUSDT) || accounting.UnrealizedPnLUSDT != journalWireRatio(t, value.UnrealizedPnLUSDT) || accounting.EquityUSDT != journalWireRatio(t, value.EquityUSDT) || len(accounting.Valuations) != len(symbols) {
		t.Fatalf("accounting raw fields lost: %+v", accounting)
	}
	for i, got := range accounting.Valuations {
		want := value.Valuations[i]
		if got.Symbol != symbols[i] || got.ObservedAt != journalTime(want.Mark.ObservedAt) || got.Position.Quantity != journalWireRatio(t, want.Position.Quantity) || got.Position.CostBasisUSDT != journalWireRatio(t, want.Position.CostBasisUSDT) || got.MarkPrice != journalWireRatio(t, want.Mark.Price) || got.NotionalUSDT != journalWireRatio(t, want.NotionalUSDT) || got.UnrealizedPnLUSDT != journalWireRatio(t, want.UnrealizedPnLUSDT) {
			t.Fatalf("valuation %d lost fields: %+v", i, got)
		}
	}
	changed := value
	changed.EquityUSDT, err = changed.EquityUSDT.Add(amount(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	different, err := ProjectAccountingJournalSegment(run, "", "", "hourly", changed, symbols)
	if err != nil || bytes.Equal(segment.Bytes(), different.Bytes()) {
		t.Fatal("consequential change did not change bytes")
	}
	for i := range value.FundingPostings {
		if rows[i].At != journalTime(value.AsOf) {
			t.Fatalf("funding time mismatch: %q", rows[i].At)
		}
	}
}

// journalWireRatio возвращает ожидаемую точную пару JSON-дроби.
func journalWireRatio(t *testing.T, value model.Decimal) journalWireAmount {
	t.Helper()
	numerator, denominator, err := value.CanonicalRatio()
	if err != nil {
		t.Fatal(err)
	}
	return journalWireAmount{numerator, denominator}
}

// TestJournalRoleAndBindingMatrix проверяет роли, cycle-поля и universe.
func TestJournalRoleAndBindingMatrix(t *testing.T) {
	value, symbols := journalAccountingFixture(t)
	run, cycle, plan := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	for _, role := range []string{"hourly", "pretrade"} {
		c, p := "", ""
		if role == "pretrade" {
			c, p = cycle, plan
		}
		if _, err := ProjectAccountingJournalSegment(run, c, p, role, value, symbols); err != nil {
			t.Fatalf("role %s rejected: %v", role, err)
		}
	}
	value.FundingPostings = nil
	for _, role := range []string{"initial", "hourly", "pretrade", "posttrade", "terminal"} {
		c, p := "", ""
		if role == "pretrade" || role == "posttrade" {
			c, p = cycle, plan
		}
		if _, err := ProjectAccountingJournalSegment(run, c, p, role, value, symbols); err != nil {
			t.Fatalf("role %s rejected: %v", role, err)
		}
	}
	for _, symbolsCase := range [][]string{{"ETHUSDT", "BTCUSDT", "HYPEUSDT"}, {"BTCUSDT", "BTCUSDT", "HYPEUSDT"}, {"BTCUSDT", "ETHUSDT", "BAD"}, {"BTCUSDT", "ETHUSDT"}} {
		if _, err := ProjectAccountingJournalSegment(run, "", "", "initial", value, symbolsCase); !errors.Is(err, ErrReplayJournal) {
			t.Fatalf("invalid symbols accepted: %v", symbolsCase)
		}
	}
	for _, binding := range [][3]string{{"A" + run[1:], "", ""}, {run, "", cycle}, {run, cycle, ""}, {run, strings.ToUpper(cycle), plan}} {
		if _, err := ProjectAccountingJournalSegment(binding[0], binding[1], binding[2], "pretrade", value, symbols); !errors.Is(err, ErrReplayJournal) {
			t.Fatalf("invalid cycle binding accepted: %q", binding)
		}
	}
	if _, err := ProjectAccountingJournalSegment(run, cycle, plan, "hourly", value, symbols); !errors.Is(err, ErrReplayJournal) {
		t.Fatal("non-cycle role accepted cycle binding")
	}
}

// TestJournalLargeRatioAndZeroFailure проверяет крупную дробь и нулевой результат при ошибке.
func TestJournalLargeRatioAndZeroFailure(t *testing.T) {
	value, symbols := journalAccountingFixture(t)
	value.WalletUSDT = largeAccountingDecimal(t)
	value.FundingPostings = nil
	segment, err := ProjectAccountingJournalSegment(strings.Repeat("d", 64), "", "", "initial", value, symbols)
	if err != nil {
		t.Fatal(err)
	}
	var rows []journalWireRow
	if err := json.Unmarshal(segment.Bytes(), &rows); err != nil || len(rows) != 1 || len(rows[0].Accounting.WalletUSDT.Numerator) <= 128 {
		t.Fatalf("large ratio was not preserved: %s, %v", segment.Bytes(), err)
	}
	value.Valuations[2].Mark.Price = model.Decimal{}
	if got, err := ProjectAccountingJournalSegment(strings.Repeat("d", 64), "", "", "initial", value, symbols); !errors.Is(err, ErrReplayJournal) || !reflect.DeepEqual(got, ReplayJournalSegment{}) {
		t.Fatalf("late encoding failure returned partial segment: %+v, %v", got, err)
	}
}
