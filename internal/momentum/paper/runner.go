package paper

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

// ErrCycleAdvance означает несовместимость запроса с проверенной историей цикла.
var ErrCycleAdvance = errors.New("paper cycle cannot advance")

// CycleRunner продвигает один зарегистрированный paper run под действующим владельцем.
type CycleRunner struct {
	RecoveryRequest
	Owner     *s.ExecutionOwner
	DailyBars int
	FormulaID string
	Now       func() time.Time
	Transport http.RoundTripper
}

// observeCycle строит новый вход из публичных страниц на исходном срезе запуска.
func (r CycleRunner) observeCycle(ctx context.Context, run paperwire.RunBinding, purpose paperwire.Purpose, profile paperwire.Profile, previous *ResolvedCycle) (PreparedCycle, error) {
	if err := ValidatePaperModel(run.Config, profile); err != nil {
		return PreparedCycle{}, err
	}
	source := SourceRequest{Run: run, Purpose: purpose, Profile: profile, DailyBars: r.DailyBars, FundingAfter: run.ScheduledAt, Now: r.Now, Transport: r.Transport}
	sequence := uint64(1)
	if previous != nil {
		h := previous.Binding()
		if h.Sequence == math.MaxUint64 || !run.ScheduledAt.After(h.ScheduledAt) {
			return PreparedCycle{}, ErrCycleAdvance
		}
		sequence = h.Sequence + 1
		source.FundingAfter = previous.ActualAfter().FundingThrough
	}
	if purpose.Kind == "close-all" {
		source.DailyBars = 0
	}
	if purpose.Kind == "rebalance" {
		ready := PublicReadiness{Config: run.Config, DailyBars: r.DailyBars, RPCTimeout: profile.RPCTimeout, SourceMaxAge: profile.SourceMaxAge, Now: r.Now, Transport: r.Transport}
		through, err := ready.ClosedThrough(ctx)
		if err != nil {
			return PreparedCycle{}, err
		}
		if through.Before(run.ScheduledAt.Truncate(24 * time.Hour)) {
			return PreparedCycle{}, ErrCycleAdvance
		}
	}
	observation, err := ReadCycleObservation(ctx, source)
	if err != nil {
		return PreparedCycle{}, err
	}
	return PrepareCycle(CycleInput{Run: run, Sequence: sequence, Previous: previous, Purpose: purpose, Profile: profile, Observation: observation, FormulaID: r.FormulaID, PlanBuilder: r.PlanBuilder, Admission: r.Admission})
}

