package model

import (
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fundingPaginationFixture создаёт валидную страницу с несколькими событиями.
func fundingPaginationFixture() (FundingPageRequest, FundingPage) {
	start := time.UnixMilli(1_000).UTC()
	end := time.UnixMilli(10_000).UTC()
	rate, _ := ParseRate("-0.00031")
	zero, _ := ParseRate("0")
	request := FundingPageRequest{Environment: "testnet", Symbol: "BTCUSDT", Start: start, End: end}
	page := FundingPage{
		Environment: "testnet", Source: "bybit_v5", Category: "linear",
		Endpoint: "https://api-testnet.bybit.com/v5/market/funding/history", Symbol: "BTCUSDT",
		StartTime: start, EndTime: end, ServerTime: time.UnixMilli(20_000).UTC(),
		Events: []FundingEvent{
			{FundingTime: time.UnixMilli(9_000).UTC(), Rate: rate},
			{FundingTime: time.UnixMilli(5_000).UTC(), Rate: zero},
			{FundingTime: time.UnixMilli(2_000).UTC(), Rate: rate},
		},
	}
	return request, page
}

// TestNextFundingPageActions проверяет решения и точность включительного курсора.
func TestNextFundingPageActions(t *testing.T) {
	request, page := fundingPaginationFixture()
	page.Events[2].FundingTime = time.UnixMilli(2_000).UTC()
	first, err := NextFundingPage(request, page)
	if err != nil || first.Action != FundingPageContinue || !first.NextEnd.Equal(time.UnixMilli(1_999).UTC()) {
		t.Fatalf("first page: got %+v, %v", first, err)
	}
	secondRequest := request
	secondRequest.End = first.NextEnd
	page.StartTime, page.EndTime = secondRequest.Start, secondRequest.End
	exact, parseErr := ParseRate("0.0000000000000000001234567890123456789")
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	page.Events = []FundingEvent{{FundingTime: first.NextEnd, Rate: exact}}
	second, err := NextFundingPage(secondRequest, page)
	if err != nil || second.Action != FundingPageContinue {
		t.Fatalf("second page: got %+v, %v", second, err)
	}
	wire, wireErr := page.Events[0].Rate.WireDecimal()
	if wireErr != nil || wire != "0.0000000000000000001234567890123456789" {
		t.Fatalf("exact rate changed: %q, %v", wire, wireErr)
	}
	page.Events[0].FundingTime = time.UnixMilli(2_000).UTC()
	assertFundingPaginationError(t, secondRequest, page)
	request, page = fundingPaginationFixture()
	page.Events = []FundingEvent{}
	got, err := NextFundingPage(request, page)
	if err != nil || got != (FundingPageDecision{Action: FundingPageEmpty}) {
		t.Fatalf("empty: got %+v, %v", got, err)
	}
	request, page = fundingPaginationFixture()
	page.Events = page.Events[:1]
	page.Events[0].FundingTime = request.Start
	got, err = NextFundingPage(request, page)
	if err != nil || got != (FundingPageDecision{Action: FundingPageLowerReached}) {
		t.Fatalf("lower: got %+v, %v", got, err)
	}
	request, page = fundingPaginationFixture()
	page.Events = page.Events[:1]
	page.Events[0].FundingTime = time.UnixMilli(1_001).UTC()
	got, err = NextFundingPage(request, page)
	want := FundingPageDecision{Action: FundingPageContinue, NextEnd: time.UnixMilli(1_000).UTC()}
	if err != nil || got != want || got.NextEnd.Location() != time.UTC {
		t.Fatalf("continue: got %+v, %v; want %+v", got, err, want)
	}
	page.Events = make([]FundingEvent, 200)
	page.Events[0] = FundingEvent{FundingTime: time.UnixMilli(10_000).UTC(), Rate: Zero()}
	for i := 1; i < len(page.Events); i++ {
		page.Events[i] = FundingEvent{FundingTime: time.UnixMilli(int64(10_000 - i)).UTC(), Rate: Zero()}
	}
	got, err = NextFundingPage(request, page)
	want = FundingPageDecision{Action: FundingPageContinue, NextEnd: time.UnixMilli(9_800).UTC()}
	if err != nil || got != want {
		t.Fatalf("200 events: got %+v, %v; want %+v", got, err, want)
	}
}

// TestNextFundingPageBoundaries проверяет epoch, равные границы и крайний timestamp.
func TestNextFundingPageBoundaries(t *testing.T) {
	request, page := fundingPaginationFixture()
	epoch := time.UnixMilli(0).UTC()
	request.Start = epoch
	page.StartTime = epoch
	page.Events = []FundingEvent{{FundingTime: time.UnixMilli(1).UTC(), Rate: Zero()}}
	got, err := NextFundingPage(request, page)
	if err != nil || got != (FundingPageDecision{Action: FundingPageSourceBoundary}) {
		t.Fatalf("source boundary: got %+v, %v", got, err)
	}
	request.Start, request.End = time.UnixMilli(500).UTC(), time.UnixMilli(500).UTC()
	page.StartTime, page.EndTime = request.Start, request.End
	page.Events = []FundingEvent{{FundingTime: request.Start, Rate: Zero()}}
	got, err = NextFundingPage(request, page)
	if err != nil || got != (FundingPageDecision{Action: FundingPageLowerReached}) {
		t.Fatalf("equal bounds: got %+v, %v", got, err)
	}
	request.Start, request.End = epoch, time.UnixMilli(1).UTC()
	page.StartTime, page.EndTime = request.Start, request.End
	page.Events = []FundingEvent{{FundingTime: epoch, Rate: Zero()}}
	got, err = NextFundingPage(request, page)
	if err != nil || got.Action != FundingPageLowerReached {
		t.Fatalf("epoch event: got %+v, %v", got, err)
	}
	upper := time.Date(294276, 12, 31, 23, 59, 59, 999_000_000, time.UTC)
	request.Start, request.End = upper.Add(-time.Millisecond), upper
	page.StartTime, page.EndTime, page.ServerTime = request.Start, request.End, upper
	page.Events = []FundingEvent{{FundingTime: upper, Rate: Zero()}}
	got, err = NextFundingPage(request, page)
	if err != nil || got.Action != FundingPageContinue || !got.NextEnd.Equal(request.Start) {
		t.Fatalf("upper time: got %+v, %v", got, err)
	}
}

// TestNextFundingPageTimezone сохраняет эквивалентные instants и исходные входы.
func TestNextFundingPageTimezone(t *testing.T) {
	request, page := fundingPaginationFixture()
	east := time.FixedZone("east", 7*3600)
	request.Start, request.End = request.Start.In(east), request.End.In(east)
	page.StartTime, page.EndTime = page.StartTime.In(east), page.EndTime.In(east)
	beforeRequest, beforePage := request, page
	beforePage.Events = append([]FundingEvent(nil), page.Events...)
	got, err := NextFundingPage(request, page)
	if err != nil || got.Action != FundingPageContinue || got.NextEnd.Location() != time.UTC {
		t.Fatalf("timezone: got %+v, %v", got, err)
	}
	if !reflect.DeepEqual(request, beforeRequest) || !reflect.DeepEqual(page, beforePage) {
		t.Fatal("input changed")
	}
}

// TestNextFundingPageNumericSymbol проверяет буквенно-цифровой шаблон символа USDT.
func TestNextFundingPageNumericSymbol(t *testing.T) {
	request, page := fundingPaginationFixture()
	request.Symbol, page.Symbol = "123USDT", "123USDT"
	got, err := NextFundingPage(request, page)
	if err != nil || got.Action != FundingPageContinue {
		t.Fatalf("numeric-base symbol: got %+v, %v", got, err)
	}
}

// TestNextFundingPageDecimalLimits проверяет ParseRate и WireDecimal на точных пределах.
func TestNextFundingPageDecimalLimits(t *testing.T) {
	raw := strings.Repeat("9", 63) + "." + strings.Repeat("9", 64)
	if len(raw) != 128 {
		t.Fatalf("fixture length=%d; want 128", len(raw))
	}
	rate, err := ParseRate(raw)
	if err != nil {
		t.Fatalf("maximum valid decimal rejected: %v", err)
	}
	wire, err := rate.WireDecimal()
	if err != nil || wire != raw {
		t.Fatalf("maximum decimal wire=%q, error=%v", wire, err)
	}
	if _, err = ParseRate(strings.Repeat("9", 64) + "." + strings.Repeat("9", 64)); err == nil {
		t.Fatal("129-character decimal accepted")
	}
	req, page := fundingPaginationFixture()
	page.Events = []FundingEvent{{FundingTime: req.End, Rate: rate}}
	if got, pageErr := NextFundingPage(req, page); pageErr != nil || got.Action != FundingPageContinue {
		t.Fatalf("maximum valid decimal page: got %+v, %v", got, pageErr)
	}
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(65), nil)
	tooPrecise := Decimal{value: new(big.Rat).SetFrac(big.NewInt(1), denominator)}
	tooPreciseWire, err := tooPrecise.WireDecimal()
	if err != nil || len(strings.Split(tooPreciseWire, ".")[1]) != 65 {
		t.Fatalf("65-place wire=%q, error=%v", tooPreciseWire, err)
	}
	if _, err = ParseRate(tooPreciseWire); err == nil {
		t.Fatal("ParseRate accepted 65 fractional digits")
	}
	page.Events = []FundingEvent{{FundingTime: req.End, Rate: tooPrecise}}
	assertFundingPaginationError(t, req, page)
}

