package paper

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"reflect"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	"bybit_bot_liquidity/internal/momentum/planner"
	s "bybit_bot_liquidity/internal/momentum/storage"
	"bybit_bot_liquidity/internal/momentum/storage/pgtest"
)

// checkpoint открывает новый пул и сверяет финансовый префикс с независимой проекцией.
func (x *fillFixture) checkpoint(open bool, fills []model.ConfirmedFill, last *ResolvedCycle) RecoveryProof {
	x.Helper()
	db := pgtest.Existing(x.T, x.Name(), s.OpenMomentumPostgres)
	defer db.Close()
	audit := x.ReadHistoryAudit(db)
	got, err := Recover(x.Context(), RecoveryRequest{db, x.r.Key.Scope, x.in.PlanBuilder, x.in.Admission})
	requireCycleSuccess(x.T, err)
	p, active := got.Current()
	progress, ok := got.Progress()
	r, closed := got.Last()
	x.Require(active == open && ok == open && closed == (last != nil), "фаза")
	if open {
		want, err := ProjectProgress(x.p, fills)
		x.Require(err == nil && p.Hash() == x.p.Hash() && reflect.DeepEqual(progress, want), "actual prefix", err)
	}
	if closed {
		x.Require(r.Hash() == last.Hash() && reflect.DeepEqual(r.ActualAfter(), last.ActualAfter()), "последний receipt")
	}
	f, err := s.ReadPaperRunFacts(x.Context(), db, x.r.Key.Scope, "")
	requireCycleSuccess(x.T, err)
	proof := got.Proof()
	head, found := proof.Head()
	x.Require(head == f.Head && found == f.HeadFound && proof.DataHash() == f.DataHash && proof.ConfigHash() == f.Control.ConfigHash && proof.OwnerSessionID() == f.OwnerSessionID && proof.ControlRevision() == f.Control.State.Revision, "привязка")
	requireCycleSuccess(x.T, proof.CheckData(f))
	x.Require(x.ReadHistoryAudit(db) == audit && audit.Fills == audit.Postings && audit.Fills == audit.Fees, "неизменяемые строки")
	return proof
}

// finish сверяет строку штатной финализации с независимым финансовым итогом.
func (a finalAttempt) finish(fills []model.ConfirmedFill) ResolvedCycle {
	result, err := a.run(a.Context(), a.build)
	requireCycleSuccess(a.T, err)
	return a.proof(result, fills)
}

