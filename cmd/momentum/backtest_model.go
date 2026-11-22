package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

const maxBacktestModelBytes = 1 << 20

// backtestModelInput описывает точную wire-схему снимка модели.
type backtestModelInput struct {
	Format            string                `json:"format"`
	ConfigSHA256      string                `json:"config_sha256"`
	DatasetSHA256     string                `json:"dataset_manifest_sha256"`
	SourceAssumptions data.ManifestArtifact `json:"source_model_assumptions"`
	FeeRate           string                `json:"one_sided_fee_rate"`
	SlippageRate      string                `json:"slippage_rate"`
	MarkMaxAgeNS      int64                 `json:"mark_max_age_ns"`
	Rules             []backtestRuleInput   `json:"market_rules"`
	EventOrder        string                `json:"event_order_version"`
	Signal            string                `json:"signal_model_revision"`
	Execution         string                `json:"execution_reference_revision"`
	MarkObservation   string                `json:"mark_observation_revision"`
	Funding           string                `json:"funding_mode"`
	FundingCoverage   string                `json:"expected_funding_coverage"`
	Margin            string                `json:"historical_margin"`
	Liquidation       string                `json:"liquidation"`
	Stop              string                `json:"equity_stop"`
}

// backtestRuleInput хранит строковые рыночные ограничения одного символа.
type backtestRuleInput struct {
	Symbol   string `json:"symbol"`
	Step     string `json:"qty_step"`
	Min      string `json:"min_qty"`
	Max      string `json:"max_market_qty"`
	Notional string `json:"min_increase_notional"`
}

// decodeBacktestModel восстанавливает модель и проверяет её полный канонический снимок.
func decodeBacktestModel(raw []byte, cfg config.Config, manifest data.ManifestInput) (backtest.SimulationModel, error) {
	fail := func() (backtest.SimulationModel, error) {
		return backtest.SimulationModel{}, errors.New("invalid backtest model")
	}
	if len(raw) == 0 || len(raw) > maxBacktestModelBytes {
		return fail()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input backtestModelInput
	var extra any
	if decoder.Decode(&input) != nil || decoder.Decode(&extra) != io.EOF {
		return fail()
	}
	fee, err := model.ParseRate(input.FeeRate)
	if err != nil {
		return fail()
	}
	slippage, err := model.ParseRate(input.SlippageRate)
	if err != nil {
		return fail()
	}
	parameters := backtest.SimulationParameters{FeeRate: fee, SlippageRate: slippage, MarkMaxAge: time.Duration(input.MarkMaxAgeNS), Rules: make(map[string]planner.MarketDeltaRules, len(input.Rules))}
	for _, rule := range input.Rules {
		if _, exists := parameters.Rules[rule.Symbol]; exists {
			return fail()
		}
		values := make([]model.Decimal, 4)
		for i, text := range []string{rule.Step, rule.Min, rule.Max, rule.Notional} {
			values[i], err = model.ParseRate(text)
			if err != nil {
				return fail()
			}
		}
		parameters.Rules[rule.Symbol] = planner.MarketDeltaRules{QtyStep: values[0], MinQty: values[1], MaxMarketQty: values[2], MinIncreaseNotional: values[3]}
	}
	result, err := backtest.NewSimulationModel(cfg, manifest, parameters)
	if err != nil {
		return fail()
	}
	canonical, _, err := result.Snapshot()
	if err != nil || !bytes.Equal(raw, canonical) {
		return fail()
	}
	return result, nil
}

// readBacktestModel ограниченно читает снимок модели из файла без внешних обращений.
func readBacktestModel(path string, cfg config.Config, manifest data.ManifestInput) (backtest.SimulationModel, error) {
	file, err := os.Open(path)
	if err != nil {
		return backtest.SimulationModel{}, errors.New("cannot read backtest model")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxBacktestModelBytes+1))
	if err != nil || len(raw) > maxBacktestModelBytes {
		return backtest.SimulationModel{}, errors.New("cannot read backtest model")
	}
	return decodeBacktestModel(raw, cfg, manifest)
}
