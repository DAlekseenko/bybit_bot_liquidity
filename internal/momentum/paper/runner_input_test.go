package paper

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	s "bybit_bot_liquidity/internal/momentum/storage"
	"bybit_bot_liquidity/internal/momentum/storage/pgtest"
)

// runnerCloseCandidate готовит действительный вход из частичного BUY и сохранённого ABORTED.
func runnerCloseCandidate(t *testing.T) (*fillFixture, *resumeCase) {
	t.Helper()
	x := newFillFixture(t, false)
	previous := haltedTerminal(t, x, cycleAmount(t, "1"))
	x.in = nextCycle(t, x.in, previous, true)
	x.p = preparedFixture(t, x.in)
	x.Done(s.CreateOrReadRebalanceRun(t.Context(), x.Pool(), s.RebalanceRunInput(x.in.Run)))
	return x, x.resumeCase()
}

// TestRunnerCloseAppendOrders реально сериализует новый close input и подтверждённый RESUME в обоих порядках.
func TestRunnerCloseAppendOrders(t *testing.T) {
	for _, order := range []string{"append-first", "resume-first"} {
		t.Run(order, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			x, z := runnerCloseCandidate(t)
			observer := pgtest.Existing(t, x.Name(), s.OpenMomentumPostgres)
			original := x.p.input
			before := x.ReadHistoryAudit(observer)
			saved := make(chan error, 1)
			appendInput := func() {
				go func() {
					saved <- paperCallError(s.StoreRunnerPaperInput(ctx, x.Pool(), original))
				}()
			}
			if order == "append-first" {
				held := x.HoldPaperRecoveryRow(ctx, "head", x.in.Run.RunID)
				appendInput()
				pid := x.Wait(ctx, held.QueryRowContext, "momentum.control_revisions", "ShareLock", true, -1)
				resumed := make(chan error, 1)
				go func() { resumed <- paperCallError(z.apply(ctx, z.p.CheckData)) }()
				x.Wait(ctx, observer.QueryRowContext, "momentum.control_revisions", "ShareRowExclusiveLock", false, pid)
				x.Done(nil, held.Commit())
				x.Done(nil, <-saved)
				x.Require(errors.Is(<-resumed, s.ErrPaperFenceStale), "устаревший RESUME принят")
				c, err := s.ReadPaperCommandState(ctx, observer, z.inCommand.Scope)
				x.Require(err == nil && c.CommandID == "close" && c.State.Mode == "HALTED", "потерян HALT", err)
				head, found, err := s.ReadPaperInputHead(ctx, observer, original.Header().Scope)
				x.Require(err == nil && found && head.Input.Hash() == original.Hash(), "утрачен выигравший вход", err)
				x.Done(nil, x.transition(ctx, 2, "close-all", "later"))
				again, err := s.StoreRunnerPaperInput(ctx, x.Pool(), original)
				x.Require(err == nil && head.CreatedAt.Equal(again.CreatedAt) && bytes.Equal(again.Input.Payload(), original.Payload()), "не подтверждён точный повтор старой команды", err)
			} else {
				got, err := z.apply(ctx, func(f s.PaperRunFacts) error {
					if err := z.p.CheckData(f); err != nil {
						return err
					}
					appendInput()
					x.Wait(ctx, observer.QueryRowContext, "momentum.control_revisions", "ShareLock", false, 0)
					return nil
				})
				x.Require(err == nil && got.AppliedConfirmed && got.Applied.State.Mode == "ACTIVE", "RESUME не подтверждён", err)
				current, err := s.ReadPaperCommandState(ctx, observer, z.inCommand.Scope)
				x.Require(err == nil && current == got.Applied, "независимое чтение не подтвердило RESUME", err)
				x.Require(errors.Is(<-saved, s.ErrPaperInputConflict), "после RESUME дописан закрывающий вход")
				_, found, err := s.ReadPaperInput(ctx, observer, original.Header().Scope, original.Header().Sequence)
				x.Require(err == nil && !found && before == x.ReadHistoryAudit(observer), "отклонённый append изменил журнал", err)
				x.Done(s.StorePaperInput(ctx, x.Pool(), original))
				again, err := s.StoreRunnerPaperInput(ctx, x.Pool(), original)
				x.Require(err == nil && bytes.Equal(again.Input.Payload(), original.Payload()), "старый repository и точный readback должны сохраниться", err)
			}
			after := x.ReadHistoryAudit(observer)
			x.Require(after.Fills == before.Fills && after.Postings == before.Postings && after.Fees == before.Fees && after.Intents == before.Intents && after.Inputs == before.Inputs+1, "дополнительный финансовый эффект")
		})
	}
}

// TestRunnerCloseIdentity проверяет отказ до новой записи при другом ID, ревизии или системном HALT.
func TestRunnerCloseIdentity(t *testing.T) {
	for _, kind := range []string{"id", "revision", "risk"} {
		t.Run(kind, func(t *testing.T) {
			x, _ := runnerCloseCandidate(t)
			switch kind {
			case "id":
				x.in.Purpose.CommandID = "foreign"
			case "revision":
				x.in.Purpose.ControlRevision++
			case "risk":
				x.Done(nil, x.transition(t.Context(), 2, "risk", "system"))
			}
			p := preparedFixture(t, x.in)
			before := x.ReadHistoryAudit(x.Pool())
			got, err := s.StoreRunnerPaperInput(t.Context(), x.Pool(), p.input)
			x.Require(errors.Is(err, s.ErrPaperInputConflict) && got.Input.Hash() == "" && before == x.ReadHistoryAudit(x.Pool()), "неподтверждённый input", err)
		})
	}
}

// TestRunnerCloseCorruptControl проверяет отказ без записи при повреждённом аудите текущего HALT.
func TestRunnerCloseCorruptControl(t *testing.T) {
	x, _ := runnerCloseCandidate(t)
	ctx := t.Context()
	x.DamagePaperRecoveryFact(ctx, "audit", x.in.Run.RunID)
	observer := pgtest.Existing(t, x.Name(), s.OpenMomentumPostgres)
	before := x.ReadHistoryAudit(observer)
	got, err := s.StoreRunnerPaperInput(ctx, x.Pool(), x.p.input)
	x.Require(errors.Is(err, s.ErrPaperInputUnavailable) && got.Input.Hash() == "", "повреждённый HALT допустил запись", err)
	x.Require(before == x.ReadHistoryAudit(observer), "изменена история при повреждённом HALT")
}
