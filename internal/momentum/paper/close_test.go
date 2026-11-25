package paper

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	s "bybit_bot_liquidity/internal/momentum/storage"
	"bybit_bot_liquidity/internal/momentum/storage/pgtest"
)

// closeFixture оставляет реальную частичную покупку и ещё одно неотправленное намерение.
func closeFixture(t *testing.T, amount string) (*fillFixture, CycleRunner, *publicFake, s.PaperFillRequest) {
	t.Helper()
	x := emptyRecordedFixture(t)
	r, f := runnerMarket(t, x.in)
	r.DB, r.Scope, r.Owner = x.Pool(), x.r.Key.Scope, x.owner
	f.now = time.Now().UTC().Truncate(time.Minute).Add(-20 * time.Minute)
	x.in.Run.ScheduledAt = f.now
	var err error
	x.p, err = r.observeCycle(t.Context(), x.in.Run, x.in.Purpose, x.in.Profile, nil)
	requireCycleSuccess(t, err)
	x.r.Key.RunID, x.r.Key.InputHash = x.in.Run.RunID, x.p.Hash()
	x.Done(s.CreateOrReadRebalanceRun(t.Context(), x.Pool(), s.RebalanceRunInput(x.in.Run)))
	x.Done(s.StorePaperInput(t.Context(), x.Pool(), x.p.input))
	x.Done(s.StorePlanRevision(t.Context(), x.Pool(), x.p.Plan()))
	for _, original := range x.named(x.in.Run.RunID, nil).OrderIntents()[:2] {
		requireCycleSuccess(t, s.RecordOrderIntent(t.Context(), x.Pool(), original))
	}
	q := cycleAmount(t, amount)
	x.Done(s.RecordPaperFill(t.Context(), x.Pool(), x.request(x.slice(0, model.Zero(), q)), CheckPaperFill(x.p)))
	requireCycleSuccess(t, x.transition(t.Context(), 1, "close-all", "liquidate"))
	f.now = time.Now().UTC().Add(time.Second).Truncate(time.Millisecond)
	return x, r, f, x.request(x.slice(0, q, q))
}

