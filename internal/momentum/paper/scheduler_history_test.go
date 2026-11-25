package paper

import (
	"errors"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

// TestSchedulerFinancialHistory проверяет оба порядка недельного слота с реальными close и RESUME.
func TestSchedulerFinancialHistory(t *testing.T) {
	for _, bare := range []bool{false, true} {
		name := "weekly"
		if bare {
			name = "bare-onboarding"
		}
		t.Run(name, func(t *testing.T) {
			x := emptyRecordedFixture(t)
			ctx, db := t.Context(), x.Pool()
			x.p = preparedFixture(t, x.in)
			runner, market := runnerMarket(t, x.in)
			runner.DB, runner.Scope, runner.Owner = db, x.r.Key.Scope, x.owner
			data := &fakeData{through: monday}
			scheduler := Scheduler{Store: DBRunStore{DB: db}, Data: data, Config: x.in.Run.Config,
				AccountID: runner.Scope.AccountID, OnboardingID: "initial", Deadline: time.Hour, Poll: time.Minute}
			market.now = monday.Add(-48 * time.Hour)
			initial := tick(t, scheduler, market.now, ActionRun)
			if !bare {
				v, err := runner.AdvanceCycle(ctx, initial.Run, x.in.Purpose, x.in.Profile)
				requireCycleSuccess(t, err)
				_, resolved := v.Resolution()
				x.Require(resolved && len(v.facts.Fills()) > 0, "первый вход купил позиции")
			}
			requireCycleSuccess(t, x.transition(ctx, 1, "close-all", "close"))
			market.now = monday.Add(10 * time.Minute)
			if bare {
				market.now = monday.Add(-10 * time.Minute)
			}
			candidate := scheduler.input("operator/", "close", market.now)
			run, err := s.CreateOrReadRebalanceRun(ctx, db, candidate)
			requireCycleSuccess(t, err)
			v, err := runner.AdvanceCycle(ctx, run, paperwire.Purpose{Kind: "close-all", CommandID: "close", ControlRevision: 2}, x.in.Profile)
			requireCycleSuccess(t, err)
			terminal, ok := v.Resolution()
			x.Require(ok && terminal.Kind() == "COMPLETED" && terminal.Binding().ScheduledAt.Equal(market.now), "точный итог close")
			x.Require((len(terminal.Fills()) == 0) == bare, "число исполнений close соответствует позиции")
			for _, fill := range terminal.Fills() {
				x.Require(fill.Side == model.FillSell, "close продал позицию")
			}
			for _, pos := range terminal.ActualAfter().Positions {
				x.Require(isZero(pos.Quantity), "остаток после close")
			}
			z := x.resumeCase()
			resumed, err := ResumePaperCommand(ctx, db, z.inCommand, x.owner, z.p)
			x.Require(err == nil && resumed.AppliedConfirmed && resumed.Current.State.Mode == "ACTIVE", "снятие HALT", err)
			wake := monday.Add(15 * time.Minute)
			recovered, err := Recover(ctx, runner.RecoveryRequest)
			requireCycleSuccess(t, err)
			data.err = errors.New("ранняя проверка рынка")
			before := x.ReadHistoryAudit(db)
			dec, err := scheduler.TickRecovered(ctx, wake, recovered)
			requireCycleSuccess(t, err)
			wantRun := scheduler.input("weekly/", "", monday).RunID
			wantWake := monday.AddDate(0, 0, 7)
			if bare {
				wantRun, wantWake = initial.Run.RunID, wake
			}
			x.Require(dec.Action == ActionSkipped && dec.Run.RunID == wantRun && dec.WakeAt.Equal(wantWake) && dec.Reason == SkipFinancialHistory, "неверно пропущенный слот", dec)
			row, ok, err := s.ReadRunCompletion(ctx, db, wantRun)
			x.Require(err == nil && ok && row.RunID == wantRun && row.Outcome == s.RunSkipped && row.Reason == SkipFinancialHistory && row.PlanHash == "" && row.PlanRevision == 0 && !row.CompletedAt.IsZero(), "нет сохранённого SKIPPED", err)
			_, planned, err := s.ReadLatestPlanRevision(ctx, db, wantRun)
			x.Require(err == nil && !planned, "появился план пропущенного слота", err)
			after := x.ReadHistoryAudit(db)
			x.Require(after.Inputs == before.Inputs && after.Intents == before.Intents && after.Fills == before.Fills && after.Postings == before.Postings && after.Fees == before.Fees, "пропуск изменил финансовые строки")
			if !bare {
				again, err := scheduler.TickRecovered(ctx, wake, recovered)
				x.Require(err == nil && again.Action == ActionWait && again.WakeAt.Equal(wantWake) && after == x.ReadHistoryAudit(db), "повтор недельного слота", err)
			}
			data.err = nil
			data.through = wantWake.Truncate(24 * time.Hour)
			market.now = wantWake
			dec, err = scheduler.TickRecovered(ctx, wantWake, recovered)
			x.Require(err == nil && dec.Action == ActionRun && !dec.Resume && dec.Run.OnboardingID == "" && dec.Run.ScheduledAt.Equal(mondayOf(wantWake)), "пропущена первая доступная неделя", err)
			v, err = runner.AdvanceCycle(ctx, dec.Run, x.in.Purpose, x.in.Profile)
			requireCycleSuccess(t, err)
			next, ok := v.Resolution()
			x.Require(ok && next.Kind() == "COMPLETED" && next.Binding().PreviousResolutionHash == terminal.Hash(), "новая неделя продолжила историю close")
		})
	}
}

// TestSchedulerResolvedBoundary сохраняет приоритет плана и итогов, проверяет равную границу без I/O рынка.
func TestSchedulerResolvedBoundary(t *testing.T) {
	scheduler, store, data := testScheduler(t, "next_monday", "")
	data.err = errors.New("нет данных")
	run, err := store.CreateOrReadRebalanceRun(t.Context(), scheduler.input("weekly/", "", monday))
	requireCycleSuccess(t, err)
	store.planned[run.RunID] = true
	d, err := scheduler.tick(t.Context(), monday, monday)
	if err != nil || d.Action != ActionRun || !d.Resume || d.Run.RunID != run.RunID {
		t.Fatal(d, err)
	}
	delete(store.planned, run.RunID)
	d, err = scheduler.tick(t.Context(), monday, monday)
	if err != nil || d.Action != ActionSkipped || d.Reason != SkipFinancialHistory {
		t.Fatal(d, err)
	}
	d, err = scheduler.tick(t.Context(), monday, monday)
	if err != nil || d.Action != ActionWait || len(store.done) != 1 {
		t.Fatal(d, err)
	}
	if _, err = scheduler.TickRecovered(t.Context(), monday, RecoveredPaper{}); !errors.Is(err, ErrSchedulerConfig) {
		t.Fatal(err)
	}
}