// TestNextFundingPageRejectsInvalidInputs проверяет безопасный отказ для плохих запросов и страниц.
func TestNextFundingPageRejectsInvalidInputs(t *testing.T) {
	request, page := fundingPaginationFixture()
	tooLarge := Decimal{value: new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), 5000))}
	third := Decimal{value: new(big.Rat).SetFrac(big.NewInt(1), big.NewInt(3))}
	requestCases := map[string]func(*FundingPageRequest){
		"environment":     func(r *FundingPageRequest) { r.Environment = "prod" },
		"lowercase":       func(r *FundingPageRequest) { r.Symbol = "btcusdt" },
		"noncanonical":    func(r *FundingPageRequest) { r.Symbol = "BTC-USDT" },
		"suffix":          func(r *FundingPageRequest) { r.Symbol = "BTCUSD" },
		"epoch below":     func(r *FundingPageRequest) { r.Start = time.UnixMilli(-1) },
		"epoch end":       func(r *FundingPageRequest) { r.End = time.UnixMilli(0) },
		"negative end":    func(r *FundingPageRequest) { r.End = time.UnixMilli(-1) },
		"reversed":        func(r *FundingPageRequest) { r.Start, r.End = r.End, r.Start },
		"start precision": func(r *FundingPageRequest) { r.Start = r.Start.Add(time.Nanosecond) },
		"end precision":   func(r *FundingPageRequest) { r.End = r.End.Add(time.Nanosecond) },
		"above year":      func(r *FundingPageRequest) { r.End = time.Date(294277, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	for name, change := range requestCases {
		t.Run("request/"+name, func(t *testing.T) {
			bad := request
			change(&bad)
			assertFundingPaginationError(t, bad, page)
		})
	}
	pageCases := map[string]func(*FundingPage){
		"environment":         func(p *FundingPage) { p.Environment = "mainnet" },
		"endpoint":            func(p *FundingPage) { p.Endpoint = "https://api.bybit.com/v5/market/funding/history" },
		"endpoint query":      func(p *FundingPage) { p.Endpoint += "?symbol=BTCUSDT" },
		"source":              func(p *FundingPage) { p.Source = "foreign" },
		"category":            func(p *FundingPage) { p.Category = "inverse" },
		"symbol":              func(p *FundingPage) { p.Symbol = "ETHUSDT" },
		"start mismatch":      func(p *FundingPage) { p.StartTime = p.StartTime.Add(time.Millisecond) },
		"end mismatch":        func(p *FundingPage) { p.EndTime = p.EndTime.Add(-time.Millisecond) },
		"start precision":     func(p *FundingPage) { p.StartTime = p.StartTime.Add(time.Nanosecond) },
		"server zero":         func(p *FundingPage) { p.ServerTime = time.UnixMilli(0).UTC() },
		"server precision":    func(p *FundingPage) { p.ServerTime = p.ServerTime.Add(time.Nanosecond) },
		"nil events":          func(p *FundingPage) { p.Events = nil },
		"201 events":          func(p *FundingPage) { p.Events = make([]FundingEvent, 201) },
		"future":              func(p *FundingPage) { p.ServerTime = p.Events[0].FundingTime.Add(-time.Millisecond) },
		"outside":             func(p *FundingPage) { p.Events[0].FundingTime = request.End.Add(time.Millisecond) },
		"event precision":     func(p *FundingPage) { p.Events[1].FundingTime = p.Events[1].FundingTime.Add(time.Nanosecond) },
		"duplicate":           func(p *FundingPage) { p.Events[1] = p.Events[0] },
		"unordered":           func(p *FundingPage) { p.Events[0], p.Events[1] = p.Events[1], p.Events[0] },
		"unset rate":          func(p *FundingPage) { p.Events[1].Rate = Decimal{} },
		"nonterminating rate": func(p *FundingPage) { p.Events[1].Rate = third },
		"oversized rate":      func(p *FundingPage) { p.Events[1].Rate = tooLarge },
	}
	for name, change := range pageCases {
		t.Run("page/"+name, func(t *testing.T) {
			bad := page
			bad.Events = append([]FundingEvent(nil), page.Events...)
			change(&bad)
			assertFundingPaginationError(t, request, bad)
		})
	}
}

// assertFundingPaginationError проверяет нулевой результат, sentinel и неизменность входов.
func assertFundingPaginationError(t *testing.T, request FundingPageRequest, page FundingPage) {
	t.Helper()
	beforeRequest, beforePage := request, page
	beforePage.Events = append([]FundingEvent(nil), page.Events...)
	got, err := NextFundingPage(request, page)
	if !errors.Is(err, ErrFundingPagination) || got != (FundingPageDecision{}) {
		t.Fatalf("got %+v, %v; want zero decision and safe sentinel", got, err)
	}
	if !reflect.DeepEqual(request, beforeRequest) || !reflect.DeepEqual(page, beforePage) {
		t.Fatal("input changed")
	}
	if strings.Contains(err.Error(), request.Symbol) || strings.Contains(err.Error(), page.Endpoint) {
		t.Fatal("error exposes source values")
	}
}
