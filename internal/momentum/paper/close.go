package paper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

// ErrPaperCloseBlocked означает, что текущий запрос не подтверждён плоским портфелем.
var ErrPaperCloseBlocked = errors.New("paper close-all is blocked")

// PaperCloseResult отражает только подтверждённые состояние позиций и итог закрытия.
type PaperCloseResult struct {
	State, CommandID, RunID, ReceiptHash string
	Revision                             uint64
	Actual                               json.RawMessage
}

// closeRunID строит канонический ключ одного подтверждённого запроса.
func closeRunID(scope s.ControlScope, id string) string {
	key := map[string]any{"format": "momentum/paper-control-run/v1", "scope": map[string]string{"environment": scope.Environment, "account_id": scope.AccountID, "strategy_id": scope.StrategyID}, "command_id": id}
	raw, _ := json.Marshal(key)
	sum := sha256.Sum256(raw)
	return "paper/control/" + hex.EncodeToString(sum[:])
}

// PaperCloseState выводит CLOSED только из точного COMPLETED receipt с нулевыми остатками.
func PaperCloseState(rec RecoveredPaper) (PaperCloseResult, error) {
	c := rec.proof.facts.Control
	out := PaperCloseResult{State: "IDLE"}
	purpose := paperwire.Purpose{Kind: c.Kind, CommandID: c.CommandID, ControlRevision: c.State.Revision}
	if c.State.Mode == "HALTED" && c.Action == "HALT" && c.Actor == "OPERATOR" && c.Kind == "close-all" {
		out = PaperCloseResult{State: "PENDING", CommandID: c.CommandID, Revision: c.State.Revision, RunID: closeRunID(rec.proof.Scope(), c.CommandID)}
	}
	last, closed := rec.Last()
	var actual paperwire.PortfolioState
	if progress, open := rec.Progress(); open {
		actual = progress.ActualAfter()
	} else if closed {
		actual = last.ActualAfter()
	} else {
		return out, nil
	}
	var err error
	out.Actual, err = paperwire.EncodePortfolioState(actual)
	if err != nil {
		return PaperCloseResult{}, err
	}
	if _, open := rec.Current(); out.State == "IDLE" || open || !closed || last.Binding().RunID != out.RunID || last.input.Values().Purpose != purpose {
		return out, nil
	}
	out.ReceiptHash = last.Hash()
	out.State = "BLOCKED"
	if last.Kind() != "COMPLETED" {
		return out, nil
	}
	for _, pos := range actual.Positions {
		if !isZero(pos.Quantity) {
			return out, nil
		}
	}
	out.State = "CLOSED"
	return out, nil
}

// outcome возвращает причину известного отказа для вызывающего обработчика.
func (out PaperCloseResult) outcome() (PaperCloseResult, error) {
	if out.State == "BLOCKED" {
		return out, ErrPaperCloseBlocked
	}
	return out, nil
}

