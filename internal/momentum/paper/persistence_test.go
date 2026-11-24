package paper

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

// request добавляет полномочие записи к исходному ожиданию.
func (x *fillFixture) request(e s.PaperFillExpectation) s.PaperFillRequest {
	return s.PaperFillRequest{Key: e.Key, Owner: x.owner, OrderLinkID: e.OrderLinkID, Fill: e.Fill}
}

// reject проверяет отказ и все подтверждённые строки исполнения.
func (x *fillFixture) reject(r s.PaperFillRequest, check s.PaperFillCheck, want error) {
	x.Helper()
	before := x.facts().DataHash
	got, err := s.RecordPaperFill(x.Context(), x.Pool(), r, check)
	x.Require(errors.Is(err, want) && got == (s.PaperFillResult{}) && x.facts().DataHash == before, r.OrderLinkID, got, err, want)
}

// transition проводит реальный переход risk controller или оператора.
func (x *fillFixture) transition(ctx context.Context, rev uint64, kind, id string) error {
	if kind == "risk" {
		_, err := s.AppendRiskTransition(ctx, x.Pool(), s.RiskTransition{Scope: x.r.Key.Scope, ExpectedRevision: rev, Action: "HALT", Reason: "stale data", ConfigHash: x.p.Binding().ConfigHash, OccurredAt: x.in.Run.ScheduledAt, Events: []s.RiskEventRecord{{EventID: id, Kind: "STALE_DATA", Detail: "stale market"}}})
		return err
	}
	action := map[string]string{"pause": "PAUSE_ENTRIES", "resume": "RESUME", "halt": "HALT", "close-all": "HALT"}[kind]
	_, err := s.ApplyPaperCommandState(ctx, x.Pool(), s.PaperCommandRequest{Scope: x.r.Key.Scope, ExpectedRevision: rev, ConfigHash: x.p.Binding().ConfigHash, Action: action, Actor: "OPERATOR", ActorID: "operator/test", Kind: kind, CommandID: id, Confirmed: true, Reason: id, RecoveryID: map[string]string{"resume": "verified/test"}[kind]})
	return err
}

