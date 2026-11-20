package backtest

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// TestPrepareBaselineCycle проверяет funding-cutoff, D-01 и неизменяемый план.
func TestPrepareBaselineCycle(t *testing.T) {
	input := cycleFixture(t, "now")
	before := cloneCycleInput(input)
	got, err := PrepareBaselineCycle(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, input) {
		t.Fatal("input changed")
	}
	assertDecimal(t, got.Cutoff.FundingCashDeltaUSDT, "-30")
	assertDecimal(t, got.Cutoff.WalletUSDT, "8970")
	assertDecimal(t, got.Plan.Snapshot().Equity(), "8970")
	assertDecimal(t, got.Plan.Snapshot().Prices()["BTCUSDT"], "100")
	targets := got.Plan.Targets()
	assertDecimal(t, targets[0].Quantity, "29.86")
	assertDecimal(t, targets[1].Quantity, "29.85")
	assertDecimal(t, targets[2].Quantity, "0")
	deltas := got.Projection.Deltas()
	if len(deltas) != 2 || !deltas[0].ReduceOnly || deltas[1].ReduceOnly || got.Projection.PlanHash() != got.Plan.Hash() || !got.Projection.RequiresReductionConfirmation() {
		t.Fatal("incorrect frozen deltas")
	}
	assertDecimal(t, deltas[0].CandidateQty, "10.14")
	assertDecimal(t, deltas[1].CandidateQty, "19.85")
	values := input.Config.Values()
	_, configHash, configErr := input.Config.Snapshot()
	want := model.PlanMetadata{
		StrategyID: values.StrategyID, RunID: got.CycleID, ProfileID: values.Profile, ModelID: values.ModelID,
		FormulaID: "D-01", ConfigHash: configHash, ScheduledAt: input.At.Round(0).UTC(),
		EquityCutoffAt: input.At.Round(0).UTC(), Revision: 1, PreviousHash: "",
	}
	if configErr != nil || got.Plan.Metadata() != want || len(got.RunID) != 64 || len(got.CycleID) != 64 {
		t.Fatalf("incorrect metadata or identity: %+v, %v", got.Plan.Metadata(), configErr)
	}
	firstBytes, err := model.EncodeTradePlan(got.Plan)
	if err != nil {
		t.Fatal(err)
	}
	input.Positions["BTCUSDT"] = model.LongPosition{Quantity: model.Zero(), CostBasisUSDT: model.Zero()}
	input.Marks["BTCUSDT"] = model.LongMarkObservation{}
	secondBytes, err := model.EncodeTradePlan(got.Plan)
	if err != nil || !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("plan retained mutable input")
	}
}

// TestBaselineIdentityVectors фиксирует независимые wire-векторы идентичности.
func TestBaselineIdentityVectors(t *testing.T) {
	run := baselineIdentity(baselineReplayIdentity{
		Format: "momentum/baseline-replay/v1", ConfigSHA256: strings.Repeat("a", 64),
		DatasetSHA256: strings.Repeat("b", 64), ModelSHA256: strings.Repeat("c", 64),
		Start: "2025-07-07T00:00:00Z", End: "2025-07-21T00:00:00Z",
	})
	cycle := baselineIdentity([]string{"momentum/baseline-cycle/v1", run, "onboarding", "2025-07-07T00:00:00Z"})
	if run != "352b8023bd5f73817927613942c0caeb5655f2b311572f7684faaefcffc7336c" || cycle != "915942dd6e216979f196d8a48e75bbce7dbe3b513a4cd525d781a6b0bf11e794" {
		t.Fatalf("identity vectors: %s %s", run, cycle)
	}
}