// AdvanceCycle продолжает открытый цикл либо готовит новый и возвращает его проверенные факты.
func (r CycleRunner) AdvanceCycle(ctx context.Context, run s.StoredRebalanceRun, purpose paperwire.Purpose, profile paperwire.Profile) (RecordedPaperCycle, error) {
	var zero RecordedPaperCycle
	if ctx == nil || r.DB == nil || r.Owner == nil || run.AccountID != r.Scope.AccountID || run.StrategyID != r.Scope.StrategyID {
		return zero, ErrCycleAdvance
	}
	if purpose.Kind != "rebalance" && purpose.Kind != "close-all" ||
		purpose.Kind == "rebalance" && (purpose.CommandID != "" || purpose.ControlRevision != 0) ||
		purpose.Kind == "close-all" && (purpose.CommandID == "" || purpose.ControlRevision == 0) {
		return zero, ErrCycleAdvance
	}
	if err := r.Owner.PermitPaperScope(ctx, r.DB, r.Scope); err != nil {
		return zero, err
	}
	recovered, err := Recover(ctx, r.RecoveryRequest)
	if err != nil {
		return zero, err
	}
	prev, err := cyclePredecessor(recovered, run)
	if err != nil {
		return zero, err
	}
	read := func() (RecordedPaperCycle, error) {
		return ReadRecordedPaperCycle(ctx, r.DB, r.Scope, run.RunID, prev, r.PlanBuilder, r.Admission)
	}
	view, err := read()
	if err != nil {
		return zero, err
	}
	p, found := view.Prepared()
	if found && (p.Purpose() != purpose || !run.ScheduledAt.Equal(p.Binding().ScheduledAt) || run.OnboardingID != p.input.Values().Run.OnboardingID) {
		return zero, ErrCycleAdvance
	}
	if view.facts.Completed {
		if _, ok := view.Resolution(); !ok {
			return zero, ErrCycleAdvance
		}
		return view, nil
	}
	if !found {
		if !s.MayAttemptPaperFill(purpose, view.facts.Control, model.FillSell) {
			return view, nil
		}
		original, err := s.CreateOrReadRebalanceRun(ctx, r.DB, run.RebalanceRunInput)
		if err != nil {
			return zero, err
		}
		if original.RunID != run.RunID || !original.ScheduledAt.Equal(run.ScheduledAt) || original.OnboardingID != run.OnboardingID {
			return zero, ErrCycleAdvance
		}
		p, err = r.observeCycle(ctx, paperwire.RunBinding(original.RebalanceRunInput), purpose, profile, prev)
		if err != nil {
			return zero, err
		}
		if err = r.Owner.Permit(ctx); err != nil {
			return zero, err
		}
		if _, err = s.StorePaperInput(ctx, r.DB, p.input); err != nil {
			return zero, err
		}
		view, err = read()
		if err != nil {
			return zero, err
		}
		if saved, ok := view.Prepared(); !ok || saved.Hash() != p.Hash() {
			return zero, ErrCycleAdvance
		}
	}
	if err = r.Owner.Permit(ctx); err != nil {
		return zero, err
	}
	if !view.PlanFound() {
		if _, err = s.StorePlanRevision(ctx, r.DB, p.Plan()); err != nil {
			return zero, err
		}
	}
	for _, intent := range view.OrderIntents() {
		if err = r.Owner.Permit(ctx); err != nil {
			return zero, err
		}
		if err = s.RecordOrderIntent(ctx, r.DB, intent); err != nil {
			return zero, err
		}
	}
	key := s.PaperWriteKey{Scope: r.Scope, RunID: run.RunID, InputHash: p.Hash()}
	if err = r.applyRemaining(ctx, key, view, s.RecordPaperFill); err != nil {
		return zero, err
	}
	view, err = read()
	if err != nil {
		return zero, err
	}
	if !view.progress.Complete() {
		return view, nil
	}
	d := ResolutionDisposition{Kind: "COMPLETED", ResolvedAt: p.Binding().ScheduledAt}
	sealed, err := s.FinalizePaperRun(ctx, r.DB, s.PaperFinalizeRequest{Key: key, Owner: r.Owner, Kind: d.Kind}, BuildPaperResolution(p, d))
	if err != nil {
		return zero, err
	}
	if !sealed.Confirmed {
		return zero, s.ErrPaperWriteUnknown
	}
	view, err = read()
	if err != nil {
		return zero, err
	}
	if _, ok := view.Resolution(); !ok {
		return zero, ErrCycleAdvance
	}
	return view, nil
}

// cyclePredecessor выбирает исходный receipt для повтора, продолжения или нового финансового шага.
func cyclePredecessor(recovered RecoveredPaper, run s.StoredRebalanceRun) (*ResolvedCycle, error) {
	_, candidateHash, hashErr := run.Config.Snapshot()
	if hashErr != nil || run.ConfigHash != candidateHash || run.ConfigHash != recovered.Proof().ConfigHash() {
		return nil, ErrCycleAdvance
	}
	prev := recovered.last
	if head, ok := recovered.Proof().Head(); ok {
		if head.RunID == run.RunID {
			prev = recovered.proof.previous
		} else if recovered.current.Hash() != "" || !run.ScheduledAt.After(head.ScheduledAt) {
			return nil, ErrCycleAdvance
		}
	}
	return prev, nil
}

// applyRemaining передаёт writer только допустимый точный остаток каждого намерения.
func (r CycleRunner) applyRemaining(ctx context.Context, key s.PaperWriteKey, view RecordedPaperCycle, write func(context.Context, *sql.DB, s.PaperFillRequest, s.PaperFillCheck) (s.PaperFillResult, error)) error {
	p, _ := view.Prepared()
	purpose := p.Purpose()
	intents := view.OrderIntents()
	for _, left := range view.progress.Remaining() {
		if isZero(left.Remaining) || !s.MayAttemptPaperFill(purpose, view.facts.Control, left.Side) {
			continue
		}
		fill, err := MakeFillSlice(p, left.IntentIndex, left.Confirmed, left.Remaining)
		if err != nil {
			return err
		}
		req := s.PaperFillRequest{Key: key, Owner: r.Owner, OrderLinkID: intents[left.IntentIndex].OrderLinkID, Fill: fill}
		got, err := write(ctx, r.DB, req, CheckPaperFill(p))
		if err != nil {
			return err
		}
		if !got.Confirmed {
			return s.ErrPaperWriteUnknown
		}
	}
	return nil
}