// TestGuardedPaperFill проверяет provisional veto, точный concurrent retry и кошелёк.
func TestGuardedPaperFill(t *testing.T) {
	x := newFillFixture(t, false)
	r := x.request(x.slice(0, model.Zero(), cycleAmount(t, "1")))
	check := CheckPaperFill(x.p)
	x.reject(r, nil, s.ErrPaperWriteArgument)
	for _, tc := range []struct {
		want error
		edit func(*s.PaperFillRequest)
	}{
		{s.ErrPaperWriteArgument, func(v *s.PaperFillRequest) { v.Owner = nil }},
		{s.ErrPaperWriteArgument, func(v *s.PaperFillRequest) { v.Owner = new(s.ExecutionOwner) }},
		{s.ErrPaperWriteArgument, func(v *s.PaperFillRequest) { v.Fill.ExecutionID = "" }},
		{s.ErrPaperWriteArgument, func(v *s.PaperFillRequest) { v.Key.Scope.Environment = "testnet" }},
		{s.ErrPaperWriteArgument, func(v *s.PaperFillRequest) { v.Fill.ExecutedAt = v.Fill.ExecutedAt.Add(time.Nanosecond) }},
		{s.ErrPaperWriteDenied, func(v *s.PaperFillRequest) { v.Key.Scope.StrategyID += "other" }},
		{s.ErrPaperWriteConflict, func(v *s.PaperFillRequest) { v.Key.RunID += "other" }},
		{s.ErrPaperWriteConflict, func(v *s.PaperFillRequest) { v.Key.InputHash = strings.Repeat("a", 64) }},
		{s.ErrPaperWriteRejected, func(v *s.PaperFillRequest) { v.Fill.ExecutionID += "other" }},
		{s.ErrPaperWriteRejected, func(v *s.PaperFillRequest) { v.Fill.ExecutionPrice = cycleAmount(t, "1") }},
		{s.ErrPaperWriteRejected, func(v *s.PaperFillRequest) { v.Fill.FeeUSDT = model.Zero() }},
		{s.ErrPaperWriteConflict, func(v *s.PaperFillRequest) { v.Fill.Quantity = cycleAmount(t, "999999999") }},
	} {
		v := r
		tc.edit(&v)
		x.reject(v, check, tc.want)
	}
	x.reject(x.request(x.slice(0, r.Fill.Quantity, r.Fill.Quantity)), check, s.ErrPaperWriteRejected)
	x.reject(x.request(x.slice(1, model.Zero(), x.p.ExpectedFills()[1].Quantity)), check, s.ErrPaperWriteRejected)
	x.reject(r, func(f s.PaperRunFacts) error {
		requireCycleSuccess(t, check(f))
		x.Require(f.Fills()[r.Fill.ExecutionID].OrderLinkID == r.OrderLinkID, "нет provisional записи")
		return ErrPaperProgress
	}, ErrPaperProgress)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var done chan error
	var b s.PaperFillResult
	a, err := s.RecordPaperFill(ctx, x.Pool(), r, func(f s.PaperRunFacts) error {
		requireCycleSuccess(t, check(f))
		done = make(chan error, 1)
		go func() {
			var err error
			b, err = s.RecordPaperFill(ctx, x.Pool(), r, check)
			done <- err
		}()
		x.Wait(ctx, x.Pool().QueryRowContext, "momentum.order_intents", "RowShareLock", true, 0)
		return nil
	})
	x.Require(err == nil && done != nil, "первый writer", err)
	requireCycleSuccess(t, <-done)
	for _, got := range []s.PaperFillResult{a, b} {
		x.Require(got.Confirmed && got.InsertedKnown && got.State == "PARTIALLY_FILLED" && decimalEqual(r.Fill.Quantity, got.FilledQuantity), got)
	}
	x.Require(a.Inserted != b.Inserted && len(x.facts().Fills()) == 1, "двойное исполнение")
	progress, projectErr := ProjectProgress(x.p, []model.ConfirmedFill{r.Fill})
	want, err := x.p.Cutoff().WalletUSDT.Sub(r.Fill.FeeUSDT)
	x.Require(projectErr == nil && err == nil && decimalEqual(want, progress.ActualAfter().WalletUSDT), "кошелёк BUY", projectErr, err)
	changed := r
	changed.Fill.FeeUSDT = model.Zero()
	x.reject(changed, check, s.ErrPaperWriteConflict)
	other := newFillFixture(t, true)
	other.reject(r, check, s.ErrPaperWriteDenied)
	other.Require(len(other.facts().Fills()) == 0, "исполнение в чужом pool")
	quarter, err := other.r.Fill.Quantity.Mul(cycleAmount(t, "0.25"))
	requireCycleSuccess(t, err)
	first, second := other.slice(0, model.Zero(), quarter), other.slice(0, quarter, quarter)
	first.OrderLinkID = other.slice(1, model.Zero(), other.p.ExpectedFills()[1].Quantity).OrderLinkID
	requireCycleSuccess(t, other.record(first))
	other.Done(ProjectProgress(other.p, []model.ConfirmedFill{first.Fill, second.Fill}))
	other.reject(other.request(second), CheckPaperFill(other.p), s.ErrPaperWriteRejected)
	other = newFillFixture(t, false)
	for state, v := range map[string]s.PaperFillRequest{"UNKNOWN": other.request(other.r), "CANCELLED": other.request(other.slice(0, model.Zero(), r.Fill.Quantity))} {
		requireCycleSuccess(t, s.SetOrderIntentState(t.Context(), other.Pool(), "paper", other.in.Run.AccountID, v.OrderLinkID, state))
		other.reject(v, CheckPaperFill(other.p), s.ErrPaperWriteDenied)
	}
	id := s.OwnerIdentity{SessionID: "new", Environment: "paper", AccountID: x.in.Run.AccountID, StrategyID: x.in.Run.StrategyID, Host: "test", ProcessID: 2}
	x.KillOwner(s.ExecutionOwnerLockKey(id))
	got, err := s.RecordPaperFill(t.Context(), x.Pool(), r, check)
	x.Require(err == nil && got.Confirmed && !got.InsertedKnown && !got.Inserted, "исторический fill", err)
	fresh := x.request(x.slice(0, r.Fill.Quantity, r.Fill.Quantity))
	x.reject(fresh, check, s.ErrOwnerLost)
	fresh.Owner, err = s.AcquireExecutionOwner(t.Context(), x.Pool(), id, time.Now())
	x.Require(err == nil && fresh.Owner.NeedsReconciliation(), "takeover", err)
	x.owner = fresh.Owner
	x.reject(fresh, check, s.ErrOwnerReconciliationRequired)
	requireCycleSuccess(t, x.owner.MarkReconciled("verified/test"))
	x.ReplaceOwner(id.SessionID, "foreign")
	requireCycleSuccess(t, x.owner.Permit(t.Context()))
	x.reject(fresh, check, s.ErrPaperWriteDenied)
}