// TestOperatorClosePhases доводит каждый устойчивый префикс до того же аудированного результата.
func TestOperatorClosePhases(t *testing.T) {
	for _, phase := range []string{"partial", "after-abort", "run-only", "saved-input", "partial-close", "dust", "stale"} {
		t.Run(phase, func(t *testing.T) {
			qty := "2"
			if phase == "dust" {
				qty = "0.25"
			}
			x, r, market, late := closeFixture(t, qty)
			initial := x.facts()
			purpose := paperwire.Purpose{Kind: "close-all", CommandID: "liquidate", ControlRevision: 2}
			id := closeRunID(r.Scope, purpose.CommandID)
			expectedAt := initial.Control.CreatedAt.Truncate(time.Microsecond).Add(time.Microsecond)
			if phase == "after-abort" || phase == "run-only" || phase == "saved-input" || phase == "partial-close" {
				a := x.attempt("ABORTED", purpose.CommandID, purpose.ControlRevision)
				a.d.ResolvedAt = initial.Control.CreatedAt
				x.Done(a.run(t.Context(), BuildPaperResolution(x.p, a.d)))
				if phase != "after-abort" {
					candidate := s.RebalanceRunInput{RunID: id, OnboardingID: id, AccountID: r.Scope.AccountID, StrategyID: r.Scope.StrategyID, Config: x.in.Run.Config, ScheduledAt: market.now.Truncate(time.Microsecond)}
					expectedAt = candidate.ScheduledAt
					run, err := s.CreateOrReadRebalanceRun(t.Context(), x.Pool(), candidate)
					requireCycleSuccess(t, err)
					if phase != "run-only" {
						prior, found := x.named(x.in.Run.RunID, nil).Resolution()
						x.Require(found, "отсутствует ABORTED")
						p, err := r.observeCycle(t.Context(), paperwire.RunBinding(run.RebalanceRunInput), purpose, x.in.Profile, &prior)
						requireCycleSuccess(t, err)
						x.Done(s.StorePaperInput(t.Context(), x.Pool(), p.input))
						if phase == "partial-close" {
							x.Done(s.StorePlanRevision(t.Context(), x.Pool(), p.Plan()))
							intent := x.named(id, &prior).OrderIntents()[0]
							requireCycleSuccess(t, s.RecordOrderIntent(t.Context(), x.Pool(), intent))
							fill, err := MakeFillSlice(p, 0, model.Zero(), cycleAmount(t, "1"))
							requireCycleSuccess(t, err)
							x.Done(s.RecordPaperFill(t.Context(), x.Pool(), s.PaperFillRequest{Owner: x.owner, Key: s.PaperWriteKey{Scope: r.Scope, RunID: id, InputHash: p.Hash()}, OrderLinkID: intent.OrderLinkID, Fill: fill}, CheckPaperFill(p)))
						}
					}
					market.now = market.now.Add(time.Minute)
				}
				requireCycleSuccess(t, x.owner.Release(t.Context(), market.now))
				r.DB = pgtest.Existing(t, x.Name(), s.OpenMomentumPostgres)
				id := s.OwnerIdentity{Environment: r.Scope.Environment, AccountID: r.Scope.AccountID, StrategyID: r.Scope.StrategyID, SessionID: "restarted", Host: "test", ProcessID: 2}
				owner, err := s.AcquireExecutionOwner(t.Context(), r.DB, id, market.now)
				requireCycleSuccess(t, err)
				t.Cleanup(func() { x.Done(nil, owner.Release(context.Background(), market.now)) })
				r.Owner, late.Owner = owner, owner
			}
			calls := market.calls
			if phase == "stale" {
				market.edit = func(q *http.Request, data map[string]any) {
					if q.URL.Path == "/v5/market/mark-price-kline" {
						data["list"] = []any{}
					}
				}
			}
			var out PaperCloseResult
			var err error
			if phase == "partial" {
				ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
				defer cancel()
				hold := x.HoldPaperRecoveryRow(ctx, "head", x.in.Run.RunID)
				defer hold.Rollback()
				lateDone := make(chan error, 1)
				go func() {
					_, e := s.RecordPaperFill(ctx, x.Pool(), late, CheckPaperFill(x.p))
					lateDone <- e
				}()
				pid := x.Wait(ctx, hold.QueryRowContext, "momentum.rebalance_runs", "RowShareLock", true, -1)
				closed := make(chan struct{})
				go func() {
					defer close(closed)
					out, err = r.AdvanceCloseAll(ctx, x.in.Run.Config, x.in.Profile)
				}()
				x.Wait(ctx, hold.QueryRowContext, "momentum.rebalance_runs", "RowShareLock", true, pid)
				requireCycleSuccess(t, hold.Commit())
				x.Require(errors.Is(<-lateDone, s.ErrPaperWriteDenied), "конкурентный late fill")
				<-closed
			} else {
				out, err = r.AdvanceCloseAll(t.Context(), x.in.Run.Config, x.in.Profile)
			}
			old := x.named(x.in.Run.RunID, nil)
			abort, done := old.Resolution()
			x.Require(done && abort.Kind() == "ABORTED" && abort.CommandID() == purpose.CommandID && abort.ControlRevision() == 2 && len(abort.Fills()) == 1, "исходный actual prefix")
			x.Require(old.facts.Completion.Outcome == s.RunFailed && len(old.facts.Intents()) == 2 && old.facts.Plan.Hash() == initial.Plan.Hash(), "SQL FAILED и исходный план")
			x.Require(abort.record.Values().ResolvedAt.Equal(initial.Control.CreatedAt) && abort.Fills()[0].ExecutionID == initial.Fills()[abort.Fills()[0].ExecutionID].ExecutionID, "исходное время и исполнение")
			for _, intent := range old.facts.Intents() {
				x.Require(intent.State == "CANCELLED", "не отменён остаток", intent)
			}
			_, denied := s.RecordPaperFill(t.Context(), r.DB, late, CheckPaperFill(x.p))
			x.Require(errors.Is(denied, s.ErrPaperWriteDenied), "позднее старое исполнение", denied)
			if phase == "dust" || phase == "stale" {
				expected, encErr := paperwire.EncodePortfolioState(abort.ActualAfter())
				requireCycleSuccess(t, encErr)
				x.Require(!isZero(abort.ActualAfter().Positions["BTCUSDT"].Quantity) && bytes.Equal(expected, out.Actual), "показан точный ненулевой остаток")
				recovered, readErr := Recover(t.Context(), r.RecoveryRequest)
				requireCycleSuccess(t, readErr)
				status, readErr := PaperCloseState(recovered)
				x.Require(readErr == nil && status.State == "PENDING" && status.ReceiptHash == "" && bytes.Equal(expected, status.Actual), "read-only состояние после отказа", readErr)
				x.Require(errors.Is(err, ErrPaperCloseBlocked) && out.State == "BLOCKED" && x.facts().Control.State.Mode == "HALTED" && x.ReadHistoryAudit(x.Pool()).Fills == 1, "ложное закрытие", err)
				return
			}
			requireCycleSuccess(t, err)
			closed := x.named(id, &abort)
			completed, ok := closed.Resolution()
			x.Require(ok && completed.Hash() == out.ReceiptHash && out.State == "CLOSED" && closed.facts.Control.State.Mode == "HALTED" && completed.Binding().PreviousResolutionHash == abort.Hash(), "проверенный terminal", out)
			x.Require(completed.Binding().ScheduledAt.Equal(expectedAt) && completed.Binding().PreviousInputHash == abort.InputHash() && closed.prepared.Purpose() == purpose && out.RunID == id, "неверная привязка operator run")
			expected, encErr := paperwire.EncodePortfolioState(completed.ActualAfter())
			requireCycleSuccess(t, encErr)
			x.Require(bytes.Equal(expected, out.Actual) && len(completed.Fills()) > 0, "проверенный actual и реальный SELL")
			for _, position := range completed.ActualAfter().Positions {
				x.Require(isZero(position.Quantity), "остаток позиции")
			}
			for _, target := range closed.prepared.Plan().Targets() {
				x.Require(isZero(target.Quantity), "ненулевая цель")
			}
			for _, fill := range completed.Fills() {
				x.Require(fill.Side == model.FillSell, "увеличение позиции")
			}
			if phase == "saved-input" || phase == "partial-close" {
				x.Require(market.calls == calls, "источник при продолжении")
			}
			before, calls := x.ReadHistoryAudit(x.Pool()), market.calls
			repeat, err := r.AdvanceCloseAll(t.Context(), x.in.Run.Config, x.in.Profile)
			x.Require(err == nil && repeat.State == "CLOSED" && repeat.ReceiptHash == out.ReceiptHash && before == x.ReadHistoryAudit(x.Pool()) && calls == market.calls, "точный повтор", err)
		})
	}
}

