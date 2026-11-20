package model

import (
	"errors"
	"regexp"
	"time"
)

// ErrMarkPricePagination обозначает безопасный отказ проверки mark страницы/запроса.
var ErrMarkPricePagination = errors.New("invalid momentum mark price pagination")

// MarkPricePageRequest связывает страницу с точными включительными аргументами источника.
type MarkPricePageRequest struct {
	Environment, Symbol, IntervalCode string
	Start, End                        time.Time
}

// MarkPricePageAction описывает наблюдение без изменения durable состояния.
type MarkPricePageAction string

const (
	MarkPricePageContinue     MarkPricePageAction = "continue"
	MarkPricePageLowerReached MarkPricePageAction = "lower_reached"
	MarkPricePageEmpty        MarkPricePageAction = "empty"
	MarkPricePageGap          MarkPricePageAction = "gap"
	MarkPricePageUnconfirmed  MarkPricePageAction = "unconfirmed"
)

// MarkPricePageDecision содержит следующий end только для непрерывной закрытой страницы.
type MarkPricePageDecision struct {
	Action  MarkPricePageAction
	NextEnd time.Time
}

// NextMarkPricePage проверяет всю страницу и предлагает только безопасный следующий end.
func NextMarkPricePage(request MarkPricePageRequest, page MarkPricePage) (MarkPricePageDecision, error) {
	var empty MarkPricePageDecision
	var step int64
	switch request.IntervalCode {
	case "D":
		step = 86400000
	case "60":
		step = 3600000
	default:
		return empty, ErrMarkPricePagination
	}
	endpoint := "https://api-testnet.bybit.com/v5/market/mark-price-kline"
	if request.Environment == "mainnet" {
		endpoint = "https://api.bybit.com/v5/market/mark-price-kline"
	} else if request.Environment != "testnet" {
		return empty, ErrMarkPricePagination
	}
	if !regexp.MustCompile(`^[A-Z0-9]+USDT$`).MatchString(request.Symbol) ||
		!markPaginationMillis(request.Start) || !markPaginationMillis(request.End) ||
		request.Start.UnixMilli()%step != 0 || request.End.Before(request.Start) ||
		page.Environment != request.Environment || page.Symbol != request.Symbol || page.IntervalCode != request.IntervalCode ||
		page.Source != "bybit_v5" || page.Category != "linear" || page.Endpoint != endpoint ||
		!page.StartTime.Equal(request.Start) || !page.EndTime.Equal(request.End) ||
		!markPaginationMillis(page.ServerTime) || page.Candles == nil || len(page.Candles) > 200 {
		return empty, ErrMarkPricePagination
	}
	end, server := request.End.UnixMilli(), page.ServerTime.UnixMilli()
	newest := end / step * step
	expected := newest
	var oldest int64
	var gap bool
	for i, row := range page.Candles {
		open := row.OpenTime.UnixMilli()
		if !markPaginationMillis(row.OpenTime) || row.OpenTime.UTC().Year() > 294276 || open%step != 0 ||
			open < request.Start.UnixMilli() || open > end || open > server ||
			(i > 0 && !row.OpenTime.Before(page.Candles[i-1].OpenTime)) ||
			row.Confirmed != (server-open >= step) || !markPaginationPrices(row) {
			return empty, ErrMarkPricePagination
		}
		gap = gap || open != expected
		expected, oldest = open-step, open
	}
	switch {
	case server < newest || server-newest < step:
		return MarkPricePageDecision{Action: MarkPricePageUnconfirmed}, nil
	case len(page.Candles) == 0:
		return MarkPricePageDecision{Action: MarkPricePageEmpty}, nil
	case gap:
		return MarkPricePageDecision{Action: MarkPricePageGap}, nil
	case oldest == request.Start.UnixMilli():
		return MarkPricePageDecision{Action: MarkPricePageLowerReached}, nil
	default:
		return MarkPricePageDecision{Action: MarkPricePageContinue, NextEnd: time.UnixMilli(oldest - 1).UTC()}, nil
	}
}

// markPaginationMillis сохраняет положительный exact Unix-ms контракт metadata без ложного PG cap.
func markPaginationMillis(stamp time.Time) bool {
	ms := stamp.UnixMilli()
	return ms > 0 && time.UnixMilli(ms).Equal(stamp)
}

// markPaginationPrices проверяет точные положительные OHLC без округления или изменения входа.
func markPaginationPrices(row MarkPriceCandle) bool {
	var prices [4]Decimal
	for i, value := range []Decimal{row.Open, row.High, row.Low, row.Close} {
		raw, err := value.WireDecimal()
		if err != nil {
			return false
		}
		prices[i], err = ParsePrice(raw)
		if err != nil {
			return false
		}
	}
	for _, value := range []Decimal{prices[0], prices[3]} {
		high, _ := prices[1].Cmp(value)
		low, _ := prices[2].Cmp(value)
		if high < 0 || low > 0 {
			return false
		}
	}
	return true
}
