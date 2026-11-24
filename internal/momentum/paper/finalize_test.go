package paper

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

type finalAttempt struct {
	*fillFixture
	req   s.PaperFinalizeRequest
	d     ResolutionDisposition
	build s.PaperResolutionBuilder
}

// attempt создаёт воспроизводимую заявку с явным временем модели.
func (x *fillFixture) attempt(kind, command string, revision uint64) finalAttempt {
	d := ResolutionDisposition{Kind: kind, ResolvedAt: x.in.Run.ScheduledAt, CommandID: command, ControlRevision: revision}
	return finalAttempt{x, s.PaperFinalizeRequest{Key: x.r.Key, Owner: x.owner, Kind: d.Kind, CommandID: command, ControlRevision: revision}, d, BuildPaperResolution(x.p, d)}
}

// run вызывает штатную финализацию с указанным контекстом и проверкой.
func (a finalAttempt) run(ctx context.Context, b s.PaperResolutionBuilder) (s.PaperFinalizeResult, error) {
	return s.FinalizePaperRun(ctx, a.Pool(), a.req, b)
}

// proof сравнивает фактическую строку с независимым вычислением и возвращает раскодированный итог.
func (a finalAttempt) proof(got s.PaperFinalizeResult, fills []model.ConfirmedFill) ResolvedCycle {
	want := replayedReceipt(a.T, a.p, fills, a.d)
	row, ok, err := s.ReadRunCompletion(a.Context(), a.Pool(), a.req.Key.RunID)
	outcome := map[string]s.RunOutcome{"COMPLETED": s.RunCompleted, "ABORTED": s.RunFailed}[a.d.Kind]
	a.Require(row.Outcome == outcome && row.PlanHash == a.p.Plan().Hash() && row.PlanRevision == a.p.Plan().Metadata().Revision, "связь итога и плана")
	a.Require(err == nil && ok && got.Confirmed && row == got.Completion && row.Reason == string(want.Payload()) && !row.CompletedAt.IsZero(), "строка M14", err)
	out, err := DecodeResolution([]byte(row.Reason), a.p)
	requireCycleSuccess(a.T, err)
	progress, err := ProjectProgress(a.p, fills)
	a.Require(err == nil && reflect.DeepEqual(out.Remaining(), progress.Remaining()) && reflect.DeepEqual(out.ActualAfter(), progress.ActualAfter()), "остаток и портфель", err)
	return out
}

// reject требует нулевого результата и неизменности исходных строк.
func (a finalAttempt) reject(ctx context.Context, build s.PaperResolutionBuilder, want error) {
	a.Helper()
	evidence := func() []any {
		key := a.r.Key
		rows, err := s.ReadPaperExecutionRows(a.Context(), a.Pool(), key.Scope, key.RunID)
		requireCycleSuccess(a.T, err)
		row, found, err := s.ReadRunCompletion(a.Context(), a.Pool(), key.RunID)
		requireCycleSuccess(a.T, err)
		return []any{rows, row, found}
	}
	before := evidence()
	got, err := a.run(ctx, build)
	a.Require(errors.Is(err, want) && (ctx.Err() == nil || errors.Is(err, ctx.Err())) && got == (s.PaperFinalizeResult{}) && reflect.DeepEqual(before, evidence()), want, err)
}

