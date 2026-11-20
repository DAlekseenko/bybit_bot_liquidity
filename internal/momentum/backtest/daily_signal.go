package backtest

import (
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// ErrReplaySignal обозначает недоступный на cutoff дневной сигнал.
var ErrReplaySignal = errors.New("invalid momentum replay daily signal")

// ReplayDailySignal передаёт ROC30 только закрытый к заданному моменту префикс.
func ReplayDailySignal(dataset FrozenDataset, symbols []string, asOf time.Time) (map[string]bool, error) {
	canonical, err := config.CanonicalSymbols(symbols)
	if err != nil || asOf.IsZero() || asOf.Before(dataset.Manifest.Range.Start) || asOf.After(dataset.Manifest.Range.End) {
		return nil, ErrReplaySignal
	}
	if data.CheckManifestContentBindings(dataset.Manifest, dataset.Artifacts) != nil {
		return nil, ErrReplaySignal
	}
	asOf = asOf.Round(0).UTC()
	year, month, day := asOf.Date()
	expected := time.Date(year, month, day, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	series := make(map[string][]data.DailyClose, len(canonical))
	for _, symbol := range canonical {
		series[symbol] = nil
	}
	for _, stream := range dataset.Manifest.Streams {
		if _, selected := series[stream.Symbol]; !selected || stream.Kind != "trade" || stream.IntervalCode != "D" {
			continue
		}
		content, err := data.DecodeTradeCandleContent(dataset.Artifacts[stream.Content.SHA256])
		if err != nil {
			return nil, errors.Join(ErrReplaySignal, err)
		}
		for _, row := range content.Rows {
			if row.OpenTime.AddDate(0, 0, 1).After(asOf) {
				continue
			}
			series[stream.Symbol] = append(series[stream.Symbol], data.DailyClose{
				Symbol: stream.Symbol, OpenTime: row.OpenTime, Close: row.Close, Confirmed: row.Confirmed,
			})
		}
	}
	for _, symbol := range canonical {
		rows := series[symbol]
		if len(rows) == 0 || !rows[len(rows)-1].OpenTime.Equal(expected) {
			return nil, ErrReplaySignal
		}
	}
	active, err := planner.ROC30(canonical, series, asOf)
	if err != nil {
		return nil, errors.Join(ErrReplaySignal, err)
	}
	return active, nil
}
