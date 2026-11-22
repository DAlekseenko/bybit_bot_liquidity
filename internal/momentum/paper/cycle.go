package paper

import (
	"bytes"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/paperwire"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// ErrPaperCycle обозначает отказ финансовой подготовки.
var ErrPaperCycle = errors.New("invalid paper cycle")

// ErrPaperModelBinding обозначает несовпадение явного профиля модели.
var ErrPaperModelBinding = errors.New("invalid paper model binding")

// PlanBuilder получает явную стратегию без доступа к внешним источникам.
type PlanBuilder func(string, model.PlanMetadata, model.PortfolioSnapshot, map[string][]data.DailyClose, map[string]planner.MarketDeltaRules, model.Decimal) (model.TradePlan, error)

// CycleInput содержит все исходные данные чистого цикла.
type CycleInput struct {
	Run         paperwire.RunBinding
	Sequence    uint64
	Previous    *ResolvedCycle
	Purpose     paperwire.Purpose
	Profile     paperwire.Profile
	Observation paperwire.Observation
	FormulaID   string
	PlanBuilder PlanBuilder
	Admission   FillAdmission
}

// PreparedCycle хранит проверенный финансовый вход.
// Адаптер сохраняет baseline delta/batch restrictions, включая D-01 и три символа.
type PreparedCycle struct{ input paperwire.InputRecord }

// ValidatePaperModel связывает paper-конфигурацию с точным явным профилем.
func ValidatePaperModel(cfg config.Config, profile paperwire.Profile) error {
	if !publicConfig(cfg) {
		return ErrPaperModelBinding
	}
	_, hash, err := paperwire.EncodeProfile(profile)
	if err != nil {
		return errors.Join(ErrPaperModelBinding, err)
	}
	if cfg.Values().ModelID != hash {
		return ErrPaperModelBinding
	}
	return nil
}

// PrepareCycle проверяет модель прежде любых проекций и строит immutable input.
func PrepareCycle(in CycleInput) (PreparedCycle, error) {
	result, err := prepareCycle(in)
	if err != nil {
		return PreparedCycle{}, errors.Join(ErrPaperCycle, err)
	}
	return result, nil
}

// prepareCycle применяет funding к прежним позициям и проверяет полный пакет.
func prepareCycle(in CycleInput) (PreparedCycle, error) {
	if err := ValidatePaperModel(in.Run.Config, in.Profile); err != nil {
		return PreparedCycle{}, err
	}
	if in.PlanBuilder == nil || in.Admission == nil || strings.TrimSpace(in.FormulaID) == "" {
		return PreparedCycle{}, ErrPaperCycle
	}
	v := in.Run.Config.Values()
	_, cfgHash, err := in.Run.Config.Snapshot()
	if err != nil {
		return PreparedCycle{}, err
	}
	d := paperwire.CycleDocument{Run: in.Run, Sequence: in.Sequence, Purpose: in.Purpose, Profile: in.Profile, Observation: in.Observation}
	if in.Sequence == 1 && in.Previous == nil {
		wallet, err := paperwire.ParseRecordedAmount(v.InitialEquity)
		if err != nil {
			return PreparedCycle{}, err
		}
		d.Before = paperwire.PortfolioState{AsOf: in.Run.ScheduledAt, FundingThrough: in.Run.ScheduledAt, WalletUSDT: wallet, EquityUSDT: wallet, Positions: make(map[string]model.LongPosition)}
		for _, symbol := range v.Symbols {
			d.Before.Positions[symbol] = model.LongPosition{Quantity: model.Zero(), CostBasisUSDT: model.Zero()}
		}
	} else {
		if in.Sequence <= 1 || in.Previous == nil || in.Previous.Hash() == "" {
			return PreparedCycle{}, ErrPaperCycle
		}
		previous := in.Previous.Binding()
		if previous.Sequence == ^uint64(0) || previous.Sequence+1 != in.Sequence || previous.ConfigHash != cfgHash || previous.ModelHash != v.ModelID || previous.Scope != (paperwire.Scope{Environment: "paper", AccountID: in.Run.AccountID, StrategyID: in.Run.StrategyID}) {
			return PreparedCycle{}, ErrPaperCycle
		}
		d.Before = in.Previous.ActualAfter()
		if !in.Run.ScheduledAt.After(d.Before.AsOf) {
			return PreparedCycle{}, ErrPaperCycle
		}
		d.PreviousInputHash, d.PreviousResolutionHash = in.Previous.InputHash(), in.Previous.Hash()
	}
	// Сохранённое время захвата заменяет часы хоста при проверке источника.
	market, err := ValidateCycleObservation(SourceRequest{Run: in.Run, Purpose: in.Purpose, Profile: in.Profile, DailyBars: in.Observation.DailyBars, FundingAfter: d.Before.FundingThrough, Now: func() time.Time { return in.Observation.CapturedAt }}, in.Observation)
	if err != nil {
		return PreparedCycle{}, err
	}
	d.Cutoff = copyState(d.Before)
	funding := slices.Clone(market.Funding)
	slices.SortFunc(funding, func(a, b model.LongFundingAssessment) int {
		if c := a.Event.FundingTime.Compare(b.Event.FundingTime); c != 0 {
			return c
		}
		return strings.Compare(a.Symbol, b.Symbol)
	})
	for _, assessment := range funding {
		posting, err := model.ProjectLongFunding(d.Before.Positions[assessment.Symbol], assessment)
		if err != nil {
			return PreparedCycle{}, err
		}
		d.Cutoff.WalletUSDT, err = d.Cutoff.WalletUSDT.Add(posting.CashDeltaUSDT)
		if err != nil {
			return PreparedCycle{}, err
		}
	}
	d.Cutoff, err = accountState(d.Cutoff, d, market.Marks)
	if err != nil {
		return PreparedCycle{}, err
	}
	quantities := make(map[string]model.Decimal, len(v.Symbols))
	for symbol, position := range d.Cutoff.Positions {
		quantities[symbol] = position.Quantity
	}
	snapshot, err := model.NewPortfolioSnapshot(model.SnapshotInput{Environment: "paper", AccountID: in.Run.AccountID, ConfigVersion: v.ProfileVersion, CapturedAt: in.Run.ScheduledAt, Equity: d.Cutoff.EquityUSDT, Prices: market.ReferencePrices, Positions: quantities})
	if err != nil {
		return PreparedCycle{}, err
	}
	metadata := model.PlanMetadata{StrategyID: in.Run.StrategyID, RunID: in.Run.RunID, ProfileID: v.Profile, ModelID: v.ModelID, FormulaID: in.FormulaID, ConfigHash: cfgHash, ScheduledAt: in.Run.ScheduledAt, EquityCutoffAt: in.Run.ScheduledAt, Revision: 1}
	d.Plan, err = in.PlanBuilder(in.Purpose.Kind, metadata, snapshot, market.DailySeries, maps.Clone(in.Profile.Parameters.Rules), in.Profile.Parameters.FeeRate)
	if err != nil {
		return PreparedCycle{}, err
	}
	if d.Plan.Metadata() != metadata || d.Plan.Snapshot().Hash() != snapshot.Hash() || len(d.Plan.Targets()) != len(v.Symbols) {
		return PreparedCycle{}, ErrPaperCycle
	}
	for i, target := range d.Plan.Targets() {
		if target.Symbol != v.Symbols[i] || in.Purpose.Kind == "close-all" && !isZero(target.Quantity) {
			return PreparedCycle{}, ErrPaperCycle
		}
	}
	batch, err := SimulateBaselineFills(FillInput{Symbols: v.Symbols, Plan: d.Plan, Rules: in.Profile.Parameters.Rules, WalletUSDT: d.Cutoff.WalletUSDT, Positions: d.Cutoff.Positions, ReferencePrices: snapshot.Prices(), FeeRate: in.Profile.Parameters.FeeRate, SlippageRate: in.Profile.Parameters.SlippageRate, Admission: in.Admission})
	if err != nil {
		return PreparedCycle{}, err
	}
	for _, posting := range batch.Postings {
		if err := finiteFill(posting.Fill); err != nil {
			return PreparedCycle{}, err
		}
		d.Fills = append(d.Fills, posting.Fill)
	}
	d.ExpectedAfter, err = accountState(paperwire.PortfolioState{WalletUSDT: batch.WalletUSDT, Positions: batch.Positions}, d, market.Marks)
	if err != nil {
		return PreparedCycle{}, err
	}
	record, err := paperwire.EncodeInputRecord(d)
	return PreparedCycle{input: record}, err
}

// accountState оценивает позиции без повторного funding.
func accountState(state paperwire.PortfolioState, d paperwire.CycleDocument, marks map[string]model.LongMarkObservation) (paperwire.PortfolioState, error) {
	result, err := backtest.ProjectAccountingStep(backtest.AccountingInput{Symbols: d.Run.Config.Values().Symbols, AsOf: d.Run.ScheduledAt, MarkMaxAge: d.Profile.Parameters.MarkMaxAge, WalletUSDT: state.WalletUSDT, Positions: state.Positions, Marks: marks})
	state.AsOf, state.FundingThrough, state.EquityUSDT = d.Run.ScheduledAt, d.Run.ScheduledAt, result.EquityUSDT
	return state, err
}

// DecodePreparedCycle повторяет финансовую подготовку сохранённых исходных данных.
func DecodePreparedCycle(raw []byte, previous *ResolvedCycle, builder PlanBuilder, admission FillAdmission) (PreparedCycle, error) {
	record, err := paperwire.DecodeInputRecord(raw)
	if err != nil {
		return PreparedCycle{}, errors.Join(ErrPaperCycle, err)
	}
	d := record.Values()
	prepared, err := PrepareCycle(CycleInput{Run: d.Run, Sequence: d.Sequence, Previous: previous, Purpose: d.Purpose, Profile: d.Profile, Observation: d.Observation, FormulaID: d.Plan.Metadata().FormulaID, PlanBuilder: builder, Admission: admission})
	if err != nil {
		return PreparedCycle{}, err
	}
	if !bytes.Equal(raw, prepared.Payload()) {
		return PreparedCycle{}, ErrPaperCycle
	}
	return prepared, nil
}

// copyState отделяет изменяемую карту позиций.
func copyState(s paperwire.PortfolioState) paperwire.PortfolioState {
	s.Positions = maps.Clone(s.Positions)
	return s
}

// Payload возвращает копию канонических байтов.
func (p PreparedCycle) Payload() []byte { return p.input.Payload() }

// Hash возвращает идентичность входа.
func (p PreparedCycle) Hash() string { return p.input.Hash() }

// Binding возвращает связи цикла.
func (p PreparedCycle) Binding() paperwire.CycleBinding { return p.input.Header() }

// Plan возвращает immutable план.
func (p PreparedCycle) Plan() model.TradePlan { return p.input.Values().Plan }

// Profile возвращает отдельную копию профиля.
func (p PreparedCycle) Profile() paperwire.Profile { return p.input.Values().Profile }

// Purpose возвращает назначение цикла.
func (p PreparedCycle) Purpose() paperwire.Purpose { return p.input.Values().Purpose }

// Before возвращает отдельное исходное состояние.
func (p PreparedCycle) Before() paperwire.PortfolioState { return p.input.Values().Before }

// Cutoff возвращает отдельное состояние после funding.
func (p PreparedCycle) Cutoff() paperwire.PortfolioState { return p.input.Values().Cutoff }

// ExpectedAfter возвращает отдельный результат полного исполнения.
func (p PreparedCycle) ExpectedAfter() paperwire.PortfolioState {
	return p.input.Values().ExpectedAfter
}

// ExpectedFills возвращает копию ожидаемых исполнений.
func (p PreparedCycle) ExpectedFills() []model.ConfirmedFill { return p.input.Values().Fills }