// TestPrepareCycleIdentity проверяет порядок, часовой пояс, диапазон и start-policy.
func TestPrepareCycleIdentity(t *testing.T) {
	input := cycleFixture(t, "now")
	first, err := PrepareBaselineCycle(input)
	if err != nil {
		t.Fatal(err)
	}
	input.At = input.At.In(time.FixedZone("west", -25200))
	input.Funding[0], input.Funding[1] = input.Funding[1], input.Funding[0]
	positions, marks := input.Positions, input.Marks
	input.Positions = map[string]model.LongPosition{}
	input.Marks = map[string]model.LongMarkObservation{}
	for _, symbol := range []string{"HYPEUSDT", "ETHUSDT", "BTCUSDT"} {
		input.Positions[symbol] = positions[symbol]
		input.Marks[symbol] = marks[symbol]
	}
	again, err := PrepareBaselineCycle(input)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("unstable repeated cycle")
	}
	input.End = input.End.Add(-time.Hour)
	changed, err := PrepareBaselineCycle(input)
	if err != nil || changed.RunID == first.RunID || changed.CycleID == first.CycleID {
		t.Fatal("range did not affect identity")
	}
	parameters := input.Model.Parameters()
	parameters.FeeRate = amount(t, "0.002")
	input.Model, err = NewSimulationModel(input.Config, input.Dataset.Manifest, parameters)
	if err != nil {
		t.Fatal(err)
	}
	feeChanged, err := PrepareBaselineCycle(input)
	if err != nil || feeChanged.RunID == changed.RunID || feeChanged.Plan.Hash() == changed.Plan.Hash() {
		t.Fatal("model fee did not affect identity")
	}
	weekly := cycleFixture(t, "next_monday")
	if _, err := PrepareBaselineCycle(weekly); !errors.Is(err, ErrBaselineCycle) {
		t.Fatal("next_monday allowed onboarding")
	}
	weekly.Kind = "weekly"
	scheduled, err := PrepareBaselineCycle(weekly)
	if err != nil || scheduled.CycleID == first.CycleID {
		t.Fatal("inclusive Monday failed")
	}
	later := cycleFixture(t, "now")
	later.Kind = "weekly"
	later.At = later.At.AddDate(0, 0, 7)
	later.Funding = nil
	for symbol, mark := range later.Marks {
		mark.ObservedAt = later.At
		later.Marks[symbol] = mark
	}
	if _, err := PrepareBaselineCycle(later); err != nil {
		t.Fatalf("later weekly failed: %v", err)
	}
}

// TestPrepareCycleFailures сохраняет типы ошибок и полностью нулевой результат.
func TestPrepareCycleFailures(t *testing.T) {
	cases := []struct {
		name string
		edit func(*BaselineCycleInput)
		want error
	}{
		{"zero-model", func(i *BaselineCycleInput) { i.Model = SimulationModel{} }, ErrSimulationModel},
		{"wrong-model-binding", func(i *BaselineCycleInput) {
			other := cycleFixture(t, "next_monday")
			i.Model = other.Model
		}, nil},
		{"wrong-dataset", func(i *BaselineCycleInput) { i.Dataset.Manifest.Source.DatasetID = "other" }, ErrSimulationModel},
		{"empty-range", func(i *BaselineCycleInput) { i.End = i.Start }, nil},
		{"outside-range", func(i *BaselineCycleInput) { i.At = i.End }, nil},
		{"before-source", func(i *BaselineCycleInput) { i.Start = i.Dataset.Manifest.Range.Start.Add(-time.Hour) }, nil},
		{"unknown-kind", func(i *BaselineCycleInput) { i.Kind = "reset" }, nil},
		{"double-Monday", func(i *BaselineCycleInput) { i.Kind = "weekly" }, nil},
		{"late-onboarding", func(i *BaselineCycleInput) { i.At = i.At.Add(time.Hour) }, nil},
		{"non-Monday-weekly", func(i *BaselineCycleInput) {
			i.Kind = "weekly"
			i.At = i.At.AddDate(0, 0, 1)
		}, nil},
		{"missing-position", func(i *BaselineCycleInput) { delete(i.Positions, "BTCUSDT") }, ErrInvalidAccountingInput},
		{"future-mark", func(i *BaselineCycleInput) {
			mark := i.Marks["BTCUSDT"]
			mark.ObservedAt = i.At.Add(time.Second)
			i.Marks["BTCUSDT"] = mark
		}, model.ErrInvalidLongValuation},
		{"wrong-funding-time", func(i *BaselineCycleInput) { i.Funding[0].Event.FundingTime = i.At.Add(time.Hour) }, ErrInvalidAccountingInput},
		{"bad-daily-bytes", func(i *BaselineCycleInput) {
			for _, raw := range i.Dataset.Artifacts {
				raw[0] = '!'
				break
			}
		}, ErrReplaySignal},
		{"nonpositive-equity", func(i *BaselineCycleInput) { i.WalletUSDT = model.Zero() }, planner.ReasonNonPositiveEquity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := cycleFixture(t, "now")
			tc.edit(&input)
			before := cloneCycleInput(input)
			got, err := PrepareBaselineCycle(input)
			if !errors.Is(err, ErrBaselineCycle) || !reflect.DeepEqual(got, PreparedBaselineCycle{}) || !reflect.DeepEqual(before, input) {
				t.Fatalf("partial result/input mutation: %+v, %v", got, err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("delegated error lost: %v", err)
			}
		})
	}
}

