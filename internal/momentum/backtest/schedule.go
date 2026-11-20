package backtest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
)

// ErrReplaySchedule обозначает несовместимые временные входы replay.
var ErrReplaySchedule = errors.New("invalid momentum replay schedule")

// ReplaySchedulePoint добавляет фактический funding и номинальную неделю к рынку.
type ReplaySchedulePoint struct {
	Market        MarketBoundary
	NominalWeekly bool
	Funding       []model.LongFundingAssessment
}

// BuildReplaySchedule связывает фактический funding с готовыми рыночными границами.
func BuildReplaySchedule(dataset FrozenDataset, symbols []string, start, end time.Time) ([]ReplaySchedulePoint, error) {
	market, err := BuildMarketBoundaries(dataset, symbols, start, end)
	if err != nil {
		return nil, errors.Join(ErrReplaySchedule, err)
	}
	points := make([]ReplaySchedulePoint, len(market))
	indices := make(map[time.Time]int, len(market))
	for i, boundary := range market {
		points[i].Market = boundary
		if !boundary.Terminal {
			indices[boundary.At] = i
		}
	}
	wanted := make(map[string]bool, len(market[0].Marks))
	for _, mark := range market[0].Marks {
		wanted[mark.Symbol] = false
	}
	for _, stream := range dataset.Manifest.Streams {
		if _, selected := wanted[stream.Symbol]; !selected || stream.Kind != "funding" {
			continue
		}
		if stream.Range.Start.After(start) || stream.Range.End.Before(end) {
			return nil, ErrReplaySchedule
		}
		content, err := data.DecodeFundingContent(dataset.Artifacts[stream.Content.SHA256])
		if err != nil {
			return nil, errors.Join(ErrReplaySchedule, err)
		}
		wanted[stream.Symbol] = true
		for _, event := range content.Events {
			at := event.FundingTime.Round(0).UTC()
			if at.Before(start) || !at.Before(end) {
				continue
			}
			index, found := indices[at]
			if !found {
				return nil, ErrReplaySchedule
			}
			for _, mark := range points[index].Market.Marks {
				if mark.Symbol == stream.Symbol {
					points[index].Funding = append(points[index].Funding, model.LongFundingAssessment{
						FundingID: replayFundingID(stream.Content.SHA256, stream.Symbol, at),
						Symbol:    stream.Symbol, Event: event, MarkPrice: mark.Price,
					})
				}
			}
		}
	}
	for _, present := range wanted {
		if !present {
			return nil, ErrReplaySchedule
		}
	}
	for i := range points {
		slices.SortFunc(points[i].Funding, func(a, b model.LongFundingAssessment) int {
			return strings.Compare(a.Symbol, b.Symbol)
		})
	}
	weekly, exists, err := FirstWeeklyBoundary(start, end)
	if err != nil {
		return nil, errors.Join(ErrReplaySchedule, err)
	}
	if exists {
		for at := weekly; at.Before(end); at = at.AddDate(0, 0, 7) {
			index, found := indices[at]
			if !found {
				return nil, ErrReplaySchedule
			}
			points[index].NominalWeekly = true
		}
	}
	return points, nil
}

// replayFundingID хеширует версию, неизменяемый content и идентичность события.
func replayFundingID(content, symbol string, at time.Time) string {
	key := strings.Join([]string{"momentum/replay-funding/v1", content, symbol, strconv.FormatInt(at.UnixMilli(), 10)}, "\n")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
