package paper

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

type checkedGate struct {
	proof  RecoveryProof
	call   func(context.Context, s.PaperRecoveryStamp, func(s.PaperRunFacts) error) error
	verify func(bool)
}
type gateFactory func(*fillFixture) checkedGate

// freshProof восстанавливает текущий head с его фактическим предыдущим финансовым итогом.
func (x *fillFixture) freshProof() RecoveryProof {
	r, err := Recover(x.Context(), RecoveryRequest{x.Pool(), x.r.Key.Scope, x.in.PlanBuilder, x.in.Admission})
	requireCycleSuccess(x.T, err)
	return r.Proof()
}

// takeOwner открывает самостоятельную сессию необходимой области.
func (x *fillFixture) takeOwner(id string) *s.ExecutionOwner {
	who := s.OwnerIdentity{SessionID: id, Environment: "paper", AccountID: x.in.Run.AccountID, StrategyID: x.in.Run.StrategyID, Host: "test", ProcessID: 2}
	owner, err := s.AcquireExecutionOwner(x.Context(), x.Pool(), who, time.Now())
	requireCycleSuccess(x.T, err)
	x.Cleanup(func() { owner.Release(context.Background(), time.Now()) })
	return owner
}

// beginReconcile показывает реальный takeover и переиспользуемую проверку latch.
func beginReconcile(x *fillFixture) checkedGate {
	x.KillOwner(s.ExecutionOwnerLockKey(s.OwnerIdentity{Environment: "paper", AccountID: x.in.Run.AccountID, StrategyID: x.in.Run.StrategyID}))
	previous := x.owner
	x.owner = x.takeOwner("new")
	x.Require(errors.Is(previous.Permit(x.Context()), s.ErrOwnerLost), "освобождён backend прежнего владельца")
	x.Require(x.owner.NeedsReconciliation() && errors.Is(x.owner.Permit(x.Context()), s.ErrOwnerReconciliationRequired), "нет latch")
	return checkedGate{x.freshProof(), func(ctx context.Context, stamp s.PaperRecoveryStamp, check func(s.PaperRunFacts) error) error {
		return s.ReconcilePaperOwner(ctx, x.Pool(), x.owner, stamp, check)
	}, func(ok bool) { x.Require(x.owner.NeedsReconciliation() != ok, "неверный latch") }}
}

// haltedTerminal фиксирует настоящий ABORTED через обычную финализацию #497.
func haltedTerminal(t *testing.T, x *fillFixture, q model.Decimal) ResolvedCycle {
	e := x.slice(0, model.Zero(), q)
	x.Done(s.RecordPaperFill(t.Context(), x.Pool(), x.request(e), CheckPaperFill(x.p)))
	requireCycleSuccess(t, x.transition(t.Context(), 1, "close-all", "close"))
	return x.attempt("ABORTED", "close", 2).finish([]model.ConfirmedFill{e.Fill})
}

