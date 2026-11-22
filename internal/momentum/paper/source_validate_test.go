package paper

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// TestSavedObservationRejects проверяет важные искажения raw pages без source I/O.
func TestSavedObservationRejects(t *testing.T) {
	cases := map[string]func(*SourceRequest, *paperwire.Observation){
		"daily-missing": func(r *SourceRequest, o *paperwire.Observation) { o.TradeWindows = o.TradeWindows[1:] },
		"daily-empty":   func(r *SourceRequest, o *paperwire.Observation) { o.TradeWindows[0].Page.Candles = nil },
		"daily-permutation": func(r *SourceRequest, o *paperwire.Observation) {
			c := o.TradeWindows[0].Page.Candles
			c[0], c[1] = c[1], c[0]
		},
		"daily-duplicate": func(r *SourceRequest, o *paperwire.Observation) { c := o.TradeWindows[0].Page.Candles; c[1] = c[0] },
		"daily-gap": func(r *SourceRequest, o *paperwire.Observation) {
			o.TradeWindows[0].Page.Candles[0].OpenTime = o.TradeWindows[0].Page.Candles[0].OpenTime.Add(-24 * time.Hour)
		},
		"daily-unconfirmed": func(r *SourceRequest, o *paperwire.Observation) { o.TradeWindows[0].Page.Candles[0].Confirmed = false },
		"daily-units": func(r *SourceRequest, o *paperwire.Observation) {
			o.TradeWindows[0].Page.Candles[0].IntervalCode = "60"
		},
		"daily-symbol": func(r *SourceRequest, o *paperwire.Observation) {
			o.TradeWindows[0].Page.Candles[0].Symbol = "OTHERUSDT"
		},
		"daily-bounds": func(r *SourceRequest, o *paperwire.Observation) { o.TradeWindows[0].StartMS++ },
		"daily-ohcl": func(r *SourceRequest, o *paperwire.Observation) {
			o.TradeWindows[0].Page.Candles[0].High = model.Zero()
		},
		"daily-volume": func(r *SourceRequest, o *paperwire.Observation) {
			o.TradeWindows[0].Page.Candles[0].Volume = model.Decimal{}
		},
		"daily-provenance": func(r *SourceRequest, o *paperwire.Observation) {
			o.TradeWindows[0].Page.Endpoint = "https://api.bybit.com/v5/market/kline"
		},
		"trade-page-order": func(r *SourceRequest, o *paperwire.Observation) {
			o.TradeWindows[0], o.TradeWindows[1] = o.TradeWindows[1], o.TradeWindows[0]
		},
		"trade-future": func(r *SourceRequest, o *paperwire.Observation) {
			o.TradeWindows[1].Page.ServerTime = r.Run.ScheduledAt.Add(-2 * time.Hour)
		},
		"mark-missing": func(r *SourceRequest, o *paperwire.Observation) { o.MarkPages = o.MarkPages[1:] },
		"mark-empty":   func(r *SourceRequest, o *paperwire.Observation) { o.MarkPages[0].Candles = nil },
		"mark-page-order": func(r *SourceRequest, o *paperwire.Observation) {
			o.MarkPages[0], o.MarkPages[1] = o.MarkPages[1], o.MarkPages[0]
		},
		"mark-unconfirmed": func(r *SourceRequest, o *paperwire.Observation) { o.MarkPages[0].Candles[0].Confirmed = false },
		"mark-boundary": func(r *SourceRequest, o *paperwire.Observation) {
			o.MarkPages[0].EndTime = o.MarkPages[0].EndTime.Add(time.Millisecond)
		},
		"mark-interval":   func(r *SourceRequest, o *paperwire.Observation) { o.MarkPages[0].IntervalCode = "D" },
		"mark-provenance": func(r *SourceRequest, o *paperwire.Observation) { o.MarkPages[0].Endpoint = "trade" },
		"mark-age":        func(r *SourceRequest, o *paperwire.Observation) { r.Profile.Parameters.MarkMaxAge = time.Minute },
		"mark-ohcl": func(r *SourceRequest, o *paperwire.Observation) {
			o.MarkPages[0].Candles[0].Low = o.TradeWindows[0].Page.Candles[0].High
			o.MarkPages[0].Candles[0].Open = model.Zero()
		},
		"mark-duplicate":  func(r *SourceRequest, o *paperwire.Observation) { o.MarkPages = append(o.MarkPages, o.MarkPages[0]) },
		"funding-missing": func(r *SourceRequest, o *paperwire.Observation) { o.FundingPages = o.FundingPages[1:] },
		"funding-empty":   func(r *SourceRequest, o *paperwire.Observation) { o.FundingPages[0].Events = []model.FundingEvent{} },
		"funding-partial": func(r *SourceRequest, o *paperwire.Observation) {
			o.FundingPages[0].Events = o.FundingPages[0].Events[:2]
		},
		"funding-order": func(r *SourceRequest, o *paperwire.Observation) {
			c := o.FundingPages[0].Events
			c[0], c[1] = c[1], c[0]
		},
		"funding-exact-duplicate": func(r *SourceRequest, o *paperwire.Observation) { c := o.FundingPages[0].Events; c[1] = c[0] },
		"funding-changed-duplicate": func(r *SourceRequest, o *paperwire.Observation) {
			c := o.FundingPages[0].Events
			c[1].FundingTime = c[0].FundingTime
			c[1].Rate = model.Zero()
		},
		"funding-overlap": func(r *SourceRequest, o *paperwire.Observation) { o.FundingPages[1] = o.FundingPages[0] },
		"funding-unexpected": func(r *SourceRequest, o *paperwire.Observation) {
			o.FundingPages[0].Events[0].FundingTime = o.FundingPages[0].Events[0].FundingTime.Add(time.Minute)
		},
		"funding-future": func(r *SourceRequest, o *paperwire.Observation) {
			o.FundingPages[0].Events[0].FundingTime = r.Run.ScheduledAt.Add(time.Hour)
		},
		"funding-provenance": func(r *SourceRequest, o *paperwire.Observation) { o.FundingPages[0].Environment = "mainnet" },
		"funding-bounds": func(r *SourceRequest, o *paperwire.Observation) {
			o.FundingPages[0].StartTime = o.FundingPages[0].StartTime.Add(time.Millisecond)
		},
		"source-stale":  func(r *SourceRequest, o *paperwire.Observation) { o.SourceClock = o.SourceClock.Add(-time.Hour) },
		"source-future": func(r *SourceRequest, o *paperwire.Observation) { o.SourceClock = o.SourceClock.Add(time.Hour) },
		"page-stale": func(r *SourceRequest, o *paperwire.Observation) {
			o.MarkPages[0].ServerTime = o.MarkPages[0].ServerTime.Add(-2 * time.Minute)
		},
		"page-future": func(r *SourceRequest, o *paperwire.Observation) {
			o.TradeWindows[0].Page.ServerTime = o.CapturedAt.Add(time.Second)
		},
		"bars-mismatch": func(r *SourceRequest, o *paperwire.Observation) { o.DailyBars++ },
		"extra-trade": func(r *SourceRequest, o *paperwire.Observation) {
			o.TradeWindows = append(o.TradeWindows, o.TradeWindows[0])
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := sourceFixture(t, "ALT123USDT")
			o, err := ReadCycleObservation(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			r.Transport = nil
			edit(&r, &o)
			if _, err = ValidateCycleObservation(r, o); err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
}

// TestSourceFundingPagination проверяет backward cursors, boundary и стабильную identity.
func TestSourceFundingPagination(t *testing.T) {
	r, _ := sourceFixture(t, "ALT123USDT")
	r.FundingAfter = r.Run.ScheduledAt.Add(-100 * 24 * time.Hour)
	o, err := ReadCycleObservation(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.FundingPages) != 9 || len(o.FundingPages[0].Events) != 200 || len(o.FundingPages[1].Events) != 100 {
		t.Fatal("pagination incomplete")
	}
	m, err := ValidateCycleObservation(r, o)
	if err != nil || len(m.Funding) != 900 {
		t.Fatal(err, len(m.Funding))
	}
	original := m.Funding[0]
	o.FundingPages[1].Events[99].Rate = model.Zero()
	m, err = ValidateCycleObservation(r, o)
	if err != nil || m.Funding[0].FundingID != original.FundingID {
		t.Fatal("rate changed identity", err)
	}
	r, _ = sourceFixture(t, "ALT123USDT")
	r.Run.ScheduledAt = r.Run.ScheduledAt.Truncate(8 * time.Hour)
	r.Now = func() time.Time { return r.Run.ScheduledAt }
	r.Transport.(*publicFake).now = r.Run.ScheduledAt
	r.FundingAfter = r.Run.ScheduledAt.Add(-8 * time.Hour)
	o, err = ReadCycleObservation(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	m, err = ValidateCycleObservation(r, o)
	if err != nil || len(m.Funding) != 3 || !m.Funding[0].Event.FundingTime.Equal(r.Run.ScheduledAt) {
		t.Fatal("inclusive boundary", err)
	}
}

// TestSourceResourceCaps проверяет exact caps и cap+1 до проекций или HTTP декодирования.
func TestSourceResourceCaps(t *testing.T) {
	pages := paperwire.Observation{FundingPages: make([]model.FundingPage, paperwire.MaxObservationPages)}
	if err := observationBudget(pages); err != nil {
		t.Fatal(err)
	}
	pages.FundingPages = append(pages.FundingPages, model.FundingPage{})
	if err := observationBudget(pages); !errors.Is(err, paperwire.ErrPaperResourceLimit) {
		t.Fatal(err)
	}
	rows := make([]model.FundingEvent, paperwire.MaxObservationRows)
	for i := range rows {
		rows[i].Rate = model.Zero()
	}
	o := paperwire.Observation{FundingPages: []model.FundingPage{{Events: rows}}}
	if err := observationBudget(o); err != nil {
		t.Fatal(err)
	}
	o.FundingPages[0].Events = append(rows, model.FundingEvent{})
	if err := observationBudget(o); !errors.Is(err, paperwire.ErrPaperResourceLimit) {
		t.Fatal(err)
	}
	o = paperwire.Observation{FundingPages: []model.FundingPage{{Endpoint: strings.Repeat("x", paperwire.MaxCanonicalBytes)}}}
	if err := observationBudget(o); !errors.Is(err, paperwire.ErrPaperResourceLimit) {
		t.Fatal(err)
	}
	for _, n := range []int{8, 9} {
		body := sourceBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", n))), budget: &sourceTransport{remaining: 8}}
		_, err := io.ReadAll(body)
		if n == 8 && err != nil || n == 9 && !errors.Is(err, paperwire.ErrPaperResourceLimit) {
			t.Fatal(n, err)
		}
	}
	r, f := sourceFixture(t, "ALT123USDT")
	s := r.Profile.FundingSchedules["ALT123USDT"]
	s.Interval = time.Millisecond
	r.Profile.FundingSchedules["ALT123USDT"] = s
	// Другие символы не добавляют событий в этом коротком окне.
	r.FundingAfter = r.Run.ScheduledAt.Add(-4096 * time.Millisecond)
	if err := sourceRequest(r); err != nil || f.calls != 0 {
		t.Fatal("exact event cap", err)
	}
	r.FundingAfter = r.FundingAfter.Add(-time.Millisecond)
	if err := sourceRequest(r); !errors.Is(err, paperwire.ErrPaperResourceLimit) {
		t.Fatal(err)
	}
}

type sourceRoundTrip func(*http.Request) (*http.Response, error)

// RoundTrip позволяет независимо задавать поведение clock/context в fake.
func (f sourceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestElapsedClockRollback проверяет, что rollback не продлевает исходный timeout.
func TestElapsedClockRollback(t *testing.T) {
	r, f := sourceFixture(t, "ALT123USDT")
	r.Profile.DataDeadline = 5 * time.Millisecond
	r.Transport = sourceRoundTrip(func(q *http.Request) (*http.Response, error) {
		f.now = f.now.Add(-time.Hour)
		<-q.Context().Done()
		return nil, q.Context().Err()
	})
	if _, err := ReadCycleObservation(context.Background(), r); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

// TestProfileRatiosAndIndependentMarks сохраняет4096bit ratio и разные mark timestamps.
func TestProfileRatiosAndIndependentMarks(t *testing.T) {
	r, _ := sourceFixture(t, "ALT123USDT")
	two, _ := model.ParseAmount("2")
	huge, _ := model.ParseAmount("1")
	for i := 0; i < 4095; i++ {
		var err error
		huge, err = huge.Mul(two)
		if err != nil {
			t.Fatal(err)
		}
	}
	one, _ := model.ParseAmount("1")
	tiny, err := one.Quo(huge)
	if err != nil {
		t.Fatal(err)
	}
	r.Profile.Parameters.FeeRate = tiny
	r.Profile.Parameters.SlippageRate = tiny
	o, err := ReadCycleObservation(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ValidateCycleObservation(r, o)
	if err != nil {
		t.Fatal(err)
	}
	cmp, _ := m.Funding[0].MarkPrice.Cmp(m.Marks[m.Funding[0].Symbol].Price)
	if cmp == 0 {
		t.Fatal("historical funding used cutoff mark")
	}
	before, _, _ := tiny.CanonicalRatio()
	after, _, _ := r.Profile.Parameters.FeeRate.CanonicalRatio()
	if before != after {
		t.Fatal("profile rounded")
	}
	r.Profile.RPCTimeout = 0
	if _, err = ValidateCycleObservation(r, o); !errors.Is(err, ErrPaperObservation) {
		t.Fatal(err)
	}
}

// TestExplicitDailyBounds проверяет крайние caller counts без baseline default.
func TestExplicitDailyBounds(t *testing.T) {
	for _, count := range []int{1, 200} {
		r, _ := sourceFixture(t, "ALT123USDT")
		r.DailyBars = count
		o, err := ReadCycleObservation(context.Background(), r)
		if err != nil {
			t.Fatal(count, err)
		}
		m, err := ValidateCycleObservation(r, o)
		if err != nil || len(m.DailySeries["ALT123USDT"]) != count {
			t.Fatal(count, err)
		}
	}
}
