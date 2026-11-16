package bybit

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

const fundingHistoryPath = "/v5/market/funding/history"

var fundingSymbolPattern = regexp.MustCompile(`^[A-Z0-9]+USDT$`)

type fundingHistoryResult struct {
	Category string               `json:"category"`
	List     []fundingHistoryItem `json:"list"`
}

type fundingHistoryItem struct {
	Symbol    string `json:"symbol"`
	Rate      string `json:"fundingRate"`
	Timestamp string `json:"fundingRateTimestamp"`
}

// GetMomentumFundingPage загружает одну неподписанную страницу событий funding.
func (c *Client) GetMomentumFundingPage(ctx context.Context, symbol string, startMs, endMs int64) (model.FundingPage, error) {
	var empty model.FundingPage
	if !fundingSymbolPattern.MatchString(symbol) || startMs < 0 || endMs <= 0 || endMs < startMs {
		return empty, fmt.Errorf("get momentum funding page: %w", ErrInvalidArgument)
	}
	reader, environment, err := c.momentumMarketPageReader()
	if err != nil {
		return empty, fmt.Errorf("get momentum funding page: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return empty, fmt.Errorf("get momentum funding page: %w", err)
	}
	query := url.Values{
		"category": {linearCategory}, "symbol": {symbol}, "limit": {"200"},
		"startTime": {strconv.FormatInt(startMs, 10)}, "endTime": {strconv.FormatInt(endMs, 10)},
	}
	var result fundingHistoryResult
	serverMs, err := reader.get(ctx, fundingHistoryPath, query, &result)
	if err != nil {
		return empty, fmt.Errorf("get momentum funding page: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return empty, fmt.Errorf("get momentum funding page: %w", err)
	}
	if serverMs <= 0 || result.Category != linearCategory || result.List == nil || len(result.List) > 200 {
		return empty, fmt.Errorf("get momentum funding page: invalid result metadata")
	}
	events := make([]model.FundingEvent, 0, len(result.List))
	for _, row := range result.List {
		stamp, parseErr := strconv.ParseInt(row.Timestamp, 10, 64)
		if parseErr != nil || strconv.FormatInt(stamp, 10) != row.Timestamp || row.Symbol != symbol ||
			stamp < startMs || stamp > endMs || stamp > serverMs {
			return empty, fmt.Errorf("get momentum funding page: invalid funding event identity or timestamp")
		}
		rate, err := model.ParseRate(row.Rate)
		if err != nil {
			return empty, fmt.Errorf("get momentum funding page: invalid funding rate")
		}
		events = append(events, model.FundingEvent{FundingTime: time.UnixMilli(stamp).UTC(), Rate: rate})
	}
	slices.SortFunc(events, func(a, b model.FundingEvent) int { return b.FundingTime.Compare(a.FundingTime) })
	for i := 1; i < len(events); i++ {
		if events[i].FundingTime.Equal(events[i-1].FundingTime) {
			return empty, fmt.Errorf("get momentum funding page: duplicate funding timestamp")
		}
	}
	return model.FundingPage{
		Environment: environment, Source: "bybit_v5", Category: linearCategory, Endpoint: "https://" + c.baseURL.Host + fundingHistoryPath,
		Symbol: symbol, StartTime: time.UnixMilli(startMs).UTC(), EndTime: time.UnixMilli(endMs).UTC(),
		ServerTime: time.UnixMilli(serverMs).UTC(), Events: events,
	}, nil
}