// TestGuardedPaperAmount проверяет postcommit ParseQty, повтор и недоступный readback.
func TestGuardedPaperAmount(t *testing.T) {
	x := newFillFixture(t, false)
	check := CheckPaperFill(x.p)
	ctx, cancel := context.WithCancel(t.Context())
	got, err := s.RecordPaperFill(ctx, x.Pool(), x.request(x.r), func(f s.PaperRunFacts) error { err := check(f); cancel(); return err })
	x.Require(got == (s.PaperFillResult{}) && errors.Is(err, context.Canceled) && errors.Is(err, s.ErrPaperWriteUnknown) && len(x.facts().Fills()) == 0, "отмена", err)
	q := cycleAmount(t, "0."+strings.Repeat("0", 140)+"1")
	r := x.request(x.slice(0, model.Zero(), q))
	for range 2 {
		got, err = s.RecordPaperFill(t.Context(), x.Pool(), r, check)
		x.Require(err == nil && got.Confirmed && !got.Inserted && !got.InsertedKnown && got.State == "PARTIALLY_FILLED" && decimalEqual(q, got.FilledQuantity) && len(x.facts().Fills()) == 1, "точный readback", err)
	}
	meta := x.p.Plan().Metadata()
	meta.Revision, meta.PreviousHash = 2, x.p.Plan().Hash()
	revised, err := model.NewTradePlan(model.PlanInput{Snapshot: x.p.Plan().Snapshot(), Targets: x.p.Plan().Targets(), Metadata: meta})
	requireCycleSuccess(t, err)
	x.Done(s.StorePlanRevision(t.Context(), x.Pool(), revised))
	before := x.Snapshot()
	got, err = s.RecordPaperFill(t.Context(), x.Pool(), x.request(x.slice(0, q, q)), check)
	x.Require(got == (s.PaperFillResult{}) && errors.Is(err, s.ErrPaperWriteConflict) && before == x.Snapshot(), "чужая revision", err)
}

// TestGuardedPaperHalt проверяет все порядки фактических публичных писателей.
func TestGuardedPaperHalt(t *testing.T) {
	for kind, mode := range map[string]string{"risk": "RowExclusiveLock", "halt": "ShareRowExclusiveLock"} {
		for _, first := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/first=%t", kind, first), func(t *testing.T) {
				x := newFillFixture(t, false)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				holder, err := x.Pool().BeginTx(ctx, nil)
				requireCycleSuccess(t, err)
				defer holder.Rollback()
				stopped, written := make(chan error, 1), make(chan error, 1)
				ready, startHalt := context.WithCancel(ctx)
				go func() { <-ready.Done(); stopped <- x.transition(ctx, 1, kind, "stop") }()
				if first {
					x.HoldConfig(ctx, holder)
					startHalt()
					controller := x.Wait(ctx, holder.QueryRowContext, "momentum.control_revisions", mode, true, -1)
					go func() {
						_, err := s.RecordPaperFill(ctx, x.Pool(), x.request(x.r), CheckPaperFill(x.p))
						written <- err
					}()
					x.Wait(ctx, holder.QueryRowContext, "momentum.control_revisions", "ShareLock", false, controller)
					requireCycleSuccess(t, holder.Commit())
				} else {
					_, err := s.RecordPaperFill(ctx, x.Pool(), x.request(x.r), func(f s.PaperRunFacts) error {
						requireCycleSuccess(t, CheckPaperFill(x.p)(f))
						startHalt()
						x.Wait(ctx, holder.QueryRowContext, "momentum.control_revisions", mode, false, 0)
						return nil
					})
					written <- err
				}
				requireCycleSuccess(t, <-stopped)
				err = <-written
				f := x.facts()
				x.Require(f.Control.State.Mode == "HALTED" && (first && errors.Is(err, s.ErrPaperWriteDenied) && len(f.Fills()) == 0 || !first && err == nil && len(f.Fills()) == 1 && f.Intents()[x.r.OrderLinkID].State == "FILLED"), "порядок HALT/fill", err)
			})
		}
	}
}

