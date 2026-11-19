package model

import (
	"errors"
	"strings"
	"time"
)

// ErrFundingPagination обозначает недопустимый запрос или страницу funding.
var ErrFundingPagination = errors.New("invalid funding pagination input")

// FundingPageAction описывает результат обработки одной страницы funding.
type FundingPageAction string

const (
	FundingPageContinue       FundingPageAction = "continue"
	FundingPageLowerReached   FundingPageAction = "lower_reached"
	FundingPageEmpty          FundingPageAction = "empty"
	FundingPageSourceBoundary FundingPageAction = "source_boundary"
)

// FundingPageRequest задаёт включительное окно запроса истории funding.
type FundingPageRequest struct {
	Environment, Symbol string
	Start, End          time.Time
}

// FundingPageDecision задаёт действие и следующий конец запроса при continue.
type FundingPageDecision struct {
	Action  FundingPageAction
	NextEnd time.Time
}

// NextFundingPage проверяет страницу и вычисляет курсор от её раннего события.
func NextFundingPage(request FundingPageRequest, page FundingPage) (FundingPageDecision, error) {
	if !validFundingPageRequest(request) || !validFundingPage(request, page) {
		return FundingPageDecision{}, ErrFundingPagination
	}
	if len(page.Events) == 0 {
		return FundingPageDecision{Action: FundingPageEmpty}, nil
	}
	oldest := page.Events[len(page.Events)-1].FundingTime.UTC()
	start := request.Start.UTC()
	if oldest.Equal(start) {
		return FundingPageDecision{Action: FundingPageLowerReached}, nil
	}
	next := oldest.Add(-time.Millisecond)
	if next.Equal(time.UnixMilli(0).UTC()) {
		return FundingPageDecision{Action: FundingPageSourceBoundary}, nil
	}
	if !next.Before(request.End.UTC()) || next.Before(start) {
		return FundingPageDecision{}, ErrFundingPagination
	}
	return FundingPageDecision{Action: FundingPageContinue, NextEnd: next}, nil
}

// validFundingPageRequest проверяет environment, symbol и точные границы запроса.
func validFundingPageRequest(request FundingPageRequest) bool {
	if request.Environment != "mainnet" && request.Environment != "testnet" || !canonicalFundingSymbol(request.Symbol) {
		return false
	}
	return validFundingCoverageTime(request.Start) && validFundingCoverageTime(request.End) &&
		!request.Start.Before(time.UnixMilli(0)) && request.End.After(time.UnixMilli(0)) &&
		!request.Start.After(request.End)
}

// validFundingPage проверяет provenance, границы и события без изменения входа.
func validFundingPage(request FundingPageRequest, page FundingPage) bool {
	if page.Environment != request.Environment || page.Symbol != request.Symbol ||
		page.Source != "bybit_v5" || page.Category != "linear" || page.Events == nil || len(page.Events) > 200 ||
		!page.StartTime.Equal(request.Start) || !page.EndTime.Equal(request.End) ||
		!validFundingCoverageTime(page.StartTime) || !validFundingCoverageTime(page.EndTime) ||
		!validFundingCoverageTime(page.ServerTime) || !page.ServerTime.After(time.UnixMilli(0)) ||
		page.ServerTime.Nanosecond()%int(time.Millisecond) != 0 || !validFundingEndpoint(page.Environment, page.Endpoint) {
		return false
	}
	var later time.Time
	for i, event := range page.Events {
		stamp := event.FundingTime
		if !validFundingCoverageTime(stamp) || stamp.Before(request.Start) || stamp.After(request.End) ||
			stamp.After(page.ServerTime) || i > 0 && !stamp.Before(later) {
			return false
		}
		raw, err := event.Rate.WireDecimal()
		if err != nil {
			return false
		}
		if _, err = ParseRate(raw); err != nil {
			return false
		}
		later = stamp
	}
	return true
}

// canonicalFundingSymbol принимает только uppercase USDT symbols с буквенно-цифровой базой.
func canonicalFundingSymbol(symbol string) bool {
	if len(symbol) <= len("USDT") || !strings.HasSuffix(symbol, "USDT") {
		return false
	}
	for _, r := range symbol[:len(symbol)-len("USDT")] {
		if r < 'A' || r > 'Z' {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// validFundingEndpoint проверяет точный Bybit V5 funding history URL среды.
func validFundingEndpoint(environment, endpoint string) bool {
	base := "https://api.bybit.com"
	if environment == "testnet" {
		base = "https://api-testnet.bybit.com"
	}
	return endpoint == base+"/v5/market/funding/history"
}
