package backtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// simulationFixture создаёт строгие конфигурацию, manifest и параметры для unit-тестов.
func simulationFixture(t *testing.T) (config.Config, data.ManifestInput, SimulationParameters) {
	t.Helper()
	dataset := newDatasetFixture(t).manifest
	parameters := SimulationParameters{
		FeeRate: model.Zero(), SlippageRate: mustRate(t, "0.001"),
		MarkMaxAge: 3*time.Hour + 17*time.Second,
		Rules: map[string]planner.MarketDeltaRules{
			"BTCUSDT":  simulationRuleFixture(t, "0.1", "0.25", "1.05", "5"),
			"ETHUSDT":  simulationRuleFixture(t, "0.01", "0.02", "2.05", "0"),
			"HYPEUSDT": simulationRuleFixture(t, "1", "2", "7", "10"),
		},
	}
	return simulationConfig(t, dataset, "backtest", "baseline_delta_1x", "10000"), dataset, parameters
}

// simulationRuleFixture разбирает явные значения тестового правила.
func simulationRuleFixture(t *testing.T, step, min, max, notional string) planner.MarketDeltaRules {
	t.Helper()
	return planner.MarketDeltaRules{
		QtyStep: mustRate(t, step), MinQty: mustRate(t, min),
		MaxMarketQty: mustRate(t, max), MinIncreaseNotional: mustRate(t, notional),
	}
}