// TestFinalizeCompleted проверяет обе очереди на одной строке run и исторический повтор.
func TestFinalizeCompleted(t *testing.T) {
	for _, fillFirst := range []bool{false, true} {
		x := newFillFixture(t, false)
		a := x.attempt("COMPLETED", "", 0)
		var fills []model.ConfirmedFill
		var last s.PaperFillRequest
		expected := x.p.ExpectedFills()
		for i, f := range expected {
			e := x.slice(i, model.Zero(), f.Quantity)
			fills = append(fills, e.Fill)
			last = x.request(e)
			if !fillFirst || i < len(expected)-1 {
				requireCycleSuccess(t, x.record(e))
			}
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		var primary, concurrent s.PaperFinalizeResult
		var finished chan error
		block := func() {
			finished = make(chan error, 1)
			go func() {
				var err error
				concurrent, err = a.run(ctx, a.build)
				finished <- err
			}()
			x.Wait(ctx, x.Pool().QueryRowContext, "momentum.rebalance_runs", "RowShareLock", true, 0)
		}
		if fillFirst {
			x.Done(s.RecordPaperFill(ctx, x.Pool(), last, func(f s.PaperRunFacts) error {
				requireCycleSuccess(t, CheckPaperFill(x.p)(f))
				block()
				return nil
			}))
		} else {
			var err error
			primary, err = a.run(ctx, func(f s.PaperRunFacts) (paperwire.ResolutionRecord, error) {
				r, err := a.build(f)
				requireCycleSuccess(t, err)
				block()
				return r, nil
			})
			requireCycleSuccess(t, err)
		}
		x.Require(finished != nil, "нет provisional proof")
		requireCycleSuccess(t, <-finished)
		receipt := a.proof(concurrent, fills)
		x.Require(fillFirst || primary == concurrent, "разные строки M14")
		next := *x
		next.in = nextCycle(t, x.in, receipt, false)
		next.in.PlanBuilder = singleTarget(model.Zero())
		next.stage(false)
		requireCycleSuccess(t, x.owner.Release(ctx, time.Now()))
		got, err := a.run(ctx, a.build)
		x.Require(err == nil && got == concurrent, "история после смены head и owner", err)
		a.reject(ctx, BuildPaperResolution(x.p, ResolutionDisposition{Kind: "COMPLETED", ResolvedAt: a.d.ResolvedAt.Add(time.Second)}), s.ErrPaperWriteConflict)
		aborted := x.attempt("ABORTED", "other", 1)
		aborted.reject(ctx, aborted.build, s.ErrPaperWriteConflict)
	}
}

// TestFinalizeAbort проверяет partial, полномочие close-all и ожидание позднего SELL за финализатором.
func TestFinalizeAbort(t *testing.T) {
	x := newFillFixture(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	part := x.request(x.slice(0, model.Zero(), cycleAmount(t, "1")))
	x.Done(s.RecordPaperFill(ctx, x.Pool(), part, CheckPaperFill(x.p)))
	a := x.attempt("ABORTED", "close", 2)
	a.reject(ctx, a.build, s.ErrPaperWriteDenied)
	requireCycleSuccess(t, x.transition(ctx, 1, "close-all", "close"))
	for _, bad := range []finalAttempt{x.attempt("ABORTED", "close", 1), x.attempt("ABORTED", "foreign", 2)} {
		bad.reject(ctx, bad.build, s.ErrPaperWriteDenied)
	}
	before := x.Snapshot()
	got, err := a.run(ctx, a.build)
	x.Require(err == nil && before == x.Snapshot() && got.Completion.Outcome == s.RunFailed, "partial ABORTED", err)
	receipt := a.proof(got, []model.ConfirmedFill{part.Fill})
	next := *x
	next.in = nextCycle(t, x.in, receipt, true)
	next.stage(false)
	half := cycleAmount(t, "0.5")
	sell := next.request(next.slice(0, model.Zero(), half))
	late := next.request(next.slice(0, half, half))
	check := CheckPaperFill(next.p)
	next.Done(s.RecordPaperFill(ctx, next.Pool(), sell, check))
	before = next.Snapshot()
	closeRun := next.attempt("ABORTED", "close", 2)
	var lateResult s.PaperFillResult
	var finished chan error
	got, err = closeRun.run(ctx, func(f s.PaperRunFacts) (paperwire.ResolutionRecord, error) {
		r, err := closeRun.build(f)
		requireCycleSuccess(t, err)
		finished = make(chan error, 1)
		go func() {
			var err error
			lateResult, err = s.RecordPaperFill(ctx, next.Pool(), late, check)
			finished <- err
		}()
		next.Wait(ctx, next.Pool().QueryRowContext, "momentum.rebalance_runs", "RowShareLock", true, 0)
		return r, nil
	})
	x.Require(err == nil && finished != nil, "финализация close-run", err)
	x.Require(errors.Is(<-finished, s.ErrPaperWriteDenied) && lateResult == (s.PaperFillResult{}) && before == next.Snapshot(), "поздний SELL")
	closeRun.proof(got, []model.ConfirmedFill{sell.Fill})
	duplicate, err := s.RecordPaperFill(ctx, next.Pool(), sell, check)
	x.Require(err == nil && duplicate.Confirmed && !duplicate.Inserted, "исторический SELL", err)
	requireCycleSuccess(t, next.record(next.slice(0, half, half)))
	closeRun.reject(ctx, closeRun.build, s.ErrPaperWriteConflict)
}

// TestFinalizeExact проверяет широкий TEXT и цепочку с конечной долей себестоимости 1/3.
func TestFinalizeExact(t *testing.T) {
	x := newFillFixture(t, false)
	requireCycleSuccess(t, x.transition(t.Context(), 1, "close-all", "close"))
	a := x.attempt("ABORTED", "close", 2)
	den := new(big.Int).Lsh(big.NewInt(1), 4076)
	tiny, err := cycleAmount(t, "1").Quo(cycleAmount(t, den.String()))
	requireCycleSuccess(t, err)
	e := x.slice(0, model.Zero(), tiny)
	x.Require(errors.Is(x.record(e), s.ErrExecution), "ожидался postcommit ParseQty")
	x.observe(e, "PARTIALLY_FILLED", tiny, nil)
	got, err := a.run(t.Context(), a.build)
	requireCycleSuccess(t, err)
	receipt := a.proof(got, []model.ConfirmedFill{e.Fill})
	n, _, _ := receipt.ActualAfter().WalletUSDT.CanonicalRatio()
	num, ok := new(big.Int).SetString(n, 10)
	x.Require(ok && num.BitLen() == 4096 && len(got.Completion.Reason) > 10*1024, "широкий receipt")
	x.Pool().SetMaxIdleConns(0)
	dup, err := a.run(t.Context(), a.build)
	x.Require(err == nil && dup == got, "повтор на свежем backend", err)
	x = newFillFixture(t, false)
	requireCycleSuccess(t, x.transition(t.Context(), 1, "close-all", "close"))
	a = x.attempt("ABORTED", "close", 2)
	got, err = a.run(t.Context(), a.build)
	requireCycleSuccess(t, err)
	receipt = a.proof(got, nil)
	requireCycleSuccess(t, x.transition(t.Context(), 2, "resume", "restore"))
	for i, target := range []string{"0", "1", "3", "1"} {
		x.in = nextCycle(t, x.in, receipt, false)
		price := cycleAmount(t, []string{"0.2", "0.2", "0.4", "0.4"}[i])
		for _, w := range x.in.Observation.TradeWindows {
			if w.Interval == "60" {
				for k := range w.Page.Candles {
					c := &w.Page.Candles[k]
					c.Open, c.High, c.Low, c.Close = price, price, price, price
				}
			}
		}
		x.in.PlanBuilder = singleTarget(cycleAmount(t, target))
		var fills []model.ConfirmedFill
		if i == 0 {
			x.p = preparedFixture(t, x.in)
			x.r.Key.RunID, x.r.Key.InputHash = x.in.Run.RunID, x.p.Hash()
			x.Done(s.CreateOrReadRebalanceRun(t.Context(), x.Pool(), s.RebalanceRunInput(x.in.Run)))
			empty := x.attempt("COMPLETED", "", 0)
			empty.reject(t.Context(), empty.build, s.ErrPaperWriteConflict)
			x.Done(s.StorePaperInput(t.Context(), x.Pool(), x.p.input))
			empty.reject(t.Context(), empty.build, s.ErrPaperWriteConflict)
			x.Done(s.StorePlanRevision(t.Context(), x.Pool(), x.p.Plan()))
		} else {
			x.stage(false)
			x.Require(len(x.p.ExpectedFills()) == 1, "единственный BTC target")
			requireCycleSuccess(t, x.record(x.r))
			fills = []model.ConfirmedFill{x.r.Fill}
		}
		a = x.attempt("COMPLETED", "", 0)
		got, err = a.run(t.Context(), a.build)
		requireCycleSuccess(t, err)
		receipt = a.proof(got, fills)
	}
	n, d, _ := receipt.ActualAfter().Positions["BTCUSDT"].CostBasisUSDT.CanonicalRatio()
	x.Require(n == "1" && d == "3", n, d)
}

// TestFinalizeEvidence проверяет отказы до записи, при откате и после подмены сохранённых фактов.
func TestFinalizeEvidence(t *testing.T) {
	x := newFillFixture(t, false)
	a := x.attempt("COMPLETED", "", 0)
	a.reject(t.Context(), nil, s.ErrPaperWriteArgument)
	a.reject(t.Context(), a.build, s.ErrPaperWriteRejected)
	foreign := a
	other := newFillFixture(t, true)
	foreign.req.Owner = other.owner
	foreign.reject(t.Context(), a.build, s.ErrPaperWriteDenied)
	foreign.req = a.req
	foreign.req.Key.Scope.StrategyID += "foreign"
	foreign.reject(t.Context(), a.build, s.ErrPaperWriteDenied)
	requireCycleSuccess(t, x.transition(t.Context(), 1, "close-all", "close"))
	a = x.attempt("ABORTED", "close", 2)
	a.reject(t.Context(), func(s.PaperRunFacts) (paperwire.ResolutionRecord, error) { return paperwire.ResolutionRecord{}, nil }, s.ErrPaperWriteConflict)
	a.reject(t.Context(), func(f s.PaperRunFacts) (paperwire.ResolutionRecord, error) {
		_, err := a.build(f)
		requireCycleSuccess(t, err)
		return paperwire.ResolutionRecord{}, ErrPaperProgress
	}, ErrPaperProgress)
	cancelCtx, cancel := context.WithCancel(t.Context())
	a.reject(cancelCtx, func(f s.PaperRunFacts) (paperwire.ResolutionRecord, error) {
		r, err := a.build(f)
		requireCycleSuccess(t, err)
		cancel()
		return r, nil
	}, s.ErrPaperWriteUnknown)
	requireCycleSuccess(t, x.owner.Release(t.Context(), time.Now()))
	a.reject(t.Context(), a.build, s.ErrOwnerLost)
	doc := replayedReceipt(t, x.p, nil, a.d).record.Values()
	doc.ActualAfter.WalletUSDT = model.Zero()
	forged, err := paperwire.EncodeResolutionRecord(doc, x.p.input)
	requireCycleSuccess(t, err)
	x.Done(s.CompleteRebalanceRun(t.Context(), x.Pool(), s.RunCompletion{RunID: x.r.Key.RunID, Outcome: s.RunFailed, PlanRevision: 1, PlanHash: x.p.Plan().Hash(), Reason: string(forged.Payload())}))
	a.reject(t.Context(), a.build, s.ErrPaperWriteConflict)
	a.reject(cancelCtx, a.build, s.ErrPaperWriteUnknown)
	a = other.attempt("ABORTED", "close", 2)
	requireCycleSuccess(t, other.transition(t.Context(), 1, "close-all", "close"))
	bad := other.slice(0, model.Zero(), cycleAmount(t, "1"))
	bad.OrderLinkID = other.slice(1, model.Zero(), other.p.ExpectedFills()[1].Quantity).OrderLinkID
	requireCycleSuccess(t, other.record(bad))
	other.Done(ProjectProgress(other.p, []model.ConfirmedFill{bad.Fill}))
	a.reject(t.Context(), a.build, s.ErrPaperWriteRejected)
	other = newFillFixture(t, false)
	a = other.attempt("ABORTED", "close", 2)
	requireCycleSuccess(t, other.transition(t.Context(), 1, "close-all", "close"))
	bad = other.r
	bad.Fill.ExecutionID += "extra"
	requireCycleSuccess(t, other.record(bad))
	a.reject(t.Context(), a.build, s.ErrPaperWriteRejected)
	requireCycleSuccess(t, other.transition(t.Context(), 2, "resume", "restore"))
	requireCycleSuccess(t, other.transition(t.Context(), 3, "risk", "risk"))
	a.reject(t.Context(), a.build, s.ErrPaperWriteDenied)
	meta := other.p.Plan().Metadata()
	meta.Revision, meta.PreviousHash = 2, other.p.Plan().Hash()
	revised, err := model.NewTradePlan(model.PlanInput{Snapshot: other.p.Plan().Snapshot(), Targets: other.p.Plan().Targets(), Metadata: meta})
	requireCycleSuccess(t, err)
	other.Done(s.StorePlanRevision(t.Context(), other.Pool(), revised))
	a.reject(t.Context(), a.build, s.ErrPaperWriteConflict)
}
