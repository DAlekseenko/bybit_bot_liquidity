package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paper"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	"bybit_bot_liquidity/internal/momentum/planner"
	s "bybit_bot_liquidity/internal/momentum/storage"
	"bybit_bot_liquidity/internal/momentum/storage/pgtest"
)

// invokeOperator использует штатный CLI dispatch и читает его действительный JSON.
func invokeOperator(j *pgtest.Journal, config string, kind string, args ...string) (paper.OperatorResult, error) {
	j.Helper()
	command := []string{"paper", kind, "-config", config, "-account", "paper/cli"}
	var b bytes.Buffer
	err := run(append(command, args...), &b, nil)
	var out paper.OperatorResult
	if b.Len() > 0 {
		j.Done(nil, json.Unmarshal(b.Bytes(), &out))
	}
	return out, err
}

// TestOperatorCLI связывает каждую durable команду, operator audit и отдельное текущее состояние.
func TestOperatorCLI(t *testing.T) {
	j := useCLIJournal(t)
	m := &cliMarket{T: t, now: time.Now().UTC().Truncate(time.Minute).Add(-20 * time.Minute), rising: "BTCUSDT"}
	m.funding = m.now
	args := cliPaperArgs(t, "now", m.now)
	file := args[2]
	scope := s.ControlScope{Environment: "paper", AccountID: "paper/cli", StrategyID: "momentum-v1"}
	empty, err := invokeOperator(j, file, "status")
	j.Require(err != nil && empty.State == "", "status не создаёт схему")
	_, err = s.ReadPaperCommandState(t.Context(), j.Pool(), scope)
	j.Require(err != nil && !errors.Is(err, s.ErrControlAbsent), "status создал таблицы")
	j.Done(nil, s.MigrateMomentumPostgres(t.Context(), j.Pool()))
	before := j.ReadHistoryAudit(j.Pool())
	empty, err = invokeOperator(j, file, "status")
	j.Require(err != nil && empty.State == "" && before == j.ReadHistoryAudit(j.Pool()), "status до INIT")
	unconfirmed, err := invokeOperator(j, file, "close-all", "-revision", "1", "-id", "bad", "-actor", "test", "-reason", "bad")
	j.Require(err != nil && unconfirmed.State == "" && before == j.ReadHistoryAudit(j.Pool()), "неподтверждённая команда изменила пустую БД")
	_, err = s.ReadPaperCommandState(t.Context(), j.Pool(), scope)
	j.Require(errors.Is(err, s.ErrControlAbsent), "скрытый INITIALIZE")
	j.Require(errors.Is(runPaperCLI(j, m, args, nil), context.Canceled), "инициализация")
	live := j.ReadHistoryAudit(j.Pool())
	status, err := invokeOperator(j, file, "status")
	j.Require(err == nil && status.ReceiptHash != "" && status.Head.InputHash != "" && len(status.Actual) > 0 && live == j.ReadHistoryAudit(j.Pool()), "read-only status", err)
	pause := []string{"-id", "pause", "-actor", "operator/test", "-reason", "hold", "-revision", "1", "-confirm"}
	applied, err := invokeOperator(j, file, "pause", pause...)
	j.Require(err == nil && applied.History.Applied.ActorID == "operator/test" && applied.Control.State.Mode == "PAUSED_ENTRIES", "аудит паузы", err)
	svc := paper.OperatorService{RecoveryRequest: paper.RecoveryRequest{DB: j.Pool(), Scope: scope, PlanBuilder: planner.BuildBaselinePaperPlan, Admission: planner.AdmitBaselineExecution}, ConfigHash: status.Control.ConfigHash}
	cmd := paper.OperatorCommand{Kind: "pause", ID: "pause", ActorID: "operator/test", Reason: "hold", ExpectedRevision: 1, Confirmed: true}
	replay, err := svc.Apply(t.Context(), cmd)
	j.Require(err == nil && replay.History == applied.History, "единый service")
	cmd.Reason = "changed"
	_, err = svc.Apply(t.Context(), cmd)
	j.Require(errors.Is(err, s.ErrControlConflict), "изменённый повтор", err)
	resume := []string{"-id", "resume", "-actor", "operator/test", "-reason", "verified", "-revision", "2", "-confirm"}
	id := s.OwnerIdentity{Environment: "paper", AccountID: scope.AccountID, StrategyID: scope.StrategyID, SessionID: "held", Host: "test", ProcessID: 3}
	owner, err := s.AcquireExecutionOwner(t.Context(), j.Pool(), id, time.Now())
	j.Require(err == nil && owner != nil, err)
	unavailable, err := invokeOperator(j, file, "resume", resume...)
	j.Require(errors.Is(err, s.ErrOwnerBusy) && unavailable.State == "", "реальный owner занят")
	defer owner.Release(t.Context(), time.Now())
	j.KillOwner(s.ExecutionOwnerLockKey(id))
	active, err := invokeOperator(j, file, "resume", resume...)
	j.Require(err == nil && active.Control.State.Mode == "ACTIVE" && active.History.Applied.RecoveryID != "" && j.ReadHistoryAudit(j.Pool()).TakenOver == id.SessionID && live.Financial == j.ReadHistoryAudit(j.Pool()).Financial, "подтверждённый RESUME без финансового шага", err)
	closeArgs := []string{"-id", "close", "-actor", "operator/test", "-reason", "flatten", "-revision", "3", "-confirm"}
	halt, err := invokeOperator(j, file, "close-all", closeArgs...)
	j.Require(err == nil && halt.History.Applied.State.Mode == "HALTED" && halt.State == "APPLIED", "зафиксирован запрос, не CLOSED", err)
	pending, err := invokeOperator(j, file, "status")
	j.Require(err == nil && pending.Close.State == "PENDING", "разные фазы запроса", err)
	first, found, err := s.ReadPaperInput(t.Context(), j.Pool(), paperwire.Scope(scope), 1)
	j.Require(err == nil && found, err)
	prev, _ := cliRecovery(j).Last()
	run := first.Input.Values().Run
	run.RunID, run.OnboardingID, run.ScheduledAt = pending.Close.RunID, pending.Close.RunID, halt.Control.CreatedAt.Add(time.Microsecond)
	j.Done(s.CreateOrReadRebalanceRun(t.Context(), j.Pool(), s.RebalanceRunInput(run)))
	m.now = halt.Control.CreatedAt.Add(time.Second)
	profile := first.Input.Values().Profile
	purpose := paperwire.Purpose{Kind: "close-all", CommandID: halt.Control.CommandID, ControlRevision: halt.Control.State.Revision}
	obs, err := paper.ReadCycleObservation(t.Context(), paper.SourceRequest{Run: run, Purpose: purpose, Profile: profile, FundingAfter: prev.ActualAfter().FundingThrough, Now: func() time.Time { return m.now }, Transport: m})
	j.Require(err == nil, err)
	p, err := paper.PrepareCycle(paper.CycleInput{Run: run, Sequence: 2, Previous: &prev, Purpose: purpose, Profile: profile, Observation: obs, FormulaID: planner.BaselinePaperFormulaID, PlanBuilder: svc.PlanBuilder, Admission: svc.Admission})
	j.Require(err == nil, err)
	open, err := paperwire.DecodeInputRecord(p.Payload())
	j.Require(err == nil, err)
	j.Done(s.StoreRunnerPaperInput(t.Context(), j.Pool(), open))
	resumeAfter := append([]string(nil), resume...)
	resumeAfter[1], resumeAfter[7] = "after-close", "4"
	beforeReject := j.ReadHistoryAudit(j.Pool())
	rejected, err := invokeOperator(j, file, "resume", resumeAfter...)
	j.Require(errors.Is(err, paper.ErrOperatorCloseInProgress) && rejected.State == "", "принят RESUME при незавершённом закрытии", err)
	_, seen, auditErr := s.ReadPaperCommandOutcome(t.Context(), j.Pool(), scope, "after-close")
	audit := j.ReadHistoryAudit(j.Pool())
	j.Require(!seen && auditErr == nil && beforeReject.Financial == audit.Financial, "отказ записал команду или эффекты", auditErr)
	old, err := invokeOperator(j, file, "resume", resume...)
	j.Require(err == nil && old.History.Applied == active.History.Applied && old.Control == halt.History.Current && audit == j.ReadHistoryAudit(j.Pool()), "исторический RESUME с более поздним HALT", err)
	j.Require(errors.Is(runPaperCLI(j, m, args, nil), context.Canceled), "реальный consumer закрытия")
	done, err := invokeOperator(j, file, "status")
	j.Require(err == nil && done.Control.State.Mode == "HALTED" && done.Close.State == "CLOSED" && done.Close.ReceiptHash == done.ReceiptHash, "фактическое виртуальное закрытие", err)
	now := j.ReadHistoryAudit(j.Pool())
	again, err := invokeOperator(j, file, "close-all", closeArgs...)
	j.Require(err == nil && again.History == halt.History && now == j.ReadHistoryAudit(j.Pool()), "повтор команды не создаёт исполнения", err)
	finished, err := invokeOperator(j, file, "resume", resumeAfter...)
	j.Require(err == nil && finished.Control.State.Mode == "ACTIVE" && now.Financial == j.ReadHistoryAudit(j.Pool()).Financial, "после CLOSED та же команда разрешена", err)
	j.DamagePaperRecoveryFact(t.Context(), "audit", "confirmed")
	partial, err := invokeOperator(j, file, "resume", resume...)
	j.Require(errors.Is(err, s.ErrPaperCommandCurrentUnavailable) && partial.State == "APPLIED_CURRENT_UNAVAILABLE" && partial.History.Applied == active.History.Applied && partial.Control == (s.PaperCommandState{}), "потерянное подтверждение текущего режима", err)
}

