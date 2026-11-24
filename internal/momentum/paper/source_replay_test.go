package paper

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// copyReplay сохраняет исходный порядок страниц и копирует каждый массив строк.
func copyReplay(o paperwire.Observation) paperwire.Observation {
	o.TradeWindows = slices.Clone(o.TradeWindows)
	for i, w := range o.TradeWindows {
		o.TradeWindows[i].Page.Candles = slices.Clone(w.Page.Candles)
	}
	o.FundingPages = slices.Clone(o.FundingPages)
	for i, p := range o.FundingPages {
		o.FundingPages[i].Events = slices.Clone(p.Events)
	}
	o.MarkPages = slices.Clone(o.MarkPages)
	for i, p := range o.MarkPages {
		o.MarkPages[i].Candles = slices.Clone(p.Candles)
	}
	return o
}

// checkReplay проверяет неизменность всех переданных страниц даже при отказе.
func checkReplay(t *testing.T, r SourceRequest, o paperwire.Observation) (MarketSlice, error) {
	t.Helper()
	before := copyReplay(o)
	m, err := ValidateCycleObservation(r, o)
	if !reflect.DeepEqual(before, o) {
		t.Fatal("изменены входные страницы или строки")
	}
	return m, err
}

// rejectReplay требует типизированный отказ без частично построенного среза.
func rejectReplay(t *testing.T, r SourceRequest, o paperwire.Observation) {
	t.Helper()
	m, err := checkReplay(t, r, o)
	if !errors.Is(err, ErrPaperObservation) || !reflect.DeepEqual(m, MarketSlice{}) {
		t.Fatalf("ожидался пустой срез и ErrPaperObservation, получены %+v, %v", m, err)
	}
}

// replayFixture читает публичный fake с короткими funding-страницами и разными ценами.
func replayFixture(t *testing.T, r *SourceRequest, f *publicFake) paperwire.Observation {
	t.Helper()
	f.edit = func(q *http.Request, result map[string]any) {
		if q.URL.Path == "/v5/market/funding/history" {
			rows := result["list"].([]any)
			result["list"] = rows[:min(2, len(rows))]
		}
		if q.URL.Path == "/v5/market/kline" && q.URL.Query().Get("interval") == "60" {
			for _, row := range result["list"].([]any) {
				ohlc := row.([]string)
				ohlc[2], ohlc[4] = "12.75", "12.75"
			}
		}
	}
	o, err := ReadCycleObservation(t.Context(), *r)
	if err != nil {
		t.Fatal(err)
	}
	r.Transport = nil
	r.Now = func() time.Time { return o.CapturedAt }
	return o
}

// encodeReplay сохраняет канонические байты и проверяет порядок исходных данных.
func encodeReplay(t *testing.T, o paperwire.Observation) []byte {
	t.Helper()
	before := copyReplay(o)
	b, err := paperwire.EncodeObservation(o)
	if err != nil || !reflect.DeepEqual(before, o) {
		t.Fatal("кодирование изменило вход или вернуло ошибку", err)
	}
	return b
}

