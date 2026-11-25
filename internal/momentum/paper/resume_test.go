package paper

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

type resumeCase struct {
	*fillFixture
	inCommand s.PaperCommandRequest
	p         RecoveryProof
}

// resumeCase создаёт точный запрос из действительного recovery.
func (x *fillFixture) resumeCase() *resumeCase {
	p := x.freshProof()
	in := s.PaperCommandRequest{Scope: p.Scope(), ConfigHash: p.ConfigHash(), ExpectedRevision: p.ControlRevision(), Action: "RESUME", Kind: "resume", Actor: "OPERATOR", ActorID: "operator/test", Reason: "recovered", CommandID: "resume", RecoveryID: p.ID(), Confirmed: true}
	return &resumeCase{x, in, p}
}

// resumeClose создаёт следующий настоящий close-all по фактически купленной позиции.
func resumeClose(t *testing.T) *resumeCase {
	x := newFillFixture(t, false)
	prev := haltedTerminal(t, x, cycleAmount(t, "1"))
	x.in = nextCycle(t, x.in, prev, true)
	x.stage(false)
	return x.resumeCase()
}

// apply передаёт настоящий stamp и тестовый наблюдатель границы.
func (z *resumeCase) apply(ctx context.Context, check func(s.PaperRunFacts) error) (s.PaperResumeResult, error) {
	return s.ResumePaperCommand(ctx, z.Pool(), z.inCommand, z.owner, z.p.recoveryStamp(), check)
}

// paperCallError передаёт ошибку записи в канал для отдельной проверки сохранённого результата.
func paperCallError(_ any, err error) error { return err }

// TestPaperResumeAudit проверяет отказ, новый commit, exact и поздний исторический replay.
func TestPaperResumeAudit(t *testing.T) {
	x := newFillFixture(t, false)
	ctx := t.Context()
	requireCycleSuccess(t, x.transition(ctx, 1, "pause", "pause"))
	requireCycleSuccess(t, x.owner.Release(ctx, time.Now()))
	missing := x.checkpoint(true, nil, nil)
	x.owner = x.takeOwner("transient")
	takeover := beginReconcile(x)
	z := x.resumeCase()
	got, err := z.apply(ctx, takeover.proof.CheckData)
	x.Require(got == (s.PaperResumeResult{}) && errors.Is(err, s.ErrOwnerReconciliationRequired), "RESUME до сверки")
	requireCycleSuccess(t, ReconcilePaperOwner(ctx, x.Pool(), x.owner, takeover.proof))
	for _, other := range []*s.ExecutionOwner{nil, newFillFixture(t, false).owner} {
		got, err := ResumePaperCommand(ctx, x.Pool(), z.inCommand, other, z.p)
		x.Require(errors.Is(err, s.ErrPaperFenceDenied) && got == (s.PaperResumeResult{}), "отсутствующий или чужой handle", err)
	}
	old := z.inCommand
	old.RecoveryID = missing.ID()
	got, err = ResumePaperCommand(ctx, x.Pool(), old, x.owner, missing)
	x.Require(got == (s.PaperResumeResult{}) && errors.Is(err, s.ErrPaperFenceDenied), "принят proof без owner")
	before := x.facts()
	rejected := *z
	rejected.inCommand.Confirmed = false
	denied, e := rejected.apply(ctx, z.p.CheckData)
	x.Require(errors.Is(e, s.ErrPaperFenceArgument) && denied == (s.PaperResumeResult{}) && x.facts().Control == before.Control, "недопустимая команда")
	result := make(chan s.PaperResumeResult, 1)
	go func() {
		v, err := ResumePaperCommand(ctx, x.Pool(), z.inCommand, x.owner, z.p)
		if err != nil {
			t.Error(err)
		}
		result <- v
	}()
	second, err := ResumePaperCommand(ctx, x.Pool(), z.inCommand, x.owner, z.p)
	requireCycleSuccess(t, err)
	first := <-result
	x.Require(first.Applied == second.Applied && first.AppliedConfirmed && second.AppliedConfirmed && first.PathKnown && second.PathKnown && first.AlreadyApplied != second.AlreadyApplied && first.Current.State.Mode == "ACTIVE" && first.Applied.State.Revision == 3, first, second)
	requireCycleSuccess(t, x.transition(ctx, 3, "risk", "later"))
	requireCycleSuccess(t, x.owner.Release(ctx, time.Now()))
	calls := 0
	got, err = s.ResumePaperCommand(ctx, x.Pool(), z.inCommand, x.owner, s.PaperRecoveryStamp{}, func(s.PaperRunFacts) error {
		calls++
		return errors.New("replay")
	})
	x.Require(err == nil && calls == 0 && got.AlreadyApplied && got.PathKnown && got.Applied == first.Applied && got.Current.State.Mode == "HALTED" && x.facts().DataHash == before.DataHash, "история после risk HALT", err)
	z.inCommand.Reason += "changed"
	got, err = z.apply(ctx, z.p.CheckData)
	x.Require(got == (s.PaperResumeResult{}) && errors.Is(err, s.ErrPaperFenceConflict), "изменённый replay", err)
	requireCycleSuccess(t, x.Pool().Close())
	got, err = z.apply(ctx, z.p.CheckData)
	x.Require(errors.Is(err, s.ErrPaperFenceUnknown) && got == (s.PaperResumeResult{}), "недоступный lookup", err)
}