// TestCloseStateEmpty не трактует отсутствие финансового head как нулевой закрытый портфель.
func TestCloseStateEmpty(t *testing.T) {
	j := pgtest.Journal{T: t}
	var r RecoveredPaper
	r.proof.facts.Scope = s.ControlScope{Environment: "paper", AccountID: "paper/test", StrategyID: "momentum-v1"}
	none, err := PaperCloseState(r)
	j.Require(err == nil && none.State == "IDLE", "исходное состояние", err)
	r.proof.facts.Control = s.PaperCommandState{State: s.ControlState{Mode: "HALTED", Revision: 2}, Action: "HALT", Actor: "OPERATOR", Kind: "close-all", CommandID: "one"}
	pending, err := PaperCloseState(r)
	j.Require(pending.RunID == "paper/control/c2a8def1ad1ffafee9f2697df618d568a790d2bfb71df41215b5c7185bbfcce8", "неканонический ключ operator run")
	j.Require(err == nil && pending.State == "PENDING" && len(pending.Actual) == 0 && pending.ReceiptHash == "", "нулевой head не CLOSED", err)
}

// TestCloseInputBeforePlan достраивает сохранённый план без вымышленных intent и проверяет отдельный нулевой цикл.
func TestCloseInputBeforePlan(t *testing.T) {
	x := emptyRecordedFixture(t)
	r, market := runnerMarket(t, x.in)
	r.DB, r.Scope, r.Owner = x.Pool(), x.r.Key.Scope, x.owner
	market.now = time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	x.in.Run.ScheduledAt = market.now
	p, err := r.observeCycle(t.Context(), x.in.Run, x.in.Purpose, x.in.Profile, nil)
	requireCycleSuccess(t, err)
	x.Done(s.CreateOrReadRebalanceRun(t.Context(), r.DB, s.RebalanceRunInput(x.in.Run)))
	x.Done(s.StorePaperInput(t.Context(), r.DB, p.input))
	x.p = p
	requireCycleSuccess(t, x.transition(t.Context(), 1, "close-all", "from-input"))
	market.now = time.Now().UTC().Add(time.Second).Truncate(time.Millisecond)
	out, err := r.AdvanceCloseAll(t.Context(), x.in.Run.Config, x.in.Profile)
	requireCycleSuccess(t, err)
	old := x.named(x.in.Run.RunID, nil)
	aborted, ok := old.Resolution()
	x.Require(ok && aborted.Kind() == "ABORTED" && old.facts.Plan.Hash() == p.Plan().Hash() && len(old.facts.Intents()) == 0 && len(aborted.Fills()) == 0, "изменён незапланированный префикс")
	terminal := x.named(out.RunID, &aborted)
	flat, ok := terminal.Resolution()
	x.Require(ok && out.State == "CLOSED" && out.ReceiptHash == flat.Hash() && len(flat.Fills()) == 0, "неверный итог закрытия из cash")
}