// TestSavedObservationReplay сверяет результат офлайн для каждой и всех перестановок.
func TestSavedObservationReplay(t *testing.T) {
	r, f := sourceFixture(t, "ALT123USDT")
	raw := replayFixture(t, &r, f)
	calls := f.calls
	wire := encodeReplay(t, raw)
	decoded, err := paperwire.DecodeObservation(wire)
	if err != nil {
		t.Fatal(err)
	}
	if raw.TradeWindows[0].Interval == decoded.TradeWindows[0].Interval ||
		raw.FundingPages[0].EndTime.Equal(decoded.FundingPages[0].EndTime) ||
		raw.MarkPages[0].StartTime.Equal(decoded.MarkPages[0].StartTime) {
		t.Fatal("ожидалась перестановка каждого семейства")
	}
	want, err := checkReplay(t, r, raw)
	if err != nil {
		t.Fatal(err)
	}
	checkReplayValues(t, r, raw, want)
	trade, funding, mark := raw, raw, raw
	trade.TradeWindows = decoded.TradeWindows
	funding.FundingPages = decoded.FundingPages
	mark.MarkPages = decoded.MarkPages
	reversed := copyReplay(raw)
	slices.Reverse(reversed.TradeWindows)
	slices.Reverse(reversed.FundingPages)
	slices.Reverse(reversed.MarkPages)
	for _, o := range []paperwire.Observation{trade, funding, mark, decoded, reversed, reversed} {
		got, err := checkReplay(t, r, o)
		if err != nil || !reflect.DeepEqual(got, want) || !bytes.Equal(encodeReplay(t, o), wire) || f.calls != calls {
			t.Fatal("расхождение проекций или канонических байтов", err)
		}
	}
}

// checkReplayValues сверяет календарь, идентичность, точные цены и общую cutoff-свечу.
func checkReplayValues(t *testing.T, r SourceRequest, raw paperwire.Observation, m MarketSlice) {
	t.Helper()
	symbols := slices.Sorted(slices.Values(r.Run.Config.Values().Symbols))
	if len(m.Funding) != 3*len(symbols) || len(raw.FundingPages) != 3*len(symbols) || len(raw.MarkPages) != 3*len(symbols) {
		t.Fatal("ожидались три события и три уникальных часа на символ")
	}
	day := r.Run.ScheduledAt.Truncate(24 * time.Hour)
	for i, s := range symbols {
		pages := raw.FundingPages[i*3 : i*3+3]
		if len(pages[0].Events) != 2 || len(pages[1].Events) != 1 || len(pages[2].Events) != 0 {
			t.Fatal("ожидались две непустых страницы и пустая terminal")
		}
		mark := m.Marks[s]
		price, _ := mark.Price.WireDecimal()
		ref, _ := m.ReferencePrices[s].WireDecimal()
		cutoff := day.Add(16 * time.Hour)
		hour := raw.TradeWindows[2*i+1]
		if hour.Symbol != s || hour.StartMS != cutoff.Add(-time.Hour).UnixMilli() || hour.EndMS != cutoff.UnixMilli()-1 {
			t.Fatal(s, "границы часа reference")
		}
		if mark.Symbol != s || !mark.ObservedAt.Equal(cutoff) || ref != "12.75" || len(m.DailySeries[s]) != r.DailyBars {
			t.Fatal(s, "reference, cutoff или дневной ряд")
		}
		for j, row := range m.DailySeries[s] {
			closePrice, _ := row.Close.WireDecimal()
			if row.Symbol != s || !row.Confirmed || closePrice != "10" || !row.OpenTime.Equal(day.AddDate(0, 0, j-r.DailyBars)) {
				t.Fatal("неверная дневная свеча", s, j)
			}
		}
		for j := range 3 {
			at := day.Add(time.Duration(j) * 8 * time.Hour)
			event := m.Funding[j*len(symbols)+i]
			id := sha256.Sum256([]byte(fmt.Sprintf(`["momentum/paper-funding/v1","paper","%s","%s"]`, s, at.Format(time.RFC3339))))
			markPrice, _ := event.MarkPrice.WireDecimal()
			rate, _ := event.Event.Rate.WireDecimal()
			expected := fmt.Sprint(20 + (at.Unix()/3600-1)%10)
			if event.Symbol != s || !at.Equal(event.Event.FundingTime) || event.FundingID != fmt.Sprintf("%x", id) ||
				rate != "0.001" || markPrice != expected || at.Equal(cutoff) && price != expected {
				t.Fatal("неверная оценка funding", s, at)
			}
		}
	}
}

