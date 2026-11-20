package backtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// ErrBaselineCycle обозначает невозможность подготовить неизменяемый цикл.
var ErrBaselineCycle = errors.New("invalid baseline replay cycle")

// BaselineCycleInput задаёт исходное состояние и явный исторический cutoff.
type BaselineCycleInput struct {
	Config         config.Config
	Model          SimulationModel
	Dataset        FrozenDataset
	Start, End, At time.Time
	Kind           string
	WalletUSDT     model.Decimal
	Positions      map[string]model.LongPosition
	Marks          map[string]model.LongMarkObservation
	Funding        []model.LongFundingAssessment
}

// PreparedBaselineCycle сохраняет funding-cutoff, неизменяемый план и дельты.
type PreparedBaselineCycle struct {
	RunID, CycleID string
	Cutoff         AccountingResult
	Plan           model.TradePlan
	Projection     planner.DeltaProjection
}

type baselineReplayIdentity struct {
	Format, ConfigSHA256, DatasetSHA256, ModelSHA256 string
	Start, End                                       string
}

// PrepareBaselineCycle связывает D-07 accounting с замороженным D-01 планом.
func PrepareBaselineCycle(input BaselineCycleInput) (PreparedBaselineCycle, error) {
	cfg := input.Config.Values()
	_, modelHash, err := input.Model.Snapshot()
	if err != nil {
		return PreparedBaselineCycle{}, errors.Join(ErrBaselineCycle, err)
	}
	parameters := input.Model.Parameters()
	bound, err := NewSimulationModel(input.Config, input.Dataset.Manifest, parameters)
	if err != nil {
		return PreparedBaselineCycle{}, errors.Join(ErrBaselineCycle, err)
	}
	_, boundHash, err := bound.Snapshot()
	if err != nil || boundHash != modelHash || !validBaselineCycleTime(input, cfg.StartPolicy) {
		return PreparedBaselineCycle{}, ErrBaselineCycle
	}
	accounting, err := ProjectAccountingStep(AccountingInput{
		Symbols: cfg.Symbols, AsOf: input.At, MarkMaxAge: parameters.MarkMaxAge,
		WalletUSDT: input.WalletUSDT, Positions: input.Positions, Marks: input.Marks, Funding: input.Funding,
	})
	if err != nil {
		return PreparedBaselineCycle{}, errors.Join(ErrBaselineCycle, err)
	}
	active, err := ReplayDailySignal(input.Dataset, cfg.Symbols, input.At)
	if err != nil {
		return PreparedBaselineCycle{}, errors.Join(ErrBaselineCycle, err)
	}
	_, configHash, err := input.Config.Snapshot()
	if err != nil {
		return PreparedBaselineCycle{}, errors.Join(ErrBaselineCycle, err)
	}
	runID := baselineIdentity(baselineReplayIdentity{
		Format: "momentum/baseline-replay/v1", ConfigSHA256: configHash,
		DatasetSHA256: cfg.DataSource.ManifestSHA256, ModelSHA256: modelHash,
		Start: input.Start.Round(0).UTC().Format(time.RFC3339Nano),
		End:   input.End.Round(0).UTC().Format(time.RFC3339Nano),
	})
	cycleID := baselineIdentity([]string{"momentum/baseline-cycle/v1", runID, input.Kind, accounting.AsOf.Format(time.RFC3339Nano)})
	prices := make(map[string]model.Decimal, len(cfg.Symbols))
	quantities := make(map[string]model.Decimal, len(cfg.Symbols))
	steps := make(map[string]model.Decimal, len(cfg.Symbols))
	for _, symbol := range cfg.Symbols {
		prices[symbol] = input.Marks[symbol].Price
		quantities[symbol] = input.Positions[symbol].Quantity
		steps[symbol] = parameters.Rules[symbol].QtyStep
	}
	snapshot, err := model.NewPortfolioSnapshot(model.SnapshotInput{
		Environment: "backtest", AccountID: "simulation/" + runID, ConfigVersion: cfg.ProfileVersion,
		CapturedAt: accounting.AsOf, Equity: accounting.EquityUSDT, Prices: prices, Positions: quantities,
	})
	if err != nil {
		return PreparedBaselineCycle{}, errors.Join(ErrBaselineCycle, err)
	}
	targets, err := planner.BaselineTargets(cfg.Symbols, snapshot, active, steps, parameters.FeeRate)
	if err != nil {
		return PreparedBaselineCycle{}, errors.Join(ErrBaselineCycle, err)
	}
	plan, err := model.NewTradePlan(model.PlanInput{
		Snapshot: snapshot, Targets: targets,
		Metadata: model.PlanMetadata{
			StrategyID: cfg.StrategyID, RunID: cycleID, ProfileID: cfg.Profile, ModelID: cfg.ModelID,
			FormulaID: "D-01", ConfigHash: configHash, ScheduledAt: accounting.AsOf, EquityCutoffAt: accounting.AsOf, Revision: 1,
		},
	})
	if err != nil {
		return PreparedBaselineCycle{}, errors.Join(ErrBaselineCycle, err)
	}
	projection, err := planner.ProjectBaselineDeltas(cfg.Symbols, plan, parameters.Rules)
	if err != nil {
		return PreparedBaselineCycle{}, errors.Join(ErrBaselineCycle, err)
	}
	return PreparedBaselineCycle{RunID: runID, CycleID: cycleID, Cutoff: accounting, Plan: plan, Projection: projection}, nil
}

// validBaselineCycleTime проверяет диапазон, onboarding и номинальную неделю.
func validBaselineCycleTime(input BaselineCycleInput, policy string) bool {
	span := input.Dataset.Manifest.Range
	if input.Start.IsZero() || input.End.IsZero() || input.At.IsZero() || !input.Start.Before(input.End) ||
		input.Start.Before(span.Start) || input.End.After(span.End) || input.At.Before(input.Start) || !input.At.Before(input.End) {
		return false
	}
	if input.Kind == "onboarding" {
		return policy == "now" && input.At.Equal(input.Start)
	}
	if input.Kind != "weekly" || policy == "now" && input.At.Equal(input.Start) {
		return false
	}
	weekly, exists, err := FirstWeeklyBoundary(input.At, input.End)
	return err == nil && exists && weekly.Equal(input.At)
}

// baselineIdentity хеширует фиксированное JSON-представление технической идентичности.
func baselineIdentity(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