// cycleFixture создаёт независимые канонические данные и явные численные параметры.
func cycleFixture(t *testing.T, policy string) BaselineCycleInput {
	t.Helper()
	at := time.Date(2025, 7, 7, 0, 0, 0, 0, time.UTC)
	span := data.ManifestRange{Start: at.AddDate(0, 0, -32), End: at.AddDate(0, 0, 14)}
	source := data.ManifestSource{Environment: "mainnet", DatasetID: "cycle-probe", Source: "bybit_v5", Category: "linear"}
	ref := data.ManifestArtifact{Format: "probe/v1", SHA256: strings.Repeat("a", 64)}
	dataset := FrozenDataset{
		Manifest:  data.ManifestInput{Source: source, Range: span, LoadedAt: span.End, ModelAssumptions: ref, MarginEvidence: ref},
		Artifacts: map[string][]byte{},
	}
	symbols := []string{"BTCUSDT", "ETHUSDT", "HYPEUSDT"}
	for _, symbol := range symbols {
		rows := []data.TradeCandleContentRow{}
		for day := -32; day < 14; day++ {
			close := "20"
			if day < -1 || symbol == "HYPEUSDT" {
				close = "10"
			}
			rows = append(rows, data.TradeCandleContentRow{
				OpenTime: at.AddDate(0, 0, day), Open: amount(t, "10"),
				High: amount(t, "100"), Low: amount(t, "1"),
				Close: amount(t, close), Volume: model.Zero(), Confirmed: true,
			})
		}
		endpoint := "https://api.bybit.com/v5/market/kline"
		raw, hash, err := data.EncodeTradeCandleContent(data.TradeCandleContentInput{
			Environment: source.Environment, DatasetID: source.DatasetID, Source: source.Source, Category: source.Category,
			Symbol: symbol, IntervalCode: "D", Endpoint: endpoint, Start: span.Start, End: span.End, Rows: rows,
		})
		if err != nil {
			t.Fatal(err)
		}
		dataset.Artifacts[hash] = raw
		dataset.Manifest.Streams = append(dataset.Manifest.Streams, data.ManifestStream{
			Source: source, Range: span, Kind: "trade", Symbol: symbol, IntervalCode: "D", Endpoint: endpoint,
			Content: data.ManifestArtifact{Format: "momentum/trade-candle-content/v1", SHA256: hash}, Coverage: ref,
		})
	}
	_, digest, err := data.EncodeManifest(dataset.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf(`schema_version: 1
strategy_id: probe
profile_version: v1
model_id: explicit-fixture
mode: backtest
profile: baseline_delta_1x
start_policy: %s
symbols: [BTCUSDT, ETHUSDT, HYPEUSDT]
leverage: 1
auto_add_margin: false
margin_mode: ISOLATED_MARGIN
position_mode: one_way
initial_equity_usdt: '9000'
data_source:
  kind: frozen_dataset
  environment: mainnet
  dataset_id: cycle-probe
  manifest_sha256: %s
`, policy, digest)
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	params := SimulationParameters{
		FeeRate: amount(t, "0.001"), SlippageRate: model.Zero(), MarkMaxAge: time.Hour,
		Rules: map[string]planner.MarketDeltaRules{},
	}
	input := BaselineCycleInput{
		Config: cfg, Dataset: dataset, Start: at, End: span.End, At: at, Kind: "onboarding",
		WalletUSDT: amount(t, "9000"), Positions: map[string]model.LongPosition{}, Marks: map[string]model.LongMarkObservation{},
	}
	for n, symbol := range symbols {
		qty := amount(t, []string{"40", "10", "0"}[n])
		basis, _ := qty.Mul(amount(t, "100"))
		input.Positions[symbol] = model.LongPosition{Quantity: qty, CostBasisUSDT: basis}
		input.Marks[symbol] = model.LongMarkObservation{Symbol: symbol, ObservedAt: at, Price: amount(t, "100")}
		params.Rules[symbol] = planner.MarketDeltaRules{
			QtyStep: amount(t, "0.001"), MinQty: amount(t, "0.001"),
			MaxMarketQty: amount(t, "100"), MinIncreaseNotional: model.Zero(),
		}
		if n < 2 {
			input.Funding = append(input.Funding, model.LongFundingAssessment{
				FundingID: "fund-" + symbol, Symbol: symbol, MarkPrice: amount(t, "100"),
				Event: model.FundingEvent{FundingTime: at, Rate: amount(t, []string{"0.01", "-0.01"}[n])},
			})
		}
	}
	input.Model = constructSimulation(t, cfg, dataset.Manifest, params)
	return input
}

// cloneCycleInput копирует изменяемые коллекции для полной typed-проверки владения.
func cloneCycleInput(input BaselineCycleInput) BaselineCycleInput {
	cloned := input
	cloned.Positions = maps.Clone(input.Positions)
	cloned.Marks = maps.Clone(input.Marks)
	cloned.Funding = slices.Clone(input.Funding)
	cloned.Dataset.Manifest.Streams = slices.Clone(input.Dataset.Manifest.Streams)
	cloned.Dataset.Artifacts = maps.Clone(input.Dataset.Artifacts)
	for hash, payload := range input.Dataset.Artifacts {
		cloned.Dataset.Artifacts[hash] = bytes.Clone(payload)
	}
	cloned.Model.parameters.Rules = maps.Clone(input.Model.parameters.Rules)
	cloned.Model.snapshot = bytes.Clone(input.Model.snapshot)
	return cloned
}
