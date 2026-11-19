package data

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"bybit_bot_liquidity/internal/momentum/model"
)

const fundingContentFormat = "momentum/funding-content/v1"

// ErrFundingContent обозначает непригодные входные данные артефакта funding.
var ErrFundingContent = errors.New("invalid momentum funding content")

var fundingContentSymbol = regexp.MustCompile(`^[A-Z0-9]+USDT$`)

// FundingContentInput задаёт содержимое единственного ряда для заданного окна.
type FundingContentInput struct {
	Environment, DatasetID, Source, Category, Symbol, Endpoint string
	Start, End                                                 time.Time
	Events                                                     []model.FundingEvent
}

type fundingContentWire struct {
	Format      string              `json:"format"`
	Environment string              `json:"environment"`
	DatasetID   string              `json:"dataset_id"`
	Source      string              `json:"source"`
	Category    string              `json:"category"`
	Symbol      string              `json:"symbol"`
	Endpoint    string              `json:"endpoint"`
	StartMs     int64               `json:"start_unix_ms"`
	EndMs       int64               `json:"end_unix_ms"`
	Events      []fundingContentRow `json:"events"`
}

type fundingContentRow struct {
	FundingTimeMs int64  `json:"funding_time_unix_ms"`
	Rate          string `json:"rate"`
}

// EncodeFundingContent возвращает JSON и SHA-256 точного содержимого ряда.
func EncodeFundingContent(input FundingContentInput) ([]byte, string, error) {
	if !validFundingContentIdentity(input) || !validFundingContentTime(input.Start) ||
		!validFundingContentTime(input.End) || !input.Start.UTC().Before(input.End.UTC()) {
		return nil, "", ErrFundingContent
	}

	start, end := input.Start.UnixMilli(), input.End.UnixMilli()
	events := make([]fundingContentRow, 0, len(input.Events))
	for _, event := range input.Events {
		if !validFundingContentTime(event.FundingTime) {
			return nil, "", ErrFundingContent
		}
		stamp := event.FundingTime.UnixMilli()
		if stamp < start || stamp >= end {
			return nil, "", ErrFundingContent
		}
		rate, err := event.Rate.WireDecimal()
		if err != nil {
			return nil, "", ErrFundingContent
		}
		if _, err := model.ParseRate(rate); err != nil {
			return nil, "", ErrFundingContent
		}
		events = append(events, fundingContentRow{FundingTimeMs: stamp, Rate: rate})
	}
	slices.SortFunc(events, compareFundingContentRows)
	for i := 1; i < len(events); i++ {
		if events[i-1].FundingTimeMs == events[i].FundingTimeMs {
			return nil, "", ErrFundingContent
		}
	}
	payload, err := json.Marshal(fundingContentWire{
		Format: fundingContentFormat, Environment: input.Environment, DatasetID: input.DatasetID,
		Source: input.Source, Category: input.Category, Symbol: input.Symbol, Endpoint: input.Endpoint,
		StartMs: start, EndMs: end, Events: events,
	})
	if err != nil {
		return nil, "", ErrFundingContent
	}
	digest := sha256.Sum256(payload)
	return payload, hex.EncodeToString(digest[:]), nil
}

// validFundingContentIdentity проверяет namespace и канонический URL Bybit.
func validFundingContentIdentity(input FundingContentInput) bool {
	if input.Source != "bybit_v5" || input.Category != "linear" || !fundingContentSymbol.MatchString(input.Symbol) {
		return false
	}
	id := input.DatasetID
	if !utf8.ValidString(id) || utf8.RuneCountInString(id) < 1 || utf8.RuneCountInString(id) > 128 ||
		strings.Trim(id, " ") != id || strings.ContainsRune(id, 0) {
		return false
	}
	const path = "/v5/market/funding/history"
	switch input.Environment {
	case "testnet":
		return input.Endpoint == "https://api-testnet.bybit.com"+path
	case "mainnet":
		return input.Endpoint == "https://api.bybit.com"+path
	default:
		return false
	}
}

// validFundingContentTime проверяет точный UTC-миллисекундный момент.
func validFundingContentTime(stamp time.Time) bool {
	year := stamp.UTC().Year()
	return year >= 1970 && year <= 294276 && stamp.Nanosecond()%int(time.Millisecond) == 0
}

// compareFundingContentRows упорядочивает строки по миллисекунде funding.
func compareFundingContentRows(a, b fundingContentRow) int {
	return cmp.Compare(a.FundingTimeMs, b.FundingTimeMs)
}