// TestRecoveryHistory восстанавливает каждую фазу и повреждает только непоследнего предка.
func TestRecoveryHistory(t *testing.T) {
	for _, fee := range []bool{true, false} {
		t.Run(fmt.Sprint("fee=", fee), func(t *testing.T) {
			x := emptyRecordedFixture(t)
			ctx, db := t.Context(), x.Pool()
			x.p = preparedFixture(t, x.in)
			x.r.Key.RunID, x.r.Key.InputHash = x.in.Run.RunID, x.p.Hash()
			original, err := s.CreateOrReadRebalanceRun(ctx, db, s.RebalanceRunInput(x.in.Run))
			requireCycleSuccess(t, err)
			empty := x.checkpoint(false, nil, nil)
			repeat, err := s.CreateOrReadRebalanceRun(ctx, db, original.RebalanceRunInput)
			x.Require(err == nil && original.ScheduledAt.Equal(repeat.ScheduledAt) && repeat.ConfigHash == empty.ConfigHash(), "run-only cutoff/config", err)
			requireCycleSuccess(t, ValidatePaperModel(original.Config, x.in.Profile))
			changed := x.in.Profile
			changed.Poll += time.Second
			x.Require(ValidatePaperModel(original.Config, changed) != nil && ValidatePaperModel(original.Config, paperwire.Profile{}) != nil, "требуется исходный профиль")
			x.Done(s.StorePaperInput(ctx, db, x.p.input))
			x.checkpoint(true, nil, nil)
			x.Done(s.StorePlanRevision(ctx, db, x.p.Plan()))
			x.checkpoint(true, nil, nil)
			for _, i := range x.named(x.r.Key.RunID, nil).OrderIntents() {
				requireCycleSuccess(t, s.RecordOrderIntent(ctx, db, i))
			}
			x.checkpoint(true, nil, nil)
			first := x.slice(0, model.Zero(), cycleAmount(t, "1"))
			x.Done(s.RecordPaperFill(ctx, db, x.request(first), CheckPaperFill(x.p)))
			fills := []model.ConfirmedFill{first.Fill}
			x.checkpoint(true, fills, nil)
			requireCycleSuccess(t, x.transition(ctx, 1, "close-all", "close"))
			prev := x.attempt("ABORTED", "close", 2).finish(fills)
			x.checkpoint(false, nil, &prev)
			requireCycleSuccess(t, s.SetOrderIntentState(ctx, db, "paper", x.in.Run.AccountID, first.OrderLinkID, "CANCELLED"))
			x.checkpoint(false, nil, &prev)
			x.in = nextCycle(t, x.in, prev, true)
			x.stage(false)
			before := x.checkpoint(true, nil, &prev)
			x.Require(x.r.Fill.Side == model.FillSell && decimalEqual(x.r.Fill.Quantity, first.Fill.Quantity), "zero-target из ActualAfter")
			x.Done(s.RecordPaperFill(ctx, db, x.request(x.r), CheckPaperFill(x.p)))
			f := x.facts()
			x.Require(f.Control.State.Mode == "HALTED" && f.Control.State.Revision == before.ControlRevision() && errors.Is(before.CheckData(f), ErrPaperRecoveryStale), "устаревший prefix")
			fills = []model.ConfirmedFill{x.r.Fill}
			x.checkpoint(true, fills, &prev)
			final := x.attempt("COMPLETED", "", 0).finish(fills)
			done := x.checkpoint(false, nil, &final)
			x.Require(x.checkpoint(false, nil, &final).ID() == done.ID(), "повторный proof")
			x.in.Purpose = paperwire.Purpose{Kind: "rebalance"}
			x.in = nextCycle(t, x.in, final, false)
			x.p = preparedFixture(t, x.in)
			x.Done(s.CreateOrReadRebalanceRun(ctx, db, s.RebalanceRunInput(x.in.Run)))
			x.Done(s.StorePaperInput(ctx, db, x.p.input))
			x.DamageAncestor(fee)
			x.named(x.in.Run.RunID, &final)
			x.rejectRecovery(ctx, RecoveryRequest{db, x.r.Key.Scope, x.in.PlanBuilder, x.in.Admission}, ErrPaperRecoveryCorrupt)
		})
	}
}

// rejectRecovery проверяет причину отказа и нулевой публичный результат.
func (x *fillFixture) rejectRecovery(ctx context.Context, req RecoveryRequest, want error) error {
	x.Helper()
	out, err := Recover(ctx, req)
	x.Require(errors.Is(err, want) && reflect.DeepEqual(out, RecoveredPaper{}), "результат при ошибке", want, err)
	return err
}

// TestRecoveryChanged фиксирует реальное изменение после первоначального снимка.
func TestRecoveryChanged(t *testing.T) {
	for _, kind := range []string{"head", "fill", "intent", "owner"} {
		t.Run(kind, func(t *testing.T) {
			x := newFillFixture(t, false)
			ctx := t.Context()
			var prev ResolvedCycle
			if kind == "head" {
				requireCycleSuccess(t, x.transition(ctx, 1, "close-all", "close"))
				prev = x.attempt("ABORTED", "close", 2).finish(nil)
			}
			req := RecoveryRequest{x.Pool(), x.r.Key.Scope, x.in.PlanBuilder, x.in.Admission}
			req.PlanBuilder = func(id string, m model.PlanMetadata, snap model.PortfolioSnapshot, d map[string][]data.DailyClose, rules map[string]planner.MarketDeltaRules, e model.Decimal) (model.TradePlan, error) {
				switch kind {
				case "head":
					next := nextCycle(t, x.in, prev, false)
					x.Done(s.CreateOrReadRebalanceRun(ctx, x.Pool(), s.RebalanceRunInput(next.Run)))
					x.Done(s.StorePaperInput(ctx, x.Pool(), preparedFixture(t, next).input))
				case "fill":
					x.Done(s.RecordPaperFill(ctx, x.Pool(), x.request(x.r), CheckPaperFill(x.p)))
				case "intent":
					requireCycleSuccess(t, s.SetOrderIntentState(ctx, x.Pool(), "paper", x.in.Run.AccountID, x.r.OrderLinkID, "UNKNOWN"))
				case "owner":
					requireCycleSuccess(t, x.owner.Release(ctx, time.Now()))
				}
				return x.in.PlanBuilder(id, m, snap, d, rules, e)
			}
			x.rejectRecovery(ctx, req, ErrPaperRecoveryStale)
		})
	}
}