// TestResumeVictim создаёт реальный цикл ожиданий с двумя независимыми локальными holders.
func TestResumeVictim(t *testing.T) {
	z := resumeClose(t)
	z.Pool().SetMaxOpenConns(8)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	e := z.slice(0, model.Zero(), cycleAmount(t, "0.25"))
	start := z.Deadlocks(ctx, 0)
	intent := z.HoldPaperRecoveryRow(ctx, "intent", e.OrderLinkID)
	head := z.HoldPaperRecoveryRow(ctx, "head", z.r.Key.RunID)
	filled, resumed := make(chan error, 1), make(chan error, 1)
	go func() {
		filled <- paperCallError(s.RecordPaperFill(ctx, z.Pool(), z.request(e), CheckPaperFill(z.fillFixture.p)))
	}()
	fillPID := z.Wait(ctx, intent.QueryRowContext, "momentum.order_intents", "RowShareLock", true, -1)
	go func() { resumed <- paperCallError(z.apply(ctx, z.p.CheckData)) }()
	resumePID := z.Wait(ctx, head.QueryRowContext, "momentum.rebalance_runs", "RowShareLock", true, -1)
	requireCycleSuccess(t, intent.Commit())
	z.Wait(ctx, z.Pool().QueryRowContext, "momentum.control_revisions", "ShareLock", false, resumePID)
	requireCycleSuccess(t, head.Commit())
	z.Wait(ctx, z.Pool().QueryRowContext, "momentum.order_intents", "ShareLock", false, fillPID)
	fillErr, resumeErr := <-filled, <-resumed
	z.Deadlocks(ctx, start+1)
	f := z.facts()
	audit := z.ReadHistoryAudit(z.Pool())
	row, found, readErr := s.ReadPaperCommandOutcome(ctx, z.Pool(), z.inCommand.Scope, z.inCommand.CommandID)
	z.Require(readErr == nil && found == (resumeErr == nil) && (!found || row.Matches(z.inCommand)) && audit.Fees == audit.Fills && audit.Fills == audit.Postings && audit.Fills == 1+len(f.Fills()), "исход участников deadlock", readErr)

	if fillErr == nil {
		z.Require(errors.Is(resumeErr, s.ErrPaperFenceRetry) && f.Control.State == (s.ControlState{Revision: 2, Mode: "HALTED"}) && len(f.Fills()) == 1, "нет rollback RESUME", resumeErr)
		z.observe(e, "PARTIALLY_FILLED", e.Fill.Quantity, nil)
		z = z.resumeCase()
		got, err := z.apply(ctx, z.p.CheckData)
		z.Require(err == nil && got.AppliedConfirmed && got.PathKnown && !got.AlreadyApplied, "новый commit после rollback", err)
		row.Applied = got.Applied
	} else {
		z.Require(resumeErr == nil && f.Control.State == (s.ControlState{Revision: 3, Mode: "ACTIVE"}) && len(f.Fills()) == 0, "нет rollback fill", fillErr)
		z.reject(z.request(e), CheckPaperFill(z.fillFixture.p), s.ErrPaperWriteDenied)
	}
	again, err := z.apply(ctx, nil)
	z.Require(err == nil && again.AppliedConfirmed && again.AlreadyApplied && again.PathKnown && again.Applied == row.Applied && again.Current == row.Applied, "точный repeat победившей команды", err)
}