// TestGuardedPaperCommands проверяет обычные и close-all намерения после подтверждённого исхода предшественника.
func TestGuardedPaperCommands(t *testing.T) {
	for _, purpose := range []paperwire.Purpose{{Kind: "buy"}, {Kind: "sell"}, {Kind: "close-all", CommandID: "close", ControlRevision: 7},
		{Kind: "close-all", CommandID: "foreign", ControlRevision: 7}, {Kind: "close-all", CommandID: "close", ControlRevision: 6}} {
		t.Run(fmt.Sprint(purpose), func(t *testing.T) {
			x := newFillFixture(t, false)
			first := x.request(x.slice(0, model.Zero(), cycleAmount(t, "1")))
			check := CheckPaperFill(x.p)
			requireCycleSuccess(t, x.transition(t.Context(), 1, "pause", "pause1"))
			x.reject(first, check, s.ErrPaperWriteDenied)
			requireCycleSuccess(t, x.transition(t.Context(), 2, "resume", "resume1"))
			x.Done(s.RecordPaperFill(t.Context(), x.Pool(), first, check))
			requireCycleSuccess(t, x.transition(t.Context(), 3, "halt", "abort"))
			receipt := replayedReceipt(t, x.p, []model.ConfirmedFill{first.Fill}, ResolutionDisposition{Kind: "ABORTED", CommandID: "abort", ControlRevision: 4, ResolvedAt: x.in.Run.ScheduledAt})
			x.Done(s.CompleteRebalanceRun(t.Context(), x.Pool(), s.RunCompletion{RunID: first.Key.RunID, Outcome: s.RunFailed, PlanHash: x.p.Plan().Hash(), PlanRevision: 1, Reason: string(receipt.Payload())}))
			duplicate, err := s.RecordPaperFill(t.Context(), x.Pool(), first, check)
			x.Require(err == nil && duplicate.Confirmed && !duplicate.Inserted, "исторический duplicate", err)
			requireCycleSuccess(t, x.transition(t.Context(), 4, "resume", "resume2"))
			x.reject(x.request(x.slice(0, first.Fill.Quantity, first.Fill.Quantity)), check, s.ErrPaperWriteDenied)
			next := *x
			closeAll := purpose.Kind == "close-all"
			next.in = nextCycle(t, x.in, receipt, closeAll)
			if closeAll {
				next.in.Purpose = purpose
			} else {
				next.in.PlanBuilder = singleTarget(cycleAmount(t, map[string]string{"buy": "3", "sell": "0"}[purpose.Kind]))
			}
			next.stage(false)
			check = CheckPaperFill(next.p)
			half := cycleAmount(t, "0.5")
			candidate := next.request(next.slice(0, model.Zero(), half))
			if purpose.Kind == "buy" {
				candidate.OrderLinkID = first.OrderLinkID
				before := x.facts().DataHash
				next.reject(candidate, check, s.ErrPaperWriteConflict)
				x.Require(x.facts().DataHash == before, "изменился предыдущий run")
				return
			}
			if closeAll {
				next.reject(candidate, check, s.ErrPaperWriteDenied)
			}
			requireCycleSuccess(t, next.transition(t.Context(), 5, "pause", "pause2"))
			if purpose.Kind == "sell" {
				next.Done(s.RecordPaperFill(t.Context(), next.Pool(), candidate, check))
				return
			}
			next.reject(candidate, check, s.ErrPaperWriteDenied)
			requireCycleSuccess(t, next.transition(t.Context(), 6, "close-all", "close"))
			if purpose.CommandID != "close" || purpose.ControlRevision != 7 {
				next.reject(candidate, check, s.ErrPaperWriteDenied)
				return
			}
			next.Done(s.RecordPaperFill(t.Context(), next.Pool(), candidate, check))
			remaining := next.request(next.slice(0, half, half))
			requireCycleSuccess(t, next.transition(t.Context(), 7, "resume", "resume3"))
			next.reject(remaining, check, s.ErrPaperWriteDenied)
			requireCycleSuccess(t, next.transition(t.Context(), 8, "risk", "risk"))
			next.reject(remaining, check, s.ErrPaperWriteDenied)
		})
	}
}
