package paper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// exactUTC ограничивает арифметику представимым каноническим UTC диапазоном.
func exactUTC(t time.Time) bool {
	return t.Location() == time.UTC && t.Year() >= 1970 && t.Year() <= 9999
}

// publicConfig допускает только публичный paper Testnet источник.
func publicConfig(c config.Config) bool {
	v := c.Values()
	return v.Mode == "paper" && v.ModelID == "roc30" && v.DataSource.Kind == "bybit_testnet_public" && v.DataSource.Environment == "testnet" && len(v.Symbols) > 0
}

// fundingSpan вычисляет первый момент и count без массива и duration overflow.
func fundingSpan(r SourceRequest, symbol string) (int64, int64) {
	s := r.Profile.FundingSchedules[symbol]
	step := s.Interval.Milliseconds()
	remainder := (r.FundingAfter.UnixMilli() - s.Anchor.UnixMilli()) % step
	if remainder < 0 {
		remainder += step
	}
	first := r.FundingAfter.UnixMilli() + step - remainder
	if first > r.Run.ScheduledAt.UnixMilli() {
		return first, 0
	}
	return first, (r.Run.ScheduledAt.UnixMilli()-first)/step + 1
}

// sourceRequest проверяет count всех календарей до клонирования и RPC.
func sourceRequest(r SourceRequest) error {
	if r.Now == nil || !publicConfig(r.Run.Config) || !exactUTC(r.Run.ScheduledAt) || !exactUTC(r.FundingAfter) || r.FundingAfter.After(r.Run.ScheduledAt) || !exactUTC(r.Now()) || r.Run.RunID == "" || r.Run.AccountID == "" || r.Run.StrategyID != r.Run.Config.Values().StrategyID {
		return ErrPaperObservation
	}
	if r.Purpose.Kind != "rebalance" && r.Purpose.Kind != "close-all" || r.Purpose.Kind == "rebalance" && (r.DailyBars < 1 || r.DailyBars > 200 || r.Purpose.CommandID != "" || r.Purpose.ControlRevision != 0) || r.Purpose.Kind == "close-all" && (r.DailyBars != 0 || r.Purpose.CommandID == "" || r.Purpose.ControlRevision == 0) {
		return ErrPaperObservation
	}
	if r.Profile.DataDeadline <= 0 || !exactUTC(r.Run.ScheduledAt.Add(r.Profile.DataDeadline)) {
		return ErrPaperObservation
	}
	total := int64(0)
	for symbol, schedule := range r.Profile.FundingSchedules {
		if !exactUTC(schedule.Anchor) || schedule.Anchor.Nanosecond()%int(time.Millisecond) != 0 || schedule.Interval < time.Millisecond || schedule.Interval%time.Millisecond != 0 {
			return ErrPaperObservation
		}
		_, count := fundingSpan(r, symbol)
		if count > paperwire.MaxExpectedFundingEvents-total {
			return paperwire.ErrPaperResourceLimit
		}
		total += count
	}
	symbols := r.Run.Config.Values().Symbols
	if len(symbols) != len(r.Profile.FundingSchedules) {
		return ErrPaperObservation
	}
	for _, symbol := range symbols {
		if _, ok := r.Profile.FundingSchedules[symbol]; !ok {
			return ErrPaperObservation
		}
	}
	if _, _, err := paperwire.EncodeProfile(r.Profile); err != nil {
		return errors.Join(ErrPaperObservation, err)
	}
	return nil
}

// expectedFunding разворачивает только предварительно ограниченный календарь.
func expectedFunding(r SourceRequest, symbol string) []time.Time {
	first, count := fundingSpan(r, symbol)
	result := make([]time.Time, int(count))
	for i := range result {
		result[i] = time.UnixMilli(first + int64(i)*r.Profile.FundingSchedules[symbol].Interval.Milliseconds()).UTC()
	}
	return result
}

// previousHour задаёт последнюю целиком закрытую часовую свечу перед моментом.
func previousHour(at time.Time) time.Time { return at.Truncate(time.Hour).Add(-time.Hour) }

// dailyBounds сохраняет точные включительные границы дневного запроса.
func dailyBounds(at time.Time, count int) (int64, int64) {
	end := at.Truncate(24 * time.Hour).UnixMilli()
	return end - int64(count)*86400000, end - 1
}

// fresh отвергает будущие и устаревшие часы источника без host clock.
func fresh(source, captured time.Time, age time.Duration) bool {
	return exactUTC(source) && exactUTC(captured) && !source.After(captured) && captured.Sub(source) <= age
}