// TestCloseOwnerDenied проверяет чужой пул и реальную потерю backend до изменения старого цикла.
func TestCloseOwnerDenied(t *testing.T) {
	x, runner, market, _ := closeFixture(t, "1")
	before, calls := x.ReadHistoryAudit(x.Pool()).Financial, market.calls
	foreign := runner
	foreign.DB = pgtest.Existing(t, x.Name(), s.OpenMomentumPostgres)
	got, err := foreign.AdvanceCloseAll(t.Context(), x.in.Run.Config, x.in.Profile)
	x.Require(errors.Is(err, s.ErrOwnerArgument) && got.State == "", "чужой пул", err)
	x.KillOwner(s.ExecutionOwnerLockKey(s.OwnerIdentity{Environment: runner.Scope.Environment, AccountID: runner.Scope.AccountID, StrategyID: runner.Scope.StrategyID}))
	got, err = runner.AdvanceCloseAll(t.Context(), x.in.Run.Config, x.in.Profile)
	x.Require(errors.Is(err, s.ErrOwnerLost) && got.State == "" && before == x.ReadHistoryAudit(x.Pool()).Financial && market.calls == calls, "потерян владелец", err)
}

// TestCloseSupersession завершает прежний partial SELL и закрывает только подтверждённый остаток.
func TestCloseSupersession(t *testing.T) {
	for _, phase := range []string{"replace", "replace-after-abort", "risk-then-replace"} {
		t.Run(phase, func(t *testing.T) {
			ctx := t.Context()
			x, r, market, _ := closeFixture(t, "2")
			market.edit = func(q *http.Request, data map[string]any) {
				if q.URL.Path == "/v5/market/mark-price-kline" {
					data["list"] = []any{}
				}
			}
			first, err := r.AdvanceCloseAll(ctx, x.in.Run.Config, x.in.Profile)
			x.Require(errors.Is(err, ErrPaperCloseBlocked) && first.Revision == 2, "первый close зарегистрирован", err)
			prior, ok := x.named(x.in.Run.RunID, nil).Resolution()
			x.Require(ok && prior.Kind() == "ABORTED", "прежний rebalance")
			market.edit = nil
			run, err := s.CreateOrReadRebalanceRun(ctx, r.DB, s.RebalanceRunInput{RunID: first.RunID, OnboardingID: first.RunID, AccountID: r.Scope.AccountID, StrategyID: r.Scope.StrategyID, Config: x.in.Run.Config, ScheduledAt: market.now})
			requireCycleSuccess(t, err)
			x.p, err = r.observeCycle(ctx, paperwire.RunBinding(run.RebalanceRunInput), paperwire.Purpose{Kind: "close-all", CommandID: first.CommandID, ControlRevision: 2}, x.in.Profile, &prior)
			requireCycleSuccess(t, err)
			x.r.Key.RunID, x.r.Key.InputHash = first.RunID, x.p.Hash()
			x.Done(s.StorePaperInput(ctx, r.DB, x.p.input))
			x.Done(s.StorePlanRevision(ctx, r.DB, x.p.Plan()))
			intent := x.named(first.RunID, &prior).OrderIntents()[0]
			requireCycleSuccess(t, s.RecordOrderIntent(ctx, r.DB, intent))
			one := cycleAmount(t, "1")
			sell := x.request(x.slice(0, model.Zero(), one))
			x.Done(s.RecordPaperFill(ctx, r.DB, sell, CheckPaperFill(x.p)))
			secondRevision := uint64(3)
			if phase == "risk-then-replace" {
				requireCycleSuccess(t, x.transition(ctx, 2, "risk", "risk-stop"))
				risk := x.attempt("ABORTED", "second", 3)
				risk.reject(ctx, risk.build, s.ErrPaperWriteDenied)
				secondRevision++
			}
			requireCycleSuccess(t, x.transition(ctx, secondRevision-1, "close-all", "second"))
			complete := x.attempt("COMPLETED", "", 0)
			complete.reject(ctx, complete.build, s.ErrPaperWriteDenied)
			x.reject(x.request(x.slice(0, one, one)), CheckPaperFill(x.p), s.ErrPaperWriteDenied)
			stale := x.attempt("ABORTED", first.CommandID, 2)
			stale.reject(ctx, stale.build, s.ErrPaperWriteDenied)
			wrongID := x.attempt("ABORTED", first.CommandID, secondRevision)
			wrongID.reject(ctx, wrongID.build, s.ErrPaperWriteDenied)
			wrongRevision := x.attempt("ABORTED", "second", secondRevision-1)
			wrongRevision.reject(ctx, wrongRevision.build, s.ErrPaperWriteDenied)
			currentID, revision := "second", secondRevision
			if phase == "replace-after-abort" {
				abort := x.attempt("ABORTED", currentID, revision)
				abort.d.ResolvedAt = x.facts().Control.CreatedAt
				sealed, err := abort.run(ctx, BuildPaperResolution(x.p, abort.d))
				x.Require(err == nil && sealed.Confirmed && x.facts().Intents()[intent.OrderLinkID].State == "PARTIALLY_FILLED", "ABORTED перед третьей командой", err)
				requireCycleSuccess(t, x.transition(ctx, revision, "close-all", "third"))
				currentID, revision = "third", secondRevision+1
				requireCycleSuccess(t, r.Owner.Release(ctx, market.now))
				r.DB = pgtest.Existing(t, x.Name(), s.OpenMomentumPostgres)
				id := s.OwnerIdentity{Environment: r.Scope.Environment, AccountID: r.Scope.AccountID, StrategyID: r.Scope.StrategyID, SessionID: "third-owner", Host: "test", ProcessID: 3}
				r.Owner, err = s.AcquireExecutionOwner(ctx, r.DB, id, market.now)
				requireCycleSuccess(t, err)
				t.Cleanup(func() { x.Done(nil, r.Owner.Release(context.Background(), market.now)) })
			}
			market.now = time.Now().UTC().Add(time.Second).Truncate(time.Millisecond)
			out, err := r.AdvanceCloseAll(ctx, x.in.Run.Config, x.in.Profile)
			requireCycleSuccess(t, err)
			old := x.named(first.RunID, &prior)
			aborted, ok := old.Resolution()
			x.Require(ok && aborted.Kind() == "ABORTED" && aborted.CommandID() == "second" && aborted.ControlRevision() == secondRevision && old.facts.Completion.Outcome == s.RunFailed, "неверное завершение первого close")
			x.Require(len(aborted.Fills()) == 1 && aborted.Fills()[0].ExecutionID == sell.Fill.ExecutionID && old.facts.Plan.Hash() == x.p.Plan().Hash() && old.facts.Intents()[intent.OrderLinkID].State == "CANCELLED", "потерян префикс старого SELL")
			terminal, ok := x.named(out.RunID, &aborted).Resolution()
			x.Require(ok && out.CommandID == currentID && out.Revision == revision && out.State == "CLOSED" && out.ReceiptHash == terminal.Hash() && terminal.Binding().PreviousResolutionHash == aborted.Hash(), "неверный исход замены", out)
			fills := terminal.Fills()
			x.Require(len(fills) == 1 && fills[0].Side == model.FillSell && decimalEqual(fills[0].Quantity, one) && isZero(terminal.ActualAfter().Positions[intent.Symbol].Quantity), "повторно продано больше остатка")
			before, calls := x.ReadHistoryAudit(r.DB), market.calls
			repeat, err := r.AdvanceCloseAll(ctx, x.in.Run.Config, x.in.Profile)
			x.Require(err == nil && repeat.ReceiptHash == out.ReceiptHash && before == x.ReadHistoryAudit(r.DB) && calls == market.calls, "повтор изменил подтверждённый итог", err)
		})
	}
}
