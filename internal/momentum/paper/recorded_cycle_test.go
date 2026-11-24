package paper

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
	s "bybit_bot_liquidity/internal/momentum/storage"
	"bybit_bot_liquidity/internal/momentum/storage/pgtest"
)

// named читает run новым пулом после фактических записей.
func (x *fillFixture) named(run string, prev *ResolvedCycle) RecordedPaperCycle {
	x.Helper()
	db := pgtest.Existing(x.T, x.Name(), s.OpenMomentumPostgres)
	defer db.Close()
	v, err := ReadRecordedPaperCycle(x.Context(), db, x.r.Key.Scope, run, prev, x.in.PlanBuilder, x.in.Admission)
	requireCycleSuccess(x.T, err)
	return v
}

// rejectRecorded требует типизированного отказа с полностью нулевым результатом.
func (x *fillFixture) rejectRecorded(ctx context.Context, db *sql.DB, run string, prev *ResolvedCycle, build PlanBuilder, admit FillAdmission, want error) error {
	x.Helper()
	v, err := ReadRecordedPaperCycle(ctx, db, x.r.Key.Scope, run, prev, build, admit)
	x.Require(reflect.DeepEqual(v, RecordedPaperCycle{}) && errors.Is(err, want), "категория или результат", err)
	return err
}

// TestRecordedArguments проверяет аргументы, отсутствующий run, отмену и закрытый пул.
func TestRecordedArguments(t *testing.T) {
	x := emptyRecordedFixture(t)
	x.p = preparedFixture(t, x.in)
	x.r.Key.RunID = x.in.Run.RunID
	x.Done(s.CreateOrReadRebalanceRun(t.Context(), x.Pool(), s.RebalanceRunInput(x.in.Run)))
	x.Done(s.StorePaperInput(t.Context(), x.Pool(), x.p.input))
	x.Done(s.StorePlanRevision(t.Context(), x.Pool(), x.p.Plan()))
	all := x.named(x.r.Key.RunID, nil).OrderIntents()
	x.Require(len(all) > 1, "несколько исходных намерений")
	requireCycleSuccess(t, s.RecordOrderIntent(t.Context(), x.Pool(), all[0]))
	view := x.named(x.r.Key.RunID, nil)
	x.Require(len(view.facts.Intents()) == 1 && reflect.DeepEqual(view.OrderIntents(), all), "часть исходных намерений")
	ctx, db, run, b, a := t.Context(), x.Pool(), x.r.Key.RunID, x.in.PlanBuilder, x.in.Admission
	x.rejectRecorded(nil, db, run, nil, b, a, ErrRecordedCycleArgument) //nolint:staticcheck // Проверяем отказ при явно отсутствующем контексте.
	x.rejectRecorded(ctx, nil, run, nil, b, a, ErrRecordedCycleArgument)
	x.rejectRecorded(ctx, db, "", nil, b, a, ErrRecordedCycleArgument)
	x.rejectRecorded(ctx, db, run, nil, nil, a, ErrRecordedCycleArgument)
	x.rejectRecorded(ctx, db, run, nil, b, nil, ErrRecordedCycleArgument)
	x.rejectRecorded(ctx, db, "absent", nil, b, a, ErrRecordedCycleAbsent)
	x.rejectRecorded(ctx, db, run, &ResolvedCycle{}, b, a, ErrRecordedCycleCorrupt)
	x.rejectRecorded(ctx, db, run, nil, singleTarget(model.Zero()), a, ErrRecordedCycleCorrupt)
	veto := errors.New("admission veto")
	deny := func(model.TradePlan, map[string]planner.MarketDeltaRules, model.Decimal, map[string]model.Decimal, map[string]model.Decimal) error {
		return veto
	}
	x.Require(errors.Is(x.rejectRecorded(ctx, db, run, nil, b, deny, ErrRecordedCycleCorrupt), veto), "причина отказа допуска")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	x.Require(errors.Is(x.rejectRecorded(cancelled, db, run, nil, b, a, ErrRecordedCycleUnavailable), context.Canceled), "причина отмены")
	closed := pgtest.Existing(t, x.Name(), s.OpenMomentumPostgres)
	requireCycleSuccess(t, closed.Close())
	x.rejectRecorded(ctx, closed, run, nil, b, a, ErrRecordedCycleUnavailable)
}