// TestOperatorInput отклоняет некорректную команду до DB/owner и не раскрывает её служебные значения.
func TestOperatorInput(t *testing.T) {
	j := pgtest.Journal{T: t}
	svc := paper.OperatorService{}
	cmd := paper.OperatorCommand{Kind: "close-all", ID: "a", ActorID: "actor", Reason: "test", ExpectedRevision: 1, Confirmed: true}
	for _, change := range []func(*paper.OperatorCommand){
		func(c *paper.OperatorCommand) { c.Kind = "unknown" },
		func(c *paper.OperatorCommand) { c.ActorID = "" },
		func(c *paper.OperatorCommand) { c.ID = " " },
		func(c *paper.OperatorCommand) { c.Reason = "" },
		func(c *paper.OperatorCommand) { c.Confirmed = false },
		func(c *paper.OperatorCommand) { c.ExpectedRevision = 0 },
	} {
		bad := cmd
		change(&bad)
		_, err := svc.Apply(t.Context(), bad)
		j.Require(errors.Is(err, paper.ErrOperatorCommand), bad, err)
	}
	n, _ := model.ParseAmount("1")
	d, _ := model.ParseAmount("3")
	third, err := n.Quo(d)
	j.Done(nil, err)
	actual := paperwire.PortfolioState{AsOf: time.Now().UTC(), FundingThrough: time.Now().UTC(), WalletUSDT: n, EquityUSDT: n, Positions: map[string]model.LongPosition{"BTCUSDT": {Quantity: n, CostBasisUSDT: third}}}
	raw, err := paperwire.EncodePortfolioState(actual)
	j.Require(err == nil && bytes.Contains(raw, []byte(`"numerator":"1","denominator":"3"`)), "нет точного ratio", err)
	t.Setenv("MOMENTUM_POSTGRES_DSN", "poison-secret")
	err = run([]string{"paper", "status", "-config", "/absent", "-account", "paper/cli"}, io.Discard, nil)
	j.Require(err != nil && !strings.Contains(err.Error(), "poison-secret"), "unsafe output")
}
