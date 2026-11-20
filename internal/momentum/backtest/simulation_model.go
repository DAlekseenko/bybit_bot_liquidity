package backtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// ErrSimulationModel обозначает некорректную модель симуляции Momentum.
var ErrSimulationModel = errors.New("invalid momentum simulation model")

// SimulationParameters содержит явно заданные численные входы модели.
type SimulationParameters struct {
	FeeRate      model.Decimal
	SlippageRate model.Decimal
	MarkMaxAge   time.Duration
	Rules        map[string]planner.MarketDeltaRules
}

// SimulationModel хранит валидированные параметры и каноническую идентичность.
type SimulationModel struct {
	parameters SimulationParameters
	snapshot   []byte
	hash       string
}

type simulationRule struct {
	Symbol              string `json:"symbol"`
	QtyStep             string `json:"qty_step"`
	MinQty              string `json:"min_qty"`
	MaxMarketQty        string `json:"max_market_qty"`
	MinIncreaseNotional string `json:"min_increase_notional"`
}

type simulationWire struct {
	Format                     string                `json:"format"`
	ConfigSHA256               string                `json:"config_sha256"`
	DatasetManifestSHA256      string                `json:"dataset_manifest_sha256"`
	SourceModelAssumptions     data.ManifestArtifact `json:"source_model_assumptions"`
	OneSidedFeeRate            string                `json:"one_sided_fee_rate"`
	SlippageRate               string                `json:"slippage_rate"`
	MarkMaxAgeNS               int64                 `json:"mark_max_age_ns"`
	MarketRules                []simulationRule      `json:"market_rules"`
	EventOrderVersion          string                `json:"event_order_version"`
	SignalModelRevision        string                `json:"signal_model_revision"`
	ExecutionReferenceRevision string                `json:"execution_reference_revision"`
	MarkObservationRevision    string                `json:"mark_observation_revision"`
	FundingMode                string                `json:"funding_mode"`
	ExpectedFundingCoverage    string                `json:"expected_funding_coverage"`
	HistoricalMargin           string                `json:"historical_margin"`
	Liquidation                string                `json:"liquidation"`
	EquityStop                 string                `json:"equity_stop"`
}

// NewSimulationModel проверяет входы и создаёт неизменяемый снимок модели.
func NewSimulationModel(cfg config.Config, dataset data.ManifestInput, parameters SimulationParameters) (SimulationModel, error) {
	v := cfg.Values()
	if v.SchemaVersion != 1 || v.Mode != "backtest" || v.Profile != "baseline_delta_1x" || v.Leverage != 1 || v.DataSource.Kind != "frozen_dataset" {
		return SimulationModel{}, ErrSimulationModel
	}
	symbols, err := config.CanonicalSymbols(v.Symbols)
	if err != nil {
		return SimulationModel{}, ErrSimulationModel
	}
	_, configHash, err := cfg.Snapshot()
	if err != nil {
		return SimulationModel{}, ErrSimulationModel
	}
	_, datasetHash, err := data.EncodeManifest(dataset)
	if err != nil || dataset.Source.Environment != v.DataSource.Environment || dataset.Source.DatasetID != v.DataSource.DatasetID || datasetHash != v.DataSource.ManifestSHA256 {
		return SimulationModel{}, ErrSimulationModel
	}
	fee, err := canonicalNonnegative(parameters.FeeRate)
	if err != nil {
		return SimulationModel{}, ErrSimulationModel
	}
	slippage, err := canonicalNonnegative(parameters.SlippageRate)
	if err != nil || parameters.MarkMaxAge <= 0 || len(parameters.Rules) != len(symbols) {
		return SimulationModel{}, ErrSimulationModel
	}
	wire := simulationWire{
		Format: "momentum/simulation-model/v1", ConfigSHA256: configHash,
		DatasetManifestSHA256: datasetHash, SourceModelAssumptions: dataset.ModelAssumptions,
		OneSidedFeeRate: fee, SlippageRate: slippage, MarkMaxAgeNS: int64(parameters.MarkMaxAge),
		MarketRules: make([]simulationRule, 0, len(symbols)), EventOrderVersion: "D-07/v1",
		SignalModelRevision: "daily-roc30-closed/v1", ExecutionReferenceRevision: "next-trade-open/v1",
		MarkObservationRevision: "previous-hour-close-at-boundary/v1", FundingMode: "observed-events",
		ExpectedFundingCoverage: "unknown", HistoricalMargin: "unavailable", Liquidation: "unavailable", EquityStop: "disabled",
	}
	cloned := make(map[string]planner.MarketDeltaRules, len(symbols))
	for _, symbol := range symbols {
		rule, ok := parameters.Rules[symbol]
		if !ok {
			return SimulationModel{}, ErrSimulationModel
		}
		values := []model.Decimal{rule.QtyStep, rule.MinQty, rule.MaxMarketQty}
		encoded := make([]string, 4)
		for i, value := range append(values, rule.MinIncreaseNotional) {
			text, err := canonicalNonnegative(value)
			if err != nil || i < 3 && text == "0" {
				return SimulationModel{}, ErrSimulationModel
			}
			encoded[i] = text
		}
		floored, err := model.FloorQty(rule.MaxMarketQty, rule.QtyStep)
		if err != nil {
			return SimulationModel{}, ErrSimulationModel
		}
		cmp, err := floored.Cmp(rule.MinQty)
		if err != nil || cmp < 0 {
			return SimulationModel{}, ErrSimulationModel
		}
		cloned[symbol] = rule
		wire.MarketRules = append(wire.MarketRules, simulationRule{symbol, encoded[0], encoded[1], encoded[2], encoded[3]})
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return SimulationModel{}, ErrSimulationModel
	}
	digest := sha256.Sum256(payload)
	parameters.Rules = cloned
	parameters.FeeRate, _ = model.ParseRate(fee)
	parameters.SlippageRate, _ = model.ParseRate(slippage)
	return SimulationModel{parameters: parameters, snapshot: payload, hash: hex.EncodeToString(digest[:])}, nil
}

// Parameters возвращает параметры и отдельную копию карты правил.
func (m SimulationModel) Parameters() SimulationParameters {
	p := m.parameters
	p.Rules = make(map[string]planner.MarketDeltaRules, len(m.parameters.Rules))
	for symbol, rule := range m.parameters.Rules {
		p.Rules[symbol] = rule
	}
	return p
}

// Snapshot возвращает копию канонических байтов и SHA-256 модели.
func (m SimulationModel) Snapshot() ([]byte, string, error) {
	if len(m.snapshot) == 0 || len(m.hash) != 64 {
		return nil, "", ErrSimulationModel
	}
	return append([]byte(nil), m.snapshot...), m.hash, nil
}

// canonicalNonnegative проверяет конечный decimal, знак и повторный разбор wire-текста.
func canonicalNonnegative(value model.Decimal) (string, error) {
	cmp, err := value.Cmp(model.Zero())
	if err != nil || cmp < 0 {
		return "", ErrSimulationModel
	}
	text, err := value.WireDecimal()
	if err != nil {
		return "", ErrSimulationModel
	}
	parsed, err := model.ParseRate(text)
	if err != nil {
		return "", ErrSimulationModel
	}
	cmp, err = value.Cmp(parsed)
	if err != nil || cmp != 0 {
		return "", ErrSimulationModel
	}
	return text, nil
}
