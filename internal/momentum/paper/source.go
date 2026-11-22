// Package paper проверяет публичные наблюдения без исполнения или хранения.
package paper

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"bybit_bot_liquidity/internal/exchange/bybit"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// ErrPaperObservation обозначает неполный или недостоверный публичный срез.
var ErrPaperObservation = errors.New("invalid paper observation")

// SourceRequest задаёт явный неизменяемый контракт получения данных.
type SourceRequest struct {
	Run          paperwire.RunBinding
	Purpose      paperwire.Purpose
	Profile      paperwire.Profile
	DailyBars    int
	FundingAfter time.Time
	Now          func() time.Time
	Transport    http.RoundTripper
}

// MarketSlice содержит отдельные проверенные проекции исходных страниц.
type MarketSlice struct {
	DailySeries     map[string][]data.DailyClose
	Marks           map[string]model.LongMarkObservation
	ReferencePrices map[string]model.Decimal
	Funding         []model.LongFundingAssessment
}

// PublicReadiness проверяет реальные закрытые дневные строки всех символов.
type PublicReadiness struct {
	Config                   config.Config
	DailyBars                int
	RPCTimeout, SourceMaxAge time.Duration
	Now                      func() time.Time
	Transport                http.RoundTripper
}

type acquisition struct {
	client   *bybit.Client
	now      func() time.Time
	deadline time.Time
	timeout  time.Duration
}

// rpc ограничивает RPC исходным context и оставшимся логическим временем.
func (a acquisition) rpc(ctx context.Context, call func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timeout := a.timeout
	if !a.deadline.IsZero() {
		remaining := a.deadline.Sub(a.now())
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		timeout = min(timeout, remaining)
	}
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := call(child)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if child.Err() != nil {
		return child.Err()
	}
	if !a.deadline.IsZero() && !a.now().Before(a.deadline) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, paperwire.ErrPaperResourceLimit) {
		return paperwire.ErrPaperResourceLimit
	}
	if err != nil {
		return ErrPaperObservation
	}
	return nil
}

// daily загружает общую для readiness и цикла дневную страницу.
func (a acquisition) daily(ctx context.Context, symbol string, at time.Time, count int) (paperwire.TradeWindow, error) {
	start, end := dailyBounds(at, count)
	w := paperwire.TradeWindow{Symbol: symbol, Interval: "D", StartMS: start, EndMS: end}
	err := a.rpc(ctx, func(ctx context.Context) error {
		var err error
		w.Page, err = a.client.GetMomentumCandlePage(ctx, symbol, "D", start, end)
		return err
	})
	return w, err
}

// ClosedThrough возвращает общий UTC boundary только после проверки реальных строк.
func (r PublicReadiness) ClosedThrough(ctx context.Context) (time.Time, error) {
	if !publicConfig(r.Config) || r.Now == nil || r.Transport == nil || r.DailyBars < 1 || r.DailyBars > 200 || r.RPCTimeout <= 0 || r.SourceMaxAge <= 0 {
		return time.Time{}, ErrPaperObservation
	}
	client, err := bybit.NewClient(testnetOrigin, &http.Client{Transport: &sourceTransport{r.Transport, paperwire.MaxCanonicalBytes}})
	if err != nil {
		return time.Time{}, ErrPaperObservation
	}
	a := acquisition{client: client, now: r.Now, timeout: r.RPCTimeout}
	var clock time.Time
	if err = a.rpc(ctx, func(ctx context.Context) error { var e error; clock, e = client.GetMomentumTestnetTime(ctx); return e }); err != nil {
		return time.Time{}, err
	}
	cutoff := clock.Truncate(24 * time.Hour)
	for _, symbol := range r.Config.Values().Symbols {
		w, e := a.daily(ctx, symbol, cutoff, r.DailyBars)
		if e != nil {
			return time.Time{}, e
		}
		captured := r.Now()
		if !fresh(clock, captured, r.SourceMaxAge) || !fresh(w.Page.ServerTime, captured, r.SourceMaxAge) || validateTrade(w, symbol, "D", cutoff, r.DailyBars) != nil {
			return time.Time{}, ErrPaperObservation
		}
	}
	if err = ctx.Err(); err != nil {
		return time.Time{}, err
	}
	return cutoff, nil
}

const testnetOrigin = "https://api-testnet.bybit.com"

