package paper

import (
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

type tradeRequestKey struct {
	symbol, interval string
	start, end       int64
}

type sourcePages struct {
	trade   map[tradeRequestKey]paperwire.TradeWindow
	funding map[model.FundingPageRequest]model.FundingPage
	mark    map[model.MarkPricePageRequest]model.MarkPricePage
}

// indexSourcePages связывает полные ключи запросов со страницами без перестановки строк.
func indexSourcePages(o paperwire.Observation) (sourcePages, error) {
	pages := sourcePages{
		trade:   make(map[tradeRequestKey]paperwire.TradeWindow, len(o.TradeWindows)),
		funding: make(map[model.FundingPageRequest]model.FundingPage, len(o.FundingPages)),
		mark:    make(map[model.MarkPricePageRequest]model.MarkPricePage, len(o.MarkPages)),
	}
	for _, w := range o.TradeWindows {
		key := tradeRequestKey{w.Symbol, w.Interval, w.StartMS, w.EndMS}
		if _, exists := pages.trade[key]; exists {
			return sourcePages{}, ErrPaperObservation
		}
		pages.trade[key] = w
	}
	for _, p := range o.FundingPages {
		key := model.FundingPageRequest{Environment: "testnet", Symbol: p.Symbol, Start: p.StartTime.UTC(), End: p.EndTime.UTC()}
		if _, exists := pages.funding[key]; exists {
			return sourcePages{}, ErrPaperObservation
		}
		pages.funding[key] = p
	}
	for _, p := range o.MarkPages {
		key := model.MarkPricePageRequest{Environment: "testnet", Symbol: p.Symbol, IntervalCode: p.IntervalCode, Start: p.StartTime.UTC(), End: p.EndTime.UTC()}
		if _, exists := pages.mark[key]; exists {
			return sourcePages{}, ErrPaperObservation
		}
		pages.mark[key] = p
	}
	return pages, nil
}

// savedMark проверяет свежесть одной сохранённой страницы для каждого момента оценки.
func (pages sourcePages) savedMark(r SourceRequest, captured time.Time, symbol string, at time.Time, used map[model.MarkPricePageRequest]bool) (model.LongMarkObservation, error) {
	open := previousHour(at).UTC()
	key := model.MarkPricePageRequest{Environment: "testnet", Symbol: symbol, IntervalCode: "60", Start: open, End: open.Add(time.Hour - time.Millisecond)}
	p := pages.mark[key]
	decision, err := model.NextMarkPricePage(key, p)
	if err != nil || decision.Action != model.MarkPricePageLowerReached || len(p.Candles) != 1 || !fresh(p.ServerTime, captured, r.Profile.SourceMaxAge) || at.Sub(open.Add(time.Hour)) > r.Profile.Parameters.MarkMaxAge {
		return model.LongMarkObservation{}, ErrPaperObservation
	}
	used[key] = true
	return model.LongMarkObservation{Symbol: symbol, ObservedAt: open.Add(time.Hour), Price: p.Candles[0].Close}, nil
}