// TestRecordedCorrupt применяет ровно одно повреждение к исходному исправному журналу.
func TestRecordedCorrupt(t *testing.T) {
	for _, kind := range []string{"missing-fee", "unknown", "extra", "overfill", "latest", "forged", "wrong-link", "unjournaled", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			x := newFillFixture(t, kind == "wrong-link")
			ctx, e := t.Context(), x.r
			switch kind {
			case "missing-fee":
				requireCycleSuccess(t, x.record(e))
				x.RemoveFees()
			case "unknown", "cancelled":
				state := map[string]string{"unknown": "UNKNOWN", "cancelled": "CANCELLED"}[kind]
				requireCycleSuccess(t, s.SetOrderIntentState(ctx, x.Pool(), "paper", x.in.Run.AccountID, e.OrderLinkID, state))
			case "extra":
				e.Fill.ExecutionID += "extra"
				requireCycleSuccess(t, x.record(e))
			case "overfill":
				requireCycleSuccess(t, x.record(e))
				requireCycleSuccess(t, x.Pending(e.Fill.ExecutionID, e.Fill.ExecutionID+"more").Commit())
			case "wrong-link":
				e.OrderLinkID = x.slice(1, model.Zero(), x.p.ExpectedFills()[1].Quantity).OrderLinkID
				requireCycleSuccess(t, x.record(e))
				x.Done(ProjectProgress(x.p, []model.ConfirmedFill{e.Fill}))
			case "forged":
				r := replayedReceipt(t, x.p, []model.ConfirmedFill{e.Fill}, x.attempt("ABORTED", "close", 2).d)
				x.Done(s.CompleteRebalanceRun(ctx, x.Pool(), s.RunCompletion{RunID: x.in.Run.RunID, Outcome: s.RunFailed, PlanHash: x.p.Plan().Hash(), PlanRevision: 1, Reason: string(r.Payload())}))
			case "latest":
				p := x.p.Plan()
				meta := p.Metadata()
				meta.Revision, meta.PreviousHash = 2, p.Hash()
				revised, err := model.NewTradePlan(model.PlanInput{Snapshot: p.Snapshot(), Targets: p.Targets(), Metadata: meta})
				requireCycleSuccess(t, err)
				x.Done(s.StorePlanRevision(ctx, x.Pool(), revised))
			case "unjournaled":
				x.RemovePaperInput(x.in.Run.RunID)
			}
			x.rejectRecorded(ctx, x.Pool(), x.r.Key.RunID, nil, x.in.PlanBuilder, x.in.Admission, ErrRecordedCycleCorrupt)
		})
	}
}

// TestRecordedSnapshot фиксирует второй slice во время replay первого снимка.
func TestRecordedSnapshot(t *testing.T) {
	x := newFillFixture(t, false)
	one := cycleAmount(t, "1")
	requireCycleSuccess(t, x.record(x.slice(0, model.Zero(), one)))
	second := x.slice(0, one, one)
	b := func(id string, m model.PlanMetadata, snap model.PortfolioSnapshot, days map[string][]data.DailyClose, rules map[string]planner.MarketDeltaRules, equity model.Decimal) (model.TradePlan, error) {
		x.Done(s.RecordPaperFill(t.Context(), x.Pool(), x.request(second), CheckPaperFill(x.p)))
		return x.in.PlanBuilder(id, m, snap, days, rules, equity)
	}
	before, err := ReadRecordedPaperCycle(t.Context(), x.Pool(), x.r.Key.Scope, x.r.Key.RunID, nil, b, x.in.Admission)
	requireCycleSuccess(t, err)
	rows := x.Snapshot()
	after := x.named(x.r.Key.RunID, nil)
	old, ok := before.Progress()
	fresh, found := after.Progress()
	x.Require(ok && found && len(old.Fills()) == 1 && len(fresh.Fills()) == 2 && before.DataHash() != after.DataHash() && x.Snapshot() == rows, "снимки и строки")
	intents := after.OrderIntents()
	x.Require(decimalEqual(intents[0].Quantity, x.r.Fill.Quantity), "исходное полное количество")
	intents[0].Symbol = ""
	x.Require(after.OrderIntents()[0].Symbol == x.r.Fill.Symbol, "изменяемая копия")
}

