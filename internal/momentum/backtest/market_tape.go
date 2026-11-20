package backtest

import (
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
)

// ErrMarketTape обозначает неполную или несогласованную часовую ленту.
var ErrMarketTape = errors.New("invalid momentum hourly market tape")

// NextOpenReference содержит историческую цену открытия следующего бара. Это не подтверждение исполнения.
type NextOpenReference struct {
	Symbol string
	Price  model.Decimal
}

// MarketBoundary содержит информацию, доступную на одной часовой границе.
type MarketBoundary struct {
	At         time.Time
	Terminal   bool
	Marks      []model.LongMarkObservation
	TradeOpens []NextOpenReference
}

// BuildMarketBoundaries строит детерминированные границы из явно выбранного набора.
func BuildMarketBoundaries(dataset FrozenDataset, symbols []string, start, end time.Time) ([]MarketBoundary, error) {
	fail := func() ([]MarketBoundary, error) { return nil, ErrMarketTape }
	universe, err := config.CanonicalSymbols(symbols)
	if err != nil || start.IsZero() || end.IsZero() {
		return fail()
	}
	start, end = start.Round(0).UTC(), end.Round(0).UTC()
	hour := time.Hour
	if start.Minute() != 0 || start.Second() != 0 || start.Nanosecond() != 0 || end.Minute() != 0 || end.Second() != 0 || end.Nanosecond() != 0 || !start.Before(end) {
		return fail()
	}
	manifestRange := dataset.Manifest.Range
	prior := start.Add(-hour)
	if prior.Before(manifestRange.Start.UTC()) || end.After(manifestRange.End.UTC()) {
		return fail()
	}
	if data.CheckManifestContentBindings(dataset.Manifest, dataset.Artifacts) != nil {
		return fail()
	}
	marks, trades := make(map[string]map[int64]model.Decimal, 3), make(map[string]map[int64]model.Decimal, 3)
	for _, symbol := range universe {
		var markStream, tradeStream *data.ManifestStream
		for i := range dataset.Manifest.Streams {
			stream := &dataset.Manifest.Streams[i]
			if stream.Symbol != symbol || stream.IntervalCode != "60" {
				continue
			}
			switch stream.Kind {
			case "mark":
				if markStream != nil {
					return fail()
				}
				markStream = stream
			case "trade":
				if tradeStream != nil {
					return fail()
				}
				tradeStream = stream
			}
		}
		if markStream == nil || tradeStream == nil {
			return fail()
		}
		markRows, decodeErr := data.DecodeMarkPriceContent(dataset.Artifacts[markStream.Content.SHA256])
		if decodeErr != nil {
			return fail()
		}
		tradeRows, decodeErr := data.DecodeTradeCandleContent(dataset.Artifacts[tradeStream.Content.SHA256])
		if decodeErr != nil {
			return fail()
		}
		marks[symbol], trades[symbol] = make(map[int64]model.Decimal, len(markRows.Rows)), make(map[int64]model.Decimal, len(tradeRows.Rows))
		for _, row := range markRows.Rows {
			marks[symbol][row.OpenTime.Unix()] = row.Close
		}
		for _, row := range tradeRows.Rows {
			trades[symbol][row.OpenTime.Unix()] = row.Open
		}
	}
	out := make([]MarketBoundary, 0)
	for at := start; !at.After(end); at = at.Add(hour) {
		group := MarketBoundary{At: at, Terminal: at.Equal(end), Marks: make([]model.LongMarkObservation, 0, len(universe))}
		if !group.Terminal {
			group.TradeOpens = make([]NextOpenReference, 0, len(universe))
		}
		for _, symbol := range universe {
			price, ok := marks[symbol][at.Add(-hour).Unix()]
			if !ok {
				return fail()
			}
			group.Marks = append(group.Marks, model.LongMarkObservation{Symbol: symbol, ObservedAt: at, Price: price})
			if !group.Terminal {
				open, exists := trades[symbol][at.Unix()]
				if !exists {
					return fail()
				}
				group.TradeOpens = append(group.TradeOpens, NextOpenReference{Symbol: symbol, Price: open})
			}
		}
		out = append(out, group)
	}
	return out, nil
}