// checkRecoveryFences проверяет блокировку пишущих PostgreSQL-транзакций в реальной точке допуска.
func checkRecoveryFences(t *testing.T, begin gateFactory) {
	sharedDamage(t, begin)
	sharedDenials(t, begin)
	for _, kind := range []string{"sibling-plan", "revision", "completion", "intent", "ledger", "owner"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			x := newFillFixture(t, false)
			requireCycleSuccess(t, x.transition(ctx, 1, "close-all", "close"))
			g := begin(x)
			done := make(chan error, 1)
			report := func(_ any, err error) { done <- err }
			var write func()
			table, mode, granted := "momentum.order_intents", "RowExclusiveLock", false
			switch kind {
			case "sibling-plan", "revision":
				meta := x.p.Plan().Metadata()
				if kind == "sibling-plan" {
					sibling := x.in.Run
					sibling.RunID, sibling.ScheduledAt = "sibling", sibling.ScheduledAt.Add(24*time.Hour)
					x.Done(s.CreateOrReadRebalanceRun(ctx, x.Pool(), s.RebalanceRunInput(sibling)))
					meta.RunID, meta.ScheduledAt = sibling.RunID, sibling.ScheduledAt
				} else {
					meta.Revision, meta.PreviousHash = 2, x.p.Plan().Hash()
				}
				plan, err := model.NewTradePlan(model.PlanInput{Snapshot: x.p.Plan().Snapshot(), Targets: x.p.Plan().Targets(), Metadata: meta})
				requireCycleSuccess(t, err)
				write = func() { report(s.StorePlanRevision(ctx, x.Pool(), plan)) }
				table = "momentum.plan_revisions"
			case "completion":
				r := replayedReceipt(t, x.p, nil, ResolutionDisposition{"ABORTED", "close", 2, x.in.Run.ScheduledAt})
				completion := s.RunCompletion{RunID: x.r.Key.RunID, Outcome: s.RunFailed, PlanRevision: 1, PlanHash: x.p.Plan().Hash(), Reason: string(r.Payload())}
				write = func() { report(s.CompleteRebalanceRun(ctx, x.Pool(), completion)) }
				table, granted = "momentum.run_completions", true
			case "intent":
				write = func() {
					done <- s.SetOrderIntentState(ctx, x.Pool(), "paper", x.in.Run.AccountID, x.r.OrderLinkID, "UNKNOWN")
				}
			case "ledger":
				entry := s.CashLedgerEntry{Environment: "paper", AccountID: x.in.Run.AccountID, Kind: "TRANSFER", SourceID: "unexpected", AmountUSDT: cycleAmount(t, "1"), OccurredAt: x.in.Run.ScheduledAt}
				write = func() { report(s.RecordCashLedgerEntry(ctx, x.Pool(), entry)) }
				table = "momentum.ledger_entries"
			case "owner":
				write = func() { done <- x.EndRecoverySession(ctx, g.proof.OwnerSessionID()) }
				table, granted = "momentum.execution_owner_sessions", true
			}
			err := g.call(ctx, g.proof.recoveryStamp(), func(f s.PaperRunFacts) error {
				requireCycleSuccess(t, g.proof.CheckData(f))
				go write()
				x.Wait(ctx, x.Pool().QueryRowContext, table, mode, granted, 0)
				return nil
			})
			requireCycleSuccess(t, err)
			requireCycleSuccess(t, <-done)
			if kind == "sibling-plan" || kind == "ledger" {
				_, e := s.ReadPaperRunFacts(ctx, x.Pool(), x.r.Key.Scope, "")
				x.Require(errors.Is(e, s.ErrPaperFactsCorrupt), "неучтённые эффекты", e)
			}
			g.verify(true)
		})
	}
}

// sharedDamage проводит широкое представление и отказ повреждённых данных через обе точки входа.
func sharedDamage(t *testing.T, begin gateFactory) {
	for _, kind := range []string{"wide", "head", "fill", "audit", "config", "receipt", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			ctx := t.Context()
			x := newFillFixture(t, false)
			den := new(big.Int).Lsh(big.NewInt(1), 4076)
			q, err := cycleAmount(t, "1").Quo(cycleAmount(t, den.String()))
			requireCycleSuccess(t, err)
			e := x.slice(0, model.Zero(), q)
			receipt := haltedTerminal(t, x, q)
			requireCycleSuccess(t, s.SetOrderIntentState(ctx, x.Pool(), "paper", x.in.Run.AccountID, e.OrderLinkID, "CANCELLED"))
			g := begin(x)
			want := s.ErrPaperFenceConflict
			switch kind {
			case "wide":
				raw, err := q.WireDecimal()
				n, _, _ := receipt.ActualAfter().WalletUSDT.CanonicalRatio()
				num, ok := new(big.Int).SetString(n, 10)
				x.Require(err == nil && len(raw) > 128 && ok && num.BitLen() == 4096, "wide fixture")
				err = g.call(ctx, g.proof.recoveryStamp(), func(f s.PaperRunFacts) error {
					x.Require(f.Fills()[e.Fill.ExecutionID].Quantity == raw && f.Completion.Reason == string(receipt.Payload()) && f.DataHash == g.proof.DataHash(), "неполный CURRENT prefix")
					return g.proof.CheckData(f)
				})
				requireCycleSuccess(t, err)
				g.verify(true)
				return
			case "head":
				x.RemovePaperInput(x.r.Key.RunID)
				want = s.ErrPaperFenceStale
			case "fill":
				x.RemoveFees()
			case "audit", "config", "receipt":
				x.DamagePaperRecoveryFact(ctx, kind, x.r.Key.RunID)
			case "foreign":
				x.ReplaceOwner(g.proof.OwnerSessionID(), "other")
				want = s.ErrPaperFenceDenied
			}
			err = g.call(ctx, g.proof.recoveryStamp(), g.proof.CheckData)
			x.Require(errors.Is(err, want), "повреждение принято", kind, err)
			g.verify(false)
		})
	}
}