// TestRecordedWide восстанавливает длинный NUMERIC и сохранённый итог широкого портфеля.
func TestRecordedWide(t *testing.T) {
	x := newFillFixture(t, false)
	den := new(big.Int).Lsh(big.NewInt(1), 4076)
	tiny, err := cycleAmount(t, "1").Quo(cycleAmount(t, den.String()))
	requireCycleSuccess(t, err)
	e := x.slice(0, model.Zero(), tiny)
	write, err := s.RecordPaperFill(t.Context(), x.Pool(), x.request(e), CheckPaperFill(x.p))
	x.Require(err == nil && write.Confirmed && !write.InsertedKnown, "postcommit", err)
	wide, err := tiny.WireDecimal()
	x.Require(err == nil && len(wide) > 128 && decimalEqual(write.FilledQuantity, tiny), "конечный NUMERIC", err)
	x.Require(errors.Is(x.record(e), s.ErrExecution), "legacy ParseQty после commit")
	x.observe(e, "PARTIALLY_FILLED", tiny, nil)
	requireCycleSuccess(t, x.transition(t.Context(), 1, "close-all", "close"))
	a := x.attempt("ABORTED", "close", 2)
	result, err := a.run(t.Context(), a.build)
	requireCycleSuccess(t, err)
	want := a.proof(result, []model.ConfirmedFill{e.Fill})
	requireCycleSuccess(t, s.SetOrderIntentState(t.Context(), x.Pool(), "paper", x.in.Run.AccountID, e.OrderLinkID, "CANCELLED"))
	r, ok := x.named(x.r.Key.RunID, nil).Resolution()
	x.Require(ok && r.Hash() == want.Hash() && len(r.Payload()) > 10*1024, "широкий receipt")
	n, _, _ := r.ActualAfter().WalletUSDT.CanonicalRatio()
	num, ok := new(big.Int).SetString(n, 10)
	x.Require(ok && num.BitLen() == 4096, "широкий wallet")
}

// emptyRecordedFixture создаёт область до регистрации первого run.
func emptyRecordedFixture(t *testing.T) *fillFixture {
	x := &fillFixture{Journal: pgtest.OpenJournal(t, s.OpenMomentumPostgres), in: cycleFixture(t)}
	ctx, db := t.Context(), x.Pool()
	requireCycleSuccess(t, s.MigrateMomentumPostgres(ctx, db))
	x.in.Run.OnboardingID = ""
	scope := s.ControlScope{Environment: "paper", AccountID: x.in.Run.AccountID, StrategyID: x.in.Run.StrategyID}
	x.r.Key.Scope = scope
	owner, err := s.AcquireExecutionOwner(ctx, db, s.OwnerIdentity{SessionID: "owner", Host: "test", ProcessID: 1, Environment: "paper", AccountID: scope.AccountID, StrategyID: scope.StrategyID}, time.Now())
	requireCycleSuccess(t, err)
	x.owner = owner
	t.Cleanup(func() { x.owner.Release(context.Background(), time.Now()) })
	_, hash, err := x.in.Run.Config.Snapshot()
	requireCycleSuccess(t, err)
	x.Done(s.ApplyPaperCommandState(ctx, db, s.PaperCommandRequest{Scope: scope, Config: &x.in.Run.Config, ConfigHash: hash, Action: "INITIALIZE", Actor: "SYSTEM", ActorID: "system/test", CommandID: "init", Kind: "initialize", Reason: "genesis"}))
	return x
}