// AdvanceCloseAll под живым владельцем обрабатывает текущую подтверждённую команду закрытия.
func (r CycleRunner) AdvanceCloseAll(ctx context.Context, cfg config.Config, profile paperwire.Profile) (PaperCloseResult, error) {
	if err := r.Owner.PermitPaperScope(ctx, r.DB, r.Scope); err != nil {
		return PaperCloseResult{}, err
	}
	rec, out, err := r.closeView(ctx)
	if err != nil {
		return out, err
	}
	if out.State != "PENDING" {
		return out.outcome()
	}
	c := rec.proof.facts.Control
	_, hash, err := cfg.Snapshot()
	if err != nil || hash != c.ConfigHash {
		out.State = "BLOCKED"
		return out, ErrPaperCloseBlocked
	}
	purpose := paperwire.Purpose{Kind: "close-all", CommandID: c.CommandID, ControlRevision: c.State.Revision}
	if old, ok := rec.Current(); ok && old.Binding().RunID != out.RunID {
		if old.Purpose().Kind != "rebalance" {
			out.State = "BLOCKED"
			return out, ErrPaperCloseBlocked
		}
		if !rec.proof.facts.PlanFound {
			if _, err = s.StorePlanRevision(ctx, r.DB, old.Plan()); err != nil {
				return out, err
			}
		}
		at := c.CreatedAt.UTC()
		if at.Before(old.Binding().ScheduledAt) {
			at = old.Binding().ScheduledAt
		}
		d := ResolutionDisposition{Kind: "ABORTED", CommandID: purpose.CommandID, ControlRevision: purpose.ControlRevision, ResolvedAt: at}
		key := s.PaperWriteKey{Scope: r.Scope, RunID: old.Binding().RunID, InputHash: old.Hash()}
		sealed, err := s.FinalizePaperRun(ctx, r.DB, s.PaperFinalizeRequest{Key: key, Owner: r.Owner, Kind: d.Kind, CommandID: d.CommandID, ControlRevision: d.ControlRevision}, BuildPaperResolution(old, d))
		if err != nil {
			return out, err
		}
		if !sealed.Confirmed {
			return out, s.ErrPaperWriteUnknown
		}
		var fresh PaperCloseResult
		rec, fresh, err = r.closeView(ctx)
		if err != nil {
			return out, err
		}
		if fresh.CommandID != out.CommandID || fresh.Revision != out.Revision {
			return out, ErrPaperCloseBlocked
		}
		out = fresh
		if out.State != "PENDING" {
			return out.outcome()
		}
	}
	if err = r.Owner.Permit(ctx); err != nil {
		return out, err
	}
	last, closed := rec.Last()
	if closed && last.Kind() == "ABORTED" && last.CommandID() == purpose.CommandID && last.ControlRevision() == purpose.ControlRevision {
		facts, err := s.ReadPaperRunFacts(ctx, r.DB, r.Scope, last.Binding().RunID)
		if err != nil {
			return out, err
		}
		intents := facts.Intents()
		for _, link := range slices.Sorted(maps.Keys(intents)) {
			intent := intents[link]
			if intent.State == "PENDING" || intent.State == "PARTIALLY_FILLED" {
				if err = s.SetOrderIntentState(ctx, r.DB, r.Scope.Environment, r.Scope.AccountID, link, "CANCELLED"); err != nil {
					return out, err
				}
			}
		}
	}
	latest := c.CreatedAt.UTC()
	if closed && last.Binding().ScheduledAt.After(latest) {
		latest = last.Binding().ScheduledAt
	}
	at := latest.Truncate(time.Microsecond).Add(time.Microsecond)
	candidate := s.RebalanceRunInput{RunID: out.RunID, OnboardingID: out.RunID, AccountID: r.Scope.AccountID, StrategyID: r.Scope.StrategyID, Config: cfg, ScheduledAt: at}
	run, err := s.CreateOrReadRebalanceRun(ctx, r.DB, candidate)
	if err != nil {
		return out, err
	}
	if run.RunID != out.RunID || run.OnboardingID != out.RunID || !run.ScheduledAt.After(latest) {
		return out, ErrCycleAdvance
	}
	_, err = r.AdvanceCycle(ctx, run, purpose, profile)
	if errors.Is(err, ErrRecordedCycleCorrupt) || errors.Is(err, ErrPaperRecoveryCorrupt) || errors.Is(err, s.ErrPaperWriteRejected) {
		return out, err
	}
	if ctx.Err() == nil && (errors.Is(err, ErrPaperCycle) || errors.Is(err, ErrPaperObservation) ||
		errors.Is(err, ErrPaperModelBinding) || errors.Is(err, paperwire.ErrPaperResourceLimit) || errors.Is(err, context.DeadlineExceeded)) {
		out.State = "BLOCKED"
		return out, errors.Join(ErrPaperCloseBlocked, err)
	}
	if err != nil {
		return out, err
	}
	_, result, err := r.closeView(ctx)
	if err != nil {
		return out, err
	}
	if result.CommandID != out.CommandID || result.Revision != out.Revision {
		return out, ErrPaperCloseBlocked
	}
	return result.outcome()
}

// closeView читает подтверждённую историю и состояние команды из одного proof.
func (r CycleRunner) closeView(ctx context.Context) (RecoveredPaper, PaperCloseResult, error) {
	rec, err := Recover(ctx, r.RecoveryRequest)
	if err != nil {
		return rec, PaperCloseResult{}, err
	}
	out, err := PaperCloseState(rec)
	return rec, out, err
}