// sharedDenials проверяет исходный gate при подмене stamp, отмене или потере backend до COMMIT.
func sharedDenials(t *testing.T, begin gateFactory) {
	for _, kind := range []string{"data", "owner", "veto", "loss", "cancel", "head", "old-control", "nil-check", "pending"} {
		t.Run(kind, func(t *testing.T) {
			x := newFillFixture(t, false)
			ctx, stop := context.WithTimeout(t.Context(), 15*time.Second)
			defer stop()
			requireCycleSuccess(t, x.transition(ctx, 1, "pause", "pause"))
			g := begin(x)
			stamp, check := g.proof.recoveryStamp(), g.proof.CheckData
			want := s.ErrPaperFenceStale
			switch kind {
			case "data":
				stamp.DataHash = strings.Repeat("0", 64)
			case "owner":
				stamp.OwnerSessionID = "other"
				want = s.ErrPaperFenceDenied
			case "nil-check":
				check = nil
			case "old-control":
				stamp.ControlRevision++
			case "veto":
				want = s.ErrPaperFenceRejected
				check = func(f s.PaperRunFacts) error {
					requireCycleSuccess(t, g.proof.CheckData(f))
					return errors.New("отказ")
				}
			case "loss":
				want = s.ErrPaperFenceUnknown
				check = func(f s.PaperRunFacts) error {
					requireCycleSuccess(t, g.proof.CheckData(f))
					x.KillOwner(s.ExecutionOwnerLockKey(s.OwnerIdentity{Environment: "paper", AccountID: x.in.Run.AccountID, StrategyID: x.in.Run.StrategyID}))
					return nil
				}
			case "head":
				stamp.HeadFound = false
			case "pending":
				held := x.HoldPaperRecoveryRow(ctx, "intent", x.r.OrderLinkID)
				updated := make(chan error, 1)
				go func() {
					updated <- s.SetOrderIntentState(ctx, x.Pool(), "paper", x.in.Run.AccountID, x.r.OrderLinkID, "CANCELLED")
				}()
				pid := x.Wait(ctx, held.QueryRowContext, "momentum.order_intents", "RowExclusiveLock", true, -1)
				done := make(chan error, 1)
				go func() { done <- g.call(ctx, stamp, check) }()
				x.Wait(ctx, x.Pool().QueryRowContext, "momentum.order_intents", "ShareLock", false, pid)
				requireCycleSuccess(t, held.Commit())
				requireCycleSuccess(t, <-updated)
				x.Require(errors.Is(<-done, s.ErrPaperFenceStale), "устаревшее состояние intent разрешило проверку")
				g.verify(false)
				return
			case "cancel":
				held := x.HoldPaperRecoveryRow(ctx, "head", x.r.Key.RunID)
				call, cancel := context.WithCancel(ctx)
				exit := make(chan error, 1)
				go func() { exit <- g.call(call, stamp, check) }()
				x.Wait(ctx, held.QueryRowContext, "momentum.rebalance_runs", "RowShareLock", true, -1)
				cancel()
				x.Require(errors.Is(<-exit, context.Canceled), "потеряна отмена")
				g.verify(false)
				return
			}
			before := x.ReadHistoryAudit(x.Pool())
			err := g.call(ctx, stamp, check)
			x.Require(errors.Is(err, want) && x.ReadHistoryAudit(x.Pool()).Financial == before.Financial, "допуск после отказа", kind, err)
			g.verify(false)
		})
	}
}

// TestPaperOwnerStartup подтверждает отказ реального fill до сверки и успешный fill после неё.
func TestPaperOwnerStartup(t *testing.T) {
	checkRecoveryFences(t, beginReconcile)
	x := newFillFixture(t, false)
	old := x.checkpoint(true, nil, nil)
	gate := beginReconcile(x)
	wrong := gate.proof.recoveryStamp()
	wrong.Scope.Environment = "testnet"
	x.Require(errors.Is(s.ReconcilePaperOwner(t.Context(), x.Pool(), x.owner, wrong, gate.proof.CheckData), s.ErrPaperFenceArgument) && x.owner.NeedsReconciliation(), "проверен небумажный scope")
	x.Require(errors.Is(ReconcilePaperOwner(t.Context(), x.Pool(), nil, gate.proof), s.ErrPaperFenceArgument) && x.owner.NeedsReconciliation(), "отсутствующий handle")
	x.reject(x.request(x.r), CheckPaperFill(x.p), s.ErrOwnerReconciliationRequired)
	x.Require(errors.Is(ReconcilePaperOwner(t.Context(), x.Pool(), x.owner, old), s.ErrPaperFenceDenied), "принят прежний владелец")
	control := x.facts().Control
	requireCycleSuccess(t, ReconcilePaperOwner(t.Context(), x.Pool(), x.owner, gate.proof))
	gate.verify(true)
	x.Require(errors.Is(ReconcilePaperOwner(t.Context(), x.Pool(), x.owner, gate.proof), s.ErrPaperOwnerAlreadyReconciled), "повтор снятия latch")
	x.Require(x.facts().Control == control, "сверка записала control")
	x.Done(s.RecordPaperFill(t.Context(), x.Pool(), x.request(x.r), CheckPaperFill(x.p)))
	x.observe(x.r, "FILLED", x.r.Fill.Quantity, nil)
}