// TestRecoveryErrors различает ошибки аргументов, отсутствие области и недоступность хранилища.
func TestRecoveryErrors(t *testing.T) {
	x := newFillFixture(t, false)
	req := RecoveryRequest{x.Pool(), x.r.Key.Scope, x.in.PlanBuilder, x.in.Admission}
	missing := req
	missing.Scope.AccountID += "absent"
	x.rejectRecovery(t.Context(), missing, s.ErrPaperFactsAbsent)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	x.Require(errors.Is(x.rejectRecovery(ctx, req, ErrPaperRecoveryUnavailable), context.Canceled), "причина отмены")
	req.DB = pgtest.Existing(t, x.Name(), s.OpenMomentumPostgres)
	requireCycleSuccess(t, req.DB.Close())
	x.rejectRecovery(t.Context(), req, ErrPaperRecoveryUnavailable)
	x.rejectRecovery(t.Context(), RecoveryRequest{DB: req.DB, Scope: req.Scope, PlanBuilder: req.PlanBuilder}, ErrPaperRecoveryArgument)
}

// TestRecoveryProof фиксирует формат ID и по явным именам сравнивает фактический dump/restore.
func TestRecoveryProof(t *testing.T) {
	check := pgtest.Journal{T: t}
	f := s.PaperRunFacts{Head: paperwire.CycleBinding{Sequence: 4, InputHash: "i"}, Control: s.PaperCommandState{ConfigHash: "cfg", State: s.ControlState{Revision: 7}}, OwnerSessionID: "o", DataHash: "d"}
	f.Scope = s.ControlScope{Environment: "paper", AccountID: "account", StrategyID: "strategy"}
	check.Require((RecoveryProof{facts: f}).digest("r") == "90c28c21117025305de164be3549b89775a9525dda437819af9a1a0355c1ae76" && errors.Is((RecoveryProof{}).CheckData(f), ErrPaperRecoveryArgument), "golden/zero")
	source, target := os.Getenv("PAPER_A16_SOURCE"), os.Getenv("PAPER_A16_TARGET")
	in := cycleFixture(t)
	if source == "" && target == "" {
		input := preparedFixture(t, in).input
		facts := s.PaperRunFacts{Input: input, InputFound: true, Head: input.Header(), HeadFound: true}
		proof := RecoveryProof{facts: facts, id: (RecoveryProof{facts: facts}).digest(""), builder: in.PlanBuilder, admission: in.Admission}
		facts.Control.State.Revision++
		requireCycleSuccess(t, proof.CheckData(facts))
		return
	}
	scope := s.ControlScope{Environment: "paper", AccountID: in.Run.AccountID, StrategyID: in.Run.StrategyID}
	thirds := os.Getenv("PAPER_A16_CASE") == "thirds"
	if thirds {
		start := in.Run.ScheduledAt
		targets := map[time.Time]string{start: "1", start.Add(24 * time.Hour): "3", start.Add(48 * time.Hour): "1"}
		in.PlanBuilder = func(id string, m model.PlanMetadata, snap model.PortfolioSnapshot, d map[string][]data.DailyClose, rules map[string]planner.MarketDeltaRules, e model.Decimal) (model.TradePlan, error) {
			return singleTarget(cycleAmount(t, targets[m.ScheduledAt]))(id, m, snap, d, rules, e)
		}
	}
	ctx := t.Context()
	acquire := func(db *sql.DB, session string) *s.ExecutionOwner {
		h, err := s.AcquireExecutionOwner(ctx, db, s.OwnerIdentity{SessionID: session, Environment: "paper", AccountID: scope.AccountID, StrategyID: scope.StrategyID, Host: "a16", ProcessID: 1}, time.Now())
		requireCycleSuccess(t, err)
		t.Cleanup(func() { h.Release(context.Background(), time.Now()) })
		return h
	}
	if target == "" {
		db := pgtest.Existing(t, source, s.OpenMomentumPostgres)
		f, err := s.ReadPaperRunFacts(ctx, db, scope, "")
		requireCycleSuccess(t, err)
		if f.Control.State.Mode != "HALTED" {
			check.Done(s.ApplyPaperCommandState(ctx, db, s.PaperCommandRequest{Scope: scope, ExpectedRevision: f.Control.State.Revision, ConfigHash: f.Control.ConfigHash, Action: "HALT", Actor: "OPERATOR", ActorID: "operator/test", Kind: "halt", CommandID: "a16", Confirmed: true, Reason: "backup"}))
		}
		requireCycleSuccess(t, acquire(db, "backup").Permit(ctx))
		fmt.Println("A16 READY", source)
		_, err = io.ReadFull(os.Stdin, make([]byte, 1))
		requireCycleSuccess(t, err)
		return
	}
	check.Require(source != target, "разные базы обязательны")
	old, audit, receipt := RecoveryProof{}, pgtest.HistoryAudit{}, ""
	for i, name := range []string{source, target, target} {
		db := pgtest.Existing(t, name, s.OpenMomentumPostgres)
		var owner *s.ExecutionOwner
		if i == 2 {
			owner = acquire(db, "restored")
			check.Require(owner.NeedsReconciliation() && errors.Is(owner.Permit(ctx), s.ErrOwnerReconciliationRequired), "latch takeover")
		}
		r, err := Recover(ctx, RecoveryRequest{db, scope, in.PlanBuilder, in.Admission})
		requireCycleSuccess(t, err)
		last, ok := r.Last()
		p := r.Proof()
		check.Require(ok && p.facts.Control.State.Mode == "HALTED", "финансовый terminal/HALT")
		rows := check.ReadHistoryAudit(db)
		n, d, _ := last.ActualAfter().Positions["BTCUSDT"].CostBasisUSDT.CanonicalRatio()
		w, _, _ := last.ActualAfter().WalletUSDT.CanonicalRatio()
		wallet, _ := new(big.Int).SetString(w, 10)
		check.Require(thirds && n == "1" && d == "3" && rows.Inputs == 3 && rows.Fills == 3 || !thirds && wallet.BitLen() == 4096 && len(last.Payload()) > 10*1024 && rows.Inputs == 1 && rows.Fills == 1, "точная ratio fixture")
		check.Require(rows.Postings == rows.Fills && rows.Fees == rows.Fills && rows.Runs == rows.Inputs, "проводки")
		t.Log(name, p.ID(), last.Hash(), rows)
		switch i {
		case 0:
			old, audit, receipt = p, rows, last.Hash()
		case 1:
			check.Require(rows == audit && p.ID() == old.ID() && p.OwnerSessionID() == "backup" && last.Hash() == receipt, "восстановленная копия")
		case 2:
			check.Require(rows.Financial == audit.Financial && rows.Sessions == audit.Sessions+1 && rows.Owners != audit.Owners && rows.Active == "restored" && rows.TakenOver == "backup", "изменения owner audit")
			check.Require(last.Hash() == receipt && p.ID() != old.ID() && p.ControlRevision() == old.ControlRevision() && p.OwnerSessionID() == rows.Active && p.DataHash() == old.DataHash(), "состояние после takeover")
			requireCycleSuccess(t, owner.MarkReconciled(p.ID()))
			requireCycleSuccess(t, owner.Permit(ctx))
		}
	}
}