// TestRecordedThirds читает одну стратегию трёх циклов с целями из независимой таблицы.
func TestRecordedThirds(t *testing.T) {
	x := emptyRecordedFixture(t)
	ctx, db := t.Context(), x.Pool()
	start := x.in.Run.ScheduledAt
	targets := map[time.Time]string{start: "1", start.Add(24 * time.Hour): "3", start.Add(48 * time.Hour): "1"}
	x.in.PlanBuilder = func(id string, m model.PlanMetadata, snap model.PortfolioSnapshot, days map[string][]data.DailyClose, rules map[string]planner.MarketDeltaRules, equity model.Decimal) (model.TradePlan, error) {
		raw, ok := targets[m.ScheduledAt]
		if !ok {
			return model.TradePlan{}, ErrPaperCycle
		}
		return singleTarget(cycleAmount(t, raw))(id, m, snap, days, rules, equity)
	}
	var last ResolvedCycle
	firstRun, firstHash := x.in.Run.RunID, ""
	for i, raw := range []string{"0.2", "0.4", "0.4"} {
		if i > 0 {
			x.in = nextCycle(t, x.in, last, false)
		}
		price := cycleAmount(t, raw)
		for _, w := range x.in.Observation.TradeWindows {
			if w.Interval == "60" {
				for k := range w.Page.Candles {
					c := &w.Page.Candles[k]
					c.Open, c.High, c.Low, c.Close = price, price, price, price
				}
			}
		}
		x.p = preparedFixture(t, x.in)
		run := x.in.Run.RunID
		x.r.Key.RunID, x.r.Key.InputHash = run, x.p.Hash()
		x.Done(s.CreateOrReadRebalanceRun(ctx, db, s.RebalanceRunInput(x.in.Run)))
		view := x.named(run, x.in.Previous)
		_, p := view.Prepared()
		_, g := view.Progress()
		_, r := view.Resolution()
		x.Require(view.DataHash() != "" && !p && !g && !r && !view.PlanFound() && len(view.OrderIntents()) == 0, "run-only")
		x.Done(s.StorePaperInput(ctx, db, x.p.input))
		view = x.named(run, x.in.Previous)
		prepared, p := view.Prepared()
		progress, g := view.Progress()
		x.Require(p && g && prepared.Hash() == x.p.Hash() && !view.PlanFound() && len(progress.Fills()) == 0, "input до плана")
		x.Done(s.StorePlanRevision(ctx, db, x.p.Plan()))
		view = x.named(run, x.in.Previous)
		x.Require(view.PlanFound() && len(view.OrderIntents()) == 1 && len(view.facts.Intents()) == 0, "план до intent")
		intent := view.OrderIntents()[0]
		requireCycleSuccess(t, s.RecordOrderIntent(ctx, db, intent))
		e := x.slice(0, model.Zero(), x.p.ExpectedFills()[0].Quantity)
		x.Done(s.RecordPaperFill(ctx, db, x.request(e), CheckPaperFill(x.p)))
		progress, g = x.named(run, x.in.Previous).Progress()
		x.Require(g && progress.Complete() && len(progress.Fills()) == 1, "полное исполнение")
		a := x.attempt("COMPLETED", "", 0)
		result, err := a.run(ctx, a.build)
		requireCycleSuccess(t, err)
		want := a.proof(result, []model.ConfirmedFill{e.Fill})
		last, r = x.named(run, x.in.Previous).Resolution()
		x.Require(r && last.Hash() == want.Hash(), "durable receipt")
		if i == 0 {
			firstHash = last.Hash()
		} else {
			x.rejectRecorded(ctx, db, run, nil, x.in.PlanBuilder, x.in.Admission, ErrRecordedCycleCorrupt)
			old, ok := x.named(firstRun, nil).Resolution()
			x.Require(ok && old.Hash() == firstHash, "исторический run")
		}
	}
	n, d, _ := last.ActualAfter().Positions["BTCUSDT"].CostBasisUSDT.CanonicalRatio()
	x.Require(n == "1" && d == "3", "точная себестоимость", n, d)
}