// simulationConfig создаёт валидный профиль с manifest и явной equity.
func simulationConfig(t *testing.T, d data.ManifestInput, mode, profile, equity string) config.Config {
	t.Helper()
	_, hash, err := data.EncodeManifest(d)
	if err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf("  kind: frozen_dataset\n  environment: %s\n  dataset_id: %s\n  manifest_sha256: %s\n", d.Source.Environment, d.Source.DatasetID, hash)
	if mode == "paper" {
		source = "  kind: bybit_testnet_public\n  environment: testnet\n"
	}
	yaml := fmt.Sprintf(`schema_version: 1
strategy_id: momentum-v1
profile_version: v1
model_id: roc30
mode: %s
profile: %s
start_policy: now
symbols: [HYPEUSDT, BTCUSDT, ETHUSDT]
leverage: 1
margin_mode: ISOLATED_MARGIN
auto_add_margin: false
position_mode: one_way
initial_equity_usdt: '%s'
data_source:
%s`, mode, profile, equity, source)
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// constructSimulation строит модель и прекращает тест при неожиданной ошибке.
func constructSimulation(t *testing.T, c config.Config, d data.ManifestInput, p SimulationParameters) SimulationModel {
	t.Helper()
	m, err := NewSimulationModel(c, d, p)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestSimulationModelSnapshot проверяет поля, хеш, неизменяемые копии и детерминизм.
func TestSimulationModelSnapshot(t *testing.T) {
	c, d, p := simulationFixture(t)
	configBefore, manifestBefore := c.Values(), d
	manifestBefore.Streams = append([]data.ManifestStream(nil), d.Streams...)
	m := constructSimulation(t, c, d, p)
	a, ah, err := m.Snapshot()
	if err != nil || ah != fmt.Sprintf("%x", sha256.Sum256(a)) {
		t.Fatalf("snapshot or hash mismatch: %v", err)
	}
	_, configHash, _ := c.Snapshot()
	wantWire := simulationWire{
		Format: "momentum/simulation-model/v1", ConfigSHA256: configHash,
		DatasetManifestSHA256:  c.Values().DataSource.ManifestSHA256,
		SourceModelAssumptions: d.ModelAssumptions, OneSidedFeeRate: "0", SlippageRate: "0.001",
		MarkMaxAgeNS: int64(p.MarkMaxAge), EventOrderVersion: "D-07/v1",
		SignalModelRevision: "daily-roc30-closed/v1", ExecutionReferenceRevision: "next-trade-open/v1",
		MarkObservationRevision: "previous-hour-close-at-boundary/v1", FundingMode: "observed-events",
		ExpectedFundingCoverage: "unknown", HistoricalMargin: "unavailable",
		Liquidation: "unavailable", EquityStop: "disabled",
		MarketRules: []simulationRule{
			{"BTCUSDT", "0.1", "0.25", "1.05", "5"},
			{"ETHUSDT", "0.01", "0.02", "2.05", "0"},
			{"HYPEUSDT", "1", "2", "7", "10"},
		},
	}
	wantBytes, _ := json.Marshal(wantWire)
	if !bytes.Equal(a, wantBytes) {
		t.Fatal("exact canonical wire fields, numeric rules or order differ")
	}
	second := constructSimulation(t, c, d, p)
	b, bh, _ := second.Snapshot()
	if !bytes.Equal(a, b) || ah != bh {
		t.Fatal("snapshot is not deterministic")
	}
	saved, inputs := m.Parameters(), m.Parameters()
	gotParams, _ := json.Marshal(inputs)
	wantParams, _ := json.Marshal(p)
	if !bytes.Equal(gotParams, wantParams) {
		t.Fatal("returned parameters differ from input")
	}
	for _, rule := range inputs.Rules {
		if _, err := planner.BaselineQuantity(mustRate(t, "1000"), mustRate(t, "100"), model.Zero(), inputs.FeeRate, rule.QtyStep); err != nil {
			t.Fatalf("parameters cannot feed sizing consumer: %v", err)
		}
	}
	inputs.SlippageRate = model.Zero()
	constructSimulation(t, c, d, inputs)
	inputs.Rules["BTCUSDT"] = planner.MarketDeltaRules{}
	a[0] ^= 1
	copied, sameHash, _ := m.Snapshot()
	if sameHash != ah || !bytes.Equal(copied, b) {
		t.Fatal("returned data aliases model state")
	}
	p.Rules["BTCUSDT"] = planner.MarketDeltaRules{}
	if !reflect.DeepEqual(m.Parameters(), saved) {
		t.Fatal("parameters changed through caller map")
	}
	if !reflect.DeepEqual(configBefore, c.Values()) || !reflect.DeepEqual(manifestBefore, d) {
		t.Fatal("constructor changed config or manifest")
	}
}

// TestSimulationModelCanonicalAndSensitive проверяет нормализацию и hash commitments.
func TestSimulationModelCanonicalAndSensitive(t *testing.T) {
	c, d, p := simulationFixture(t)
	base := constructSimulation(t, c, d, p)
	_, hash, _ := base.Snapshot()
	reversed := p
	reversed.Rules = map[string]planner.MarketDeltaRules{}
	for _, symbol := range []string{"HYPEUSDT", "ETHUSDT", "BTCUSDT"} {
		reversed.Rules[symbol] = p.Rules[symbol]
	}
	if _, h, _ := constructSimulation(t, c, d, reversed).Snapshot(); h != hash {
		t.Fatal("map order changed identity")
	}
	reversed.FeeRate = mustRate(t, "0.000")
	if _, h, _ := constructSimulation(t, c, d, reversed).Snapshot(); h != hash {
		t.Fatal("decimal spelling changed identity")
	}
	for field, value := range []string{"0.002", "0.002", "", "0.2", "0.3", "1.15", "6"} {
		q := p
		q.Rules = maps.Clone(p.Rules)
		if field == 2 {
			q.MarkMaxAge++
		} else {
			setSimulationField(&q, field, mustRate(t, value))
		}
		got := constructSimulation(t, c, d, q)
		_, h, _ := got.Snapshot()
		gotParams, _ := json.Marshal(got.Parameters())
		wantParams, _ := json.Marshal(q)
		if h == hash || !bytes.Equal(gotParams, wantParams) {
			t.Fatalf("field %d did not affect identity or changed values", field)
		}
	}
	cfg := simulationConfig(t, d, "backtest", "baseline_delta_1x", "10001")
	if _, h, _ := constructSimulation(t, cfg, d, p).Snapshot(); h == hash {
		t.Fatal("config identity did not affect hash")
	}
	for _, change := range []func(*data.ManifestInput){
		func(d *data.ManifestInput) {
			d.Source.DatasetID = "frozen-2"
			d.Streams[0].Source = d.Source
		},
		func(d *data.ManifestInput) { d.ModelAssumptions.SHA256 = strings.Repeat("e", 64) },
	} {
		changed := d
		changed.Streams = append([]data.ManifestStream(nil), d.Streams...)
		change(&changed)
		cc := simulationConfig(t, changed, "backtest", "baseline_delta_1x", "10000")
		if _, h, _ := constructSimulation(t, cc, changed, p).Snapshot(); h == hash {
			t.Fatal("dataset pin did not affect hash")
		}
	}
}

// setSimulationField меняет ровно одно численное поле независимого тестового параметра.
func setSimulationField(p *SimulationParameters, field int, value model.Decimal) {
	rule := p.Rules["BTCUSDT"]
	fields := []*model.Decimal{&p.FeeRate, &p.SlippageRate, nil, &rule.QtyStep, &rule.MinQty, &rule.MaxMarketQty, &rule.MinIncreaseNotional}
	*fields[field] = value
	p.Rules["BTCUSDT"] = rule
}

// mustRate разбирает десятичный тестовый литерал.
func mustRate(t *testing.T, s string) model.Decimal {
	t.Helper()
	v, e := model.ParseRate(s)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

// TestSimulationModelRejectsInvalidInputs проверяет fail-closed guards и безопасные ошибки.
func TestSimulationModelRejectsInvalidInputs(t *testing.T) {
	c, d, p := simulationFixture(t)
	assertBad := func(c config.Config, d data.ManifestInput, p SimulationParameters) {
		t.Helper()
		m, e := NewSimulationModel(c, d, p)
		if !errors.Is(e, ErrSimulationModel) || !reflect.DeepEqual(m, SimulationModel{}) || strings.Contains(e.Error(), "canary") {
			t.Fatalf("accepted or partial model: %v", e)
		}
	}
	for _, invalid := range []config.Config{
		{}, simulationConfig(t, d, "paper", "baseline_delta_1x", "10000"),
		simulationConfig(t, d, "backtest", "isolated_weekly_reset", "10000"),
	} {
		assertBad(invalid, d, p)
	}
	third, _ := mustRate(t, "1").Quo(mustRate(t, "3"))
	for _, field := range []int{0, 1} {
		for _, value := range []model.Decimal{{}, mustRate(t, "-1"), third} {
			q := p
			q.Rules = maps.Clone(p.Rules)
			setSimulationField(&q, field, value)
			assertBad(c, d, q)
		}
	}
	for _, age := range []time.Duration{0, -time.Second} {
		q := p
		q.MarkMaxAge = age
		assertBad(c, d, q)
	}
	beyond, _ := mustRate(t, "1").Quo(mustRate(t, "36893488147419103232"))
	for _, field := range []string{"step", "min", "max", "notional"} {
		values := []model.Decimal{{}, model.Zero(), mustRate(t, "-1"), third, beyond}
		if field == "notional" {
			values = []model.Decimal{{}, mustRate(t, "-1"), third, beyond}
		}
		if field == "min" {
			values = append(values, mustRate(t, "1.1"))
		}
		for _, value := range values {
			q := p
			q.Rules = maps.Clone(p.Rules)
			index := map[string]int{"step": 3, "min": 4, "max": 5, "notional": 6}[field]
			setSimulationField(&q, index, value)
			assertBad(c, d, q)
		}
	}
	for _, kind := range []string{"missing", "extra", "replaced"} {
		bad := p
		bad.Rules = maps.Clone(p.Rules)
		if kind != "missing" {
			bad.Rules["XUSDT"] = bad.Rules["BTCUSDT"]
		}
		if kind != "extra" {
			delete(bad.Rules, "BTCUSDT")
		}
		assertBad(c, d, bad)
	}
	for _, change := range []func(*data.ManifestInput){
		func(d *data.ManifestInput) { d.ModelAssumptions.SHA256 = "canary-payload" },
		func(d *data.ManifestInput) { d.Source.DatasetID = "other"; d.Streams[0].Source = d.Source },
		func(d *data.ManifestInput) { d.ModelAssumptions.SHA256 = strings.Repeat("e", 64) },
		func(d *data.ManifestInput) {
			d.Source.Environment = "mainnet"
			d.Streams[0].Source = d.Source
			d.Streams[0].Endpoint = "https://api.bybit.com/v5/market/funding/history"
		},
	} {
		badData := d
		badData.Streams = append([]data.ManifestStream(nil), d.Streams...)
		change(&badData)
		assertBad(c, badData, p)
	}
	if b, h, err := (SimulationModel{}).Snapshot(); !errors.Is(err, ErrSimulationModel) || b != nil || h != "" {
		t.Fatal("zero model accepted")
	}
}
