package paperwire

import "bybit_bot_liquidity/internal/momentum/model"

type candleWire struct {
	Symbol, IntervalCode                     string
	OpenTime                                 instant
	Open, High, Low, Close, Volume, Turnover amount
	Confirmed                                bool
}
type candlePageWire struct {
	Environment, Source, Category, Endpoint string
	ServerTime                              instant
	Candles                                 []candleWire
}
type tradeWire struct {
	Symbol, Interval string
	StartMS, EndMS   int64
	Page             candlePageWire
}
type markWire struct {
	OpenTime               instant
	Open, High, Low, Close amount
	Confirmed              bool
}
type markPageWire struct {
	Environment, Source, Category, Endpoint, Symbol, IntervalCode string
	StartTime, EndTime, ServerTime                                instant
	Candles                                                       []markWire
}
type eventWire struct {
	FundingTime instant
	Rate        amount
}
type fundingPageWire struct {
	Environment, Source, Category, Endpoint, Symbol string
	StartTime, EndTime, ServerTime                  instant
	Events                                          []eventWire
}
type observationWire struct {
	CapturedAt, SourceClock instant
	DailyBars               int
	TradeWindows            []tradeWire
	MarkPages               []markPageWire
	FundingPages            []fundingPageWire
}

// mapValues преобразует элементы в отдельный срез, сохраняя исходный порядок.
func mapValues[A, B any](values []A, convert func(A) B) []B {
	if values == nil {
		return nil
	}
	out := make([]B, len(values))
	for i, value := range values {
		out[i] = convert(value)
	}
	return out
}

// observationOut копирует provenance, bounds и каждую исходную строку без нормализации.
func observationOut(o Observation) observationWire {
	trades := mapValues(o.TradeWindows, func(w TradeWindow) tradeWire {
		p := w.Page
		rows := mapValues(p.Candles, func(c model.Candle) candleWire {
			return candleWire{c.Symbol, c.IntervalCode, instant{c.OpenTime}, amount{c.Open}, amount{c.High}, amount{c.Low}, amount{c.Close}, amount{c.Volume}, amount{c.Turnover}, c.Confirmed}
		})
		return tradeWire{w.Symbol, w.Interval, w.StartMS, w.EndMS, candlePageWire{p.Environment, p.Source, p.Category, p.Endpoint, instant{p.ServerTime}, rows}}
	})
	marks := mapValues(o.MarkPages, func(p model.MarkPricePage) markPageWire {
		rows := mapValues(p.Candles, func(c model.MarkPriceCandle) markWire {
			return markWire{instant{c.OpenTime}, amount{c.Open}, amount{c.High}, amount{c.Low}, amount{c.Close}, c.Confirmed}
		})
		return markPageWire{p.Environment, p.Source, p.Category, p.Endpoint, p.Symbol, p.IntervalCode, instant{p.StartTime}, instant{p.EndTime}, instant{p.ServerTime}, rows}
	})
	funding := mapValues(o.FundingPages, func(p model.FundingPage) fundingPageWire {
		rows := mapValues(p.Events, func(e model.FundingEvent) eventWire { return eventWire{instant{e.FundingTime}, amount{e.Rate}} })
		return fundingPageWire{p.Environment, p.Source, p.Category, p.Endpoint, p.Symbol, instant{p.StartTime}, instant{p.EndTime}, instant{p.ServerTime}, rows}
	})
	return observationWire{instant{o.CapturedAt}, instant{o.SourceClock}, o.DailyBars, trades, marks, funding}
}

// observationIn восстанавливает полную отдельную копию исходных страниц.
func observationIn(o observationWire) Observation {
	trades := mapValues(o.TradeWindows, func(w tradeWire) TradeWindow {
		p := w.Page
		rows := mapValues(p.Candles, func(c candleWire) model.Candle {
			return model.Candle{Symbol: c.Symbol, IntervalCode: c.IntervalCode, OpenTime: c.OpenTime.Time, Open: c.Open.value, High: c.High.value,
				Low: c.Low.value, Close: c.Close.value, Volume: c.Volume.value, Turnover: c.Turnover.value, Confirmed: c.Confirmed}
		})
		return TradeWindow{w.Symbol, w.Interval, w.StartMS, w.EndMS, model.CandlePage{Environment: p.Environment, Source: p.Source, Category: p.Category, Endpoint: p.Endpoint, ServerTime: p.ServerTime.Time, Candles: rows}}
	})
	marks := mapValues(o.MarkPages, func(p markPageWire) model.MarkPricePage {
		rows := mapValues(p.Candles, func(c markWire) model.MarkPriceCandle {
			return model.MarkPriceCandle{OpenTime: c.OpenTime.Time, Open: c.Open.value, High: c.High.value, Low: c.Low.value, Close: c.Close.value, Confirmed: c.Confirmed}
		})
		return model.MarkPricePage{Environment: p.Environment, Source: p.Source, Category: p.Category, Endpoint: p.Endpoint, Symbol: p.Symbol, IntervalCode: p.IntervalCode,
			StartTime: p.StartTime.Time, EndTime: p.EndTime.Time, ServerTime: p.ServerTime.Time, Candles: rows}
	})
	funding := mapValues(o.FundingPages, func(p fundingPageWire) model.FundingPage {
		rows := mapValues(p.Events, func(e eventWire) model.FundingEvent {
			return model.FundingEvent{FundingTime: e.FundingTime.Time, Rate: e.Rate.value}
		})
		return model.FundingPage{Environment: p.Environment, Source: p.Source, Category: p.Category, Endpoint: p.Endpoint, Symbol: p.Symbol,
			StartTime: p.StartTime.Time, EndTime: p.EndTime.Time, ServerTime: p.ServerTime.Time, Events: rows}
	})
	return Observation{o.CapturedAt.Time, o.SourceClock.Time, o.DailyBars, trades, marks, funding}
}