// validateTrade проверяет происхождение, непрерывность, OHLC и единицы raw rows.
func validateTrade(w paperwire.TradeWindow, symbol, interval string, at time.Time, count int) error {
	step := time.Hour
	start, end := previousHour(at).UnixMilli(), previousHour(at).Add(time.Hour-time.Millisecond).UnixMilli()
	if interval == "D" {
		step = 24 * time.Hour
		start, end = dailyBounds(at, count)
	}
	p := w.Page
	if w.Symbol != symbol || w.Interval != interval || w.StartMS != start || w.EndMS != end || start <= 0 || p.Environment != "testnet" || p.Source != "bybit_v5" || p.Category != "linear" || p.Endpoint != testnetOrigin+"/v5/market/kline" || p.ServerTime.Nanosecond()%int(time.Millisecond) != 0 || len(p.Candles) != count {
		return ErrPaperObservation
	}
	for i, c := range p.Candles {
		expected := time.UnixMilli(start + int64(count-1-i)*step.Milliseconds()).UTC()
		if c.Symbol != symbol || c.IntervalCode != interval || !exactUTC(c.OpenTime) || !c.OpenTime.Equal(expected) || !c.Confirmed || c.OpenTime.Add(step).After(p.ServerTime) || c.OpenTime.Add(step).After(at) {
			return ErrPaperObservation
		}
		for j, value := range []model.Decimal{c.Open, c.High, c.Low, c.Close, c.Volume, c.Turnover} {
			cmp, err := value.Cmp(model.Zero())
			if err != nil || cmp < 0 || j < 4 && cmp == 0 {
				return ErrPaperObservation
			}
		}
		for _, value := range []model.Decimal{c.Open, c.Close, c.Low} {
			cmp, _ := c.High.Cmp(value)
			if cmp < 0 {
				return ErrPaperObservation
			}
		}
		for _, value := range []model.Decimal{c.Open, c.Close} {
			cmp, _ := c.Low.Cmp(value)
			if cmp > 0 {
				return ErrPaperObservation
			}
		}
	}
	return nil
}