// TestReplayFundingCursor отвергает пропуск, лишнюю ветвь и неточные границы.
func TestReplayFundingCursor(t *testing.T) {
	r, f := sourceFixture(t, "ALT123USDT")
	raw := replayFixture(t, &r, f)
	for name, edit := range map[string]func(*paperwire.Observation){
		"missing-middle":   func(o *paperwire.Observation) { o.FundingPages = slices.Delete(o.FundingPages, 1, 2) },
		"missing-terminal": func(o *paperwire.Observation) { o.FundingPages = slices.Delete(o.FundingPages, 2, 3) },
		"wrong-cursor": func(o *paperwire.Observation) {
			o.FundingPages[1].EndTime = o.FundingPages[1].EndTime.Add(time.Millisecond)
		},
		"after-terminal": func(o *paperwire.Observation) {
			p := o.FundingPages[2]
			p.EndTime = p.EndTime.Add(-time.Millisecond)
			o.FundingPages = append(o.FundingPages, p)
		},
		"funding-fraction": func(o *paperwire.Observation) {
			o.FundingPages[0].StartTime = o.FundingPages[0].StartTime.Add(time.Nanosecond)
		},
		"mark-fraction":  func(o *paperwire.Observation) { o.MarkPages[0].EndTime = o.MarkPages[0].EndTime.Add(time.Nanosecond) },
		"funding-symbol": func(o *paperwire.Observation) { o.FundingPages[0].Symbol = "OTHERUSDT" },
		"mark-symbol":    func(o *paperwire.Observation) { o.MarkPages[0].Symbol = "OTHERUSDT" },
		"trade-symbol":   func(o *paperwire.Observation) { o.TradeWindows[0].Symbol = "OTHERUSDT" },
		"trade-interval": func(o *paperwire.Observation) { o.TradeWindows[0].Interval = "W" },
		"extra-mark": func(o *paperwire.Observation) {
			p := o.MarkPages[0]
			p.StartTime, p.EndTime = p.StartTime.Add(-time.Hour), p.EndTime.Add(-time.Hour)
			o.MarkPages = append(o.MarkPages, p)
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := copyReplay(raw)
			edit(&o)
			rejectReplay(t, r, o)
		})
	}
}

// TestCloseAllReplay проверяет оба варианта funding-окна и запрет дневных страниц.
func TestCloseAllReplay(t *testing.T) {
	for _, span := range []time.Duration{0, time.Minute} {
		r, f := sourceFixture(t, "ALT123USDT")
		daily := replayFixture(t, &r, f).TradeWindows[0]
		r.Transport, r.DailyBars = f, 0
		r.Purpose = paperwire.Purpose{Kind: "close-all", CommandID: "close", ControlRevision: 1}
		r.FundingAfter = r.Run.ScheduledAt.Add(-span)
		raw := replayFixture(t, &r, f)
		calls := f.calls
		decoded, err := paperwire.DecodeObservation(encodeReplay(t, raw))
		if err != nil {
			t.Fatal(err)
		}
		want, err := checkReplay(t, r, raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := checkReplay(t, r, decoded)
		if err != nil || !reflect.DeepEqual(got, want) || len(got.DailySeries)+len(got.Funding) != 0 {
			t.Fatal("close-all replay", span, err)
		}
		for _, s := range r.Run.Config.Values().Symbols {
			ref, _ := got.ReferencePrices[s].WireDecimal()
			if ref != "12.75" || got.Marks[s].Symbol != s {
				t.Fatal("close-all без reference или cutoff", s)
			}
		}
		if span == 0 && len(raw.FundingPages) != 0 || span > 0 && len(raw.FundingPages) != len(got.Marks) {
			t.Fatal("неверное число funding-страниц")
		}
		if span > 0 {
			without := decoded
			without.FundingPages = without.FundingPages[1:]
			rejectReplay(t, r, without)
		}
		decoded.TradeWindows = append(decoded.TradeWindows, daily)
		rejectReplay(t, r, decoded)
		if f.calls != calls {
			t.Fatal("повторный HTTP при close-all")
		}
	}
}