// ReadCycleObservation сохраняет полные страницы и проверяет собственный результат.
func ReadCycleObservation(ctx context.Context, r SourceRequest) (paperwire.Observation, error) {
	var o paperwire.Observation
	if err := sourceRequest(r); err != nil {
		return o, err
	}
	if err := ctx.Err(); err != nil {
		return o, err
	}
	if r.Transport == nil {
		return o, ErrPaperObservation
	}
	deadline := r.Run.ScheduledAt.Add(r.Profile.DataDeadline)
	remaining := deadline.Sub(r.Now())
	if remaining <= 0 {
		return o, context.DeadlineExceeded
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	client, err := bybit.NewClient(testnetOrigin, &http.Client{Transport: &sourceTransport{r.Transport, paperwire.MaxCanonicalBytes}})
	if err != nil {
		return o, ErrPaperObservation
	}
	a := acquisition{client, r.Now, deadline, r.Profile.RPCTimeout}
	if err = a.rpc(ctx, func(ctx context.Context) error {
		var e error
		o.SourceClock, e = client.GetMomentumTestnetTime(ctx)
		return e
	}); err != nil {
		return paperwire.Observation{}, err
	}
	o.DailyBars = r.DailyBars
	marks := make(map[string]bool)
	for _, symbol := range r.Run.Config.Values().Symbols {
		if r.DailyBars > 0 {
			w, e := a.daily(ctx, symbol, r.Run.ScheduledAt, r.DailyBars)
			if e != nil {
				return paperwire.Observation{}, e
			}
			if err = observationBudget(o, paperwire.Observation{TradeWindows: []paperwire.TradeWindow{w}}); err != nil {
				return paperwire.Observation{}, err
			}
			o.TradeWindows = append(o.TradeWindows, w)
		}
		open := previousHour(r.Run.ScheduledAt)
		w := paperwire.TradeWindow{Symbol: symbol, Interval: "60", StartMS: open.UnixMilli(), EndMS: open.Add(time.Hour - time.Millisecond).UnixMilli()}
		if err = a.rpc(ctx, func(ctx context.Context) error {
			var e error
			w.Page, e = client.GetMomentumCandlePage(ctx, symbol, "60", w.StartMS, w.EndMS)
			return e
		}); err != nil {
			return paperwire.Observation{}, err
		}
		if err = observationBudget(o, paperwire.Observation{TradeWindows: []paperwire.TradeWindow{w}}); err != nil {
			return paperwire.Observation{}, err
		}
		o.TradeWindows = append(o.TradeWindows, w)
		first, count := fundingSpan(r, symbol)
		for i := int64(-1); i < count; i++ {
			if err = ctx.Err(); err != nil {
				return paperwire.Observation{}, err
			}
			at := r.Run.ScheduledAt
			if i >= 0 {
				at = time.UnixMilli(first + i*r.Profile.FundingSchedules[symbol].Interval.Milliseconds()).UTC()
			}
			open = previousHour(at)
			key := symbol + open.Format(time.RFC3339Nano)
			if marks[key] {
				continue
			}
			var page model.MarkPricePage
			if err = a.rpc(ctx, func(ctx context.Context) error {
				var e error
				page, e = client.GetMomentumMarkPricePage(ctx, symbol, "60", open.UnixMilli(), open.Add(time.Hour-time.Millisecond).UnixMilli())
				return e
			}); err != nil {
				return paperwire.Observation{}, err
			}
			if err = observationBudget(o, paperwire.Observation{MarkPages: []model.MarkPricePage{page}}); err != nil {
				return paperwire.Observation{}, err
			}
			o.MarkPages = append(o.MarkPages, page)
			marks[key] = true
		}
		start, end := r.FundingAfter.UnixMilli()+1, r.Run.ScheduledAt.UnixMilli()
		for start <= end {
			var page model.FundingPage
			if err = a.rpc(ctx, func(ctx context.Context) error {
				var e error
				page, e = client.GetMomentumFundingPage(ctx, symbol, start, end)
				return e
			}); err != nil {
				return paperwire.Observation{}, err
			}
			if err = observationBudget(o, paperwire.Observation{FundingPages: []model.FundingPage{page}}); err != nil {
				return paperwire.Observation{}, err
			}
			o.FundingPages = append(o.FundingPages, page)
			decision, e := model.NextFundingPage(model.FundingPageRequest{Environment: "testnet", Symbol: symbol, Start: time.UnixMilli(start).UTC(), End: time.UnixMilli(end).UTC()}, page)
			if e != nil {
				return paperwire.Observation{}, ErrPaperObservation
			}
			if decision.Action != model.FundingPageContinue {
				break
			}
			end = decision.NextEnd.UnixMilli()
		}
	}
	o.CapturedAt = r.Now()
	if _, err = ValidateCycleObservation(r, o); err != nil {
		return paperwire.Observation{}, err
	}
	if err = ctx.Err(); err != nil {
		return paperwire.Observation{}, err
	}
	return o, nil
}

// sourceTransport ограничивает совокупные source bytes до typed декодирования.
type sourceTransport struct {
	base      http.RoundTripper
	remaining int
}

// RoundTrip использует только явно переданный transport и ограниченное тело.
func (t *sourceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err == nil {
		copy := *response
		copy.Body = sourceBody{response.Body, t}
		response = &copy
	}
	return response, err
}

type sourceBody struct {
	io.ReadCloser
	budget *sourceTransport
}

// Read ограничивает суммарные bytes, сохраняя точную границу cap.
func (b sourceBody) Read(p []byte) (int, error) {
	if len(p) > b.budget.remaining+1 {
		p = p[:b.budget.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.budget.remaining -= n
	if b.budget.remaining < 0 {
		return n, paperwire.ErrPaperResourceLimit
	}
	return n, err
}