// beginResume проверяет те же факты через настоящий provisional RESUME и его durable receipt.
func beginResume(x *fillFixture) checkedGate {
	z := x.resumeCase()
	return checkedGate{z.p, func(ctx context.Context, stamp s.PaperRecoveryStamp, check func(s.PaperRunFacts) error) error {
		_, err := s.ResumePaperCommand(ctx, x.Pool(), z.inCommand, x.owner, stamp, check)
		return err
	}, func(ok bool) {
		row, found, err := s.ReadPaperCommandOutcome(x.Context(), x.Pool(), z.inCommand.Scope, z.inCommand.CommandID)
		x.Require(err == nil && found == ok && (!ok || row.Matches(z.inCommand) && row.Current == row.Applied), "состояние команды", err)
	}}
}

// TestResumeWriterMatrix проверяет допускаемые порядки типизированных публичных writers.
func TestResumeWriterMatrix(t *testing.T) {
	t.Run("fences", func(t *testing.T) { checkRecoveryFences(t, beginResume) })
	for _, scenario := range []string{"fill-first", "fill-late", "append-first", "append-late"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			var z *resumeCase
			var write func() error
			table, mode := "momentum.fills", "RowExclusiveLock"
			switch scenario {
			case "fill-first", "fill-late":
				z = resumeClose(t)
				e := z.slice(0, model.Zero(), cycleAmount(t, "0.25"))
				write = func() error {
					return paperCallError(s.RecordPaperFill(ctx, z.Pool(), z.request(e), CheckPaperFill(z.fillFixture.p)))
				}
			case "append-first", "append-late":
				x := emptyRecordedFixture(t)
				x.p = preparedFixture(t, x.in)
				x.r.Key.RunID = x.in.Run.RunID
				x.Done(s.CreateOrReadRebalanceRun(ctx, x.Pool(), s.RebalanceRunInput(x.in.Run)))
				requireCycleSuccess(t, x.transition(ctx, 1, "pause", "pause"))
				z = x.resumeCase()
				write = func() error { return paperCallError(s.StorePaperInput(ctx, x.Pool(), x.p.input)) }
				table, mode = "momentum.control_revisions", "ShareLock"
			}
			x := z.fillFixture
			reported := make(chan error, 1)
			start := func() { go func() { reported <- write() }() }
			if strings.HasSuffix(scenario, "-first") {
				held := x.HoldPaperRecoveryRow(ctx, "head", x.r.Key.RunID)
				start()
				pid := x.Wait(ctx, held.QueryRowContext, "momentum.control_revisions", "ShareLock", true, -1)
				resumed := make(chan error, 1)
				go func() { resumed <- paperCallError(z.apply(ctx, z.p.CheckData)) }()
				x.Wait(ctx, x.Pool().QueryRowContext, "momentum.control_revisions", "ShareRowExclusiveLock", false, pid)
				requireCycleSuccess(t, held.Commit())
				requireCycleSuccess(t, <-reported)
				x.Require(errors.Is(<-resumed, s.ErrPaperFenceStale), "прежний proof принят")
				f := x.facts()
				x.Require(f.Control.State.Mode != "ACTIVE" && (scenario == "append-first" && f.Head == x.p.Binding() || scenario == "fill-first" && len(f.Fills()) == 1), "потерян первый commit")
				fresh := x.resumeCase()
				fresh.Done(fresh.apply(ctx, fresh.p.CheckData))
				return
			}
			release := start
			if scenario == "fill-late" {
				held := x.HoldPaperRecoveryRow(ctx, "intent", x.r.OrderLinkID)
				start()
				x.Wait(ctx, held.QueryRowContext, "momentum.order_intents", "RowShareLock", true, -1)
				release = func() { requireCycleSuccess(t, held.Commit()) }
			}
			z.Done(z.apply(ctx, func(f s.PaperRunFacts) error {
				if err := z.p.CheckData(f); err != nil {
					return err
				}
				release()
				x.Wait(ctx, x.Pool().QueryRowContext, table, mode, false, 0)
				return nil
			}))
			switch scenario {
			case "fill-late":
				x.Require(errors.Is(<-reported, s.ErrPaperWriteDenied) && len(x.facts().Fills()) == 0, "поздний fill")
			case "append-late":
				requireCycleSuccess(t, <-reported)
				x.Require(x.facts().Head == x.p.Binding(), "потерян новый head")
			}
			state, err := s.ReadPaperCommandState(ctx, x.Pool(), z.inCommand.Scope)
			x.Require(err == nil && state.State.Mode == "ACTIVE", "утрачен первый commit", err)
		})
	}
}