// ValidateCycleObservation проверяет raw pages без I/O и возвращает отдельные проекции.
func ValidateCycleObservation(r SourceRequest, o paperwire.Observation) (MarketSlice, error) {
	var empty MarketSlice
	if err := sourceRequest(r); err != nil {
		return empty, err
	}
	if err := observationBudget(o); err != nil {
		return empty, err
	}
	if o.DailyBars != r.DailyBars || o.SourceClock.Nanosecond()%int(time.Millisecond) != 0 || o.SourceClock.Before(r.Run.ScheduledAt) || !fresh(o.SourceClock, o.CapturedAt, r.Profile.SourceMaxAge) || !fresh(o.CapturedAt, r.Now(), r.Profile.SourceMaxAge) || o.CapturedAt.Before(r.Run.ScheduledAt) || !o.CapturedAt.Before(r.Run.ScheduledAt.Add(r.Profile.DataDeadline)) {
		return empty, ErrPaperObservation
	}
	out := MarketSlice{DailySeries: make(map[string][]data.DailyClose), Marks: make(map[string]model.LongMarkObservation), ReferencePrices: make(map[string]model.Decimal)}
	tradeIndex, fundingIndex := 0, 0
	usedMarks := make(map[int]bool)
	for _, symbol := range r.Run.Config.Values().Symbols {
		if r.DailyBars > 0 {
			if tradeIndex >= len(o.TradeWindows) {
				return empty, ErrPaperObservation
			}
			w := o.TradeWindows[tradeIndex]
			if validateTrade(w, symbol, "D", r.Run.ScheduledAt, r.DailyBars) != nil || !fresh(w.Page.ServerTime, o.CapturedAt, r.Profile.SourceMaxAge) {
				return empty, ErrPaperObservation
			}
			daily := make([]data.DailyClose, r.DailyBars)
			for i, c := range w.Page.Candles {
				daily[len(daily)-1-i] = data.DailyClose{Symbol: symbol, OpenTime: c.OpenTime, Close: c.Close, Confirmed: c.Confirmed}
			}
			out.DailySeries[symbol] = daily
			tradeIndex++
		}
		if tradeIndex >= len(o.TradeWindows) {
			return empty, ErrPaperObservation
		}
		w := o.TradeWindows[tradeIndex]
		if validateTrade(w, symbol, "60", r.Run.ScheduledAt, 1) != nil || !fresh(w.Page.ServerTime, o.CapturedAt, r.Profile.SourceMaxAge) {
			return empty, ErrPaperObservation
		}
		out.ReferencePrices[symbol] = w.Page.Candles[0].Close
		tradeIndex++
		mark, err := savedMark(r, o, symbol, r.Run.ScheduledAt, usedMarks)
		if err != nil {
			return empty, err
		}
		out.Marks[symbol] = mark
		expected := expectedFunding(r, symbol)
		var events []model.FundingEvent
		start, end := r.FundingAfter.UnixMilli()+1, r.Run.ScheduledAt.UnixMilli()
		for start <= end {
			if fundingIndex >= len(o.FundingPages) {
				return empty, ErrPaperObservation
			}
			p := o.FundingPages[fundingIndex]
			decision, e := model.NextFundingPage(model.FundingPageRequest{Environment: "testnet", Symbol: symbol, Start: time.UnixMilli(start).UTC(), End: time.UnixMilli(end).UTC()}, p)
			if e != nil || !fresh(p.ServerTime, o.CapturedAt, r.Profile.SourceMaxAge) {
				return empty, ErrPaperObservation
			}
			if len(events) > paperwire.MaxExpectedFundingEvents-len(p.Events) {
				return empty, paperwire.ErrPaperResourceLimit
			}
			events = append(events, p.Events...)
			fundingIndex++
			if decision.Action != model.FundingPageContinue {
				break
			}
			end = decision.NextEnd.UnixMilli()
		}
		sort.Slice(events, func(i, j int) bool { return events[i].FundingTime.Before(events[j].FundingTime) })
		if len(expected) == 0 {
			if len(events) != 0 {
				return empty, ErrPaperObservation
			}
		} else {
			coverage, e := model.CompareFundingCoverage(expected, events)
			if e != nil || len(coverage.Missing)+len(coverage.Unexpected) != 0 {
				return empty, ErrPaperObservation
			}
		}
		for _, event := range events {
			m, e := savedMark(r, o, symbol, event.FundingTime, usedMarks)
			if e != nil {
				return empty, e
			}
			raw, _ := json.Marshal([]string{"momentum/paper-funding/v1", "paper", symbol, event.FundingTime.UTC().Format(time.RFC3339Nano)})
			hash := sha256.Sum256(raw)
			out.Funding = append(out.Funding, model.LongFundingAssessment{FundingID: hex.EncodeToString(hash[:]), Symbol: symbol, Event: event, MarkPrice: m.Price})
		}
	}
	if tradeIndex != len(o.TradeWindows) || fundingIndex != len(o.FundingPages) || len(usedMarks) != len(o.MarkPages) {
		return empty, ErrPaperObservation
	}
	sort.Slice(out.Funding, func(i, j int) bool {
		a, b := out.Funding[i], out.Funding[j]
		if a.Event.FundingTime.Equal(b.Event.FundingTime) {
			return a.Symbol < b.Symbol
		}
		return a.Event.FundingTime.Before(b.Event.FundingTime)
	})
	return out, nil
}

// savedMark требует отдельную полную страницу mark с точной request identity.
func savedMark(r SourceRequest, o paperwire.Observation, symbol string, at time.Time, used map[int]bool) (model.LongMarkObservation, error) {
	var result model.LongMarkObservation
	open := previousHour(at)
	found := false
	for i, p := range o.MarkPages {
		if p.Symbol != symbol || !p.StartTime.Equal(open) {
			continue
		}
		if found || !used[i] && i != len(used) {
			return result, ErrPaperObservation
		}
		found = true
		decision, err := model.NextMarkPricePage(model.MarkPricePageRequest{Environment: "testnet", Symbol: symbol, IntervalCode: "60", Start: open, End: open.Add(time.Hour - time.Millisecond)}, p)
		if err != nil || decision.Action != model.MarkPricePageLowerReached || len(p.Candles) != 1 || !fresh(p.ServerTime, o.CapturedAt, r.Profile.SourceMaxAge) || at.Sub(open.Add(time.Hour)) > r.Profile.Parameters.MarkMaxAge {
			return result, ErrPaperObservation
		}
		result = model.LongMarkObservation{Symbol: symbol, ObservedAt: open.Add(time.Hour), Price: p.Candles[0].Close}
		used[i] = true
	}
	if !found {
		return result, ErrPaperObservation
	}
	return result, nil
}
