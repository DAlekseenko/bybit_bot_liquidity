package backtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
	"bybit_bot_liquidity/internal/momentum/planner"
)

// runnerFixture объединяет независимые синтетические дневные и часовые данные.
func runnerFixture(t *testing.T, policy, variant string) BaselineReplayInput {
	t.Helper()
	dataset, start, end := scheduleFixture(t, 170, "")
	dataset.Manifest.Range.Start = start.AddDate(0, 0, -32)
	for i, stream := range dataset.Manifest.Streams {
		if stream.Kind == "funding" {
			content, err := data.DecodeFundingContent(dataset.Artifacts[stream.Content.SHA256])
			timelineRequire(t, err)
			event := model.FundingEvent{FundingTime: start.Add(168 * time.Hour), Rate: content.Events[0].Rate}
			if variant == "late equity" && stream.Symbol == "BTCUSDT" {
				event.Rate = amount(t, "100")
			}
			content.Events = append(content.Events[:len(content.Events)-1], event, content.Events[len(content.Events)-1])
			raw, hash, err := data.EncodeFundingContent(content)
			timelineRequire(t, err)
			dataset.Artifacts[hash] = raw
			dataset.Manifest.Streams[i].Content.SHA256 = hash
		}
		if stream.Kind != "trade" {
			continue
		}
		content, err := data.DecodeTradeCandleContent(dataset.Artifacts[stream.Content.SHA256])
		timelineRequire(t, err)
		price := amount(t, fmt.Sprint(9+i/2*10))
		for j := range content.Rows {
			current := price
			if variant == "late reserve" && j == 168 {
				current = amount(t, "999")
			}
			content.Rows[j].Open, content.Rows[j].High = current, current
			content.Rows[j].Low, content.Rows[j].Close = current, current
		}
		raw, hash, err := data.EncodeTradeCandleContent(content)
		timelineRequire(t, err)
		dataset.Artifacts[hash] = raw
		dataset.Manifest.Streams[i].Content.SHA256 = hash
	}
	for _, symbol := range tapeSymbols {
		source := dataset.Manifest.Source
		rows := []data.TradeCandleContentRow{}
		for day := -32; day < 8; day++ {
			if variant == "bad warmup" && day < -30 {
				continue
			}
			price := amount(t, fmt.Sprint(day+40))
			if variant == "no trades" || symbol == "BTCUSDT" && day >= 0 {
				price = amount(t, "1")
			}
			rows = append(rows, data.TradeCandleContentRow{OpenTime: start.AddDate(0, 0, day), Open: price, High: price, Low: price, Close: price, Volume: model.Zero(), Confirmed: true})
		}
		raw, hash, err := data.EncodeTradeCandleContent(data.TradeCandleContentInput{
			Environment: source.Environment, DatasetID: source.DatasetID, Source: source.Source, Category: source.Category,
			Symbol: symbol, IntervalCode: "D", Endpoint: "https://api-testnet.bybit.com/v5/market/kline",
			Start: dataset.Manifest.Range.Start, End: start.AddDate(0, 0, 8), Rows: rows,
		})
		timelineRequire(t, err)
		dataset.Manifest.Range.End = start.AddDate(0, 0, 8)
		dataset.Artifacts[hash] = raw
		dataset.Manifest.Streams = append(dataset.Manifest.Streams, data.ManifestStream{
			Source: source, Range: data.ManifestRange{Start: dataset.Manifest.Range.Start, End: dataset.Manifest.Range.End}, Kind: "trade", Symbol: symbol, IntervalCode: "D", Endpoint: "https://api-testnet.bybit.com/v5/market/kline",
			Content: data.ManifestArtifact{Format: "momentum/trade-candle-content/v1", SHA256: hash}, Coverage: dataset.Manifest.MarginEvidence,
		})
	}
	cfg := runnerPolicy(t, dataset, policy)
	parameters := SimulationParameters{FeeRate: model.Zero(), SlippageRate: model.Zero(), MarkMaxAge: time.Hour, Rules: map[string]planner.MarketDeltaRules{}}
	for _, symbol := range tapeSymbols {
		parameters.Rules[symbol] = planner.MarketDeltaRules{QtyStep: amount(t, "0.001"), MinQty: amount(t, "0.001"), MaxMarketQty: amount(t, "1000000"), MinIncreaseNotional: model.Zero()}
	}
	if variant == "late residual" {
		rule := parameters.Rules["ETHUSDT"]
		rule.MinQty = amount(t, "100")
		parameters.Rules["ETHUSDT"] = rule
	}
	if variant == "fee" {
		parameters.FeeRate = amount(t, "0.001")
	}
	return BaselineReplayInput{Config: cfg, Model: constructSimulation(t, cfg, dataset.Manifest, parameters), Dataset: dataset, Start: start, End: end}
}

// runnerPolicy создаёт вариант стандартной конфигурации с заданным стартом.
func runnerPolicy(t *testing.T, dataset FrozenDataset, policy string) config.Config {
	t.Helper()
	values := simulationConfig(t, dataset.Manifest, "backtest", "baseline_delta_1x", "9000").Values()
	values.StartPolicy = policy
	raw, err := json.Marshal(values)
	timelineRequire(t, err)
	cfg, err := config.Parse(raw)
	timelineRequire(t, err)
	return cfg
}

// TestRunnerLifecycle проверяет причинные фазы, единственность funding и terminal.
func TestRunnerLifecycle(t *testing.T) {
	input := runnerFixture(t, "now", "")
	before := cloneCycleInput(BaselineCycleInput{Model: input.Model, Dataset: input.Dataset})
	got, err := ReplayBaseline(context.Background(), input)
	timelineRequire(t, err)
	if got.Status != "complete" || len(got.Points) != 171 || got.Points[0].Cycle == nil || got.Points[168].Cycle == nil {
		t.Fatal("cycles")
	}
	assertDecimal(t, got.Initial.WalletUSDT, "9000")
	for _, valuation := range got.Initial.Valuations {
		assertDecimal(t, valuation.Position.Quantity, "0")
	}
	first := got.Points[0].Cycle
	assertDecimal(t, first.Prepared.Plan.Snapshot().Prices()["BTCUSDT"], "10")
	assertDecimal(t, first.Batch.Postings[0].Fill.ExecutionPrice, "9")
	assertDecimal(t, first.Batch.Positions["BTCUSDT"].Quantity, "300")
	assertDecimal(t, first.Prepared.Cutoff.FundingCashDeltaUSDT, "0")
	assertDecimal(t, first.PostFill.FundingCashDeltaUSDT, "0")
	assertDecimal(t, first.PostFill.WalletUSDT, "9000")
	ordinary := got.Points[1].Accounting
	if ordinary == nil || len(ordinary.FundingPostings) != 3 {
		t.Fatal("ordinary funding")
	}
	assertDecimal(t, ordinary.WalletUSDT, "9030")
	second := got.Points[168].Cycle
	assertDecimal(t, second.Prepared.Cutoff.WalletUSDT, "9060")
	assertDecimal(t, second.Prepared.Cutoff.FundingCashDeltaUSDT, "30")
	assertDecimal(t, second.Prepared.Cutoff.Valuations[0].Position.Quantity, "300")
	assertDecimal(t, second.Prepared.Plan.Targets()[1].Quantity, "160.166")
	assertDecimal(t, second.Batch.Positions["BTCUSDT"].Quantity, "0")
	if second.Batch.Postings[0].Fill.Side != model.FillSell || second.Batch.Postings[1].Fill.Side != model.FillBuy {
		t.Fatal("phase order")
	}
	assertDecimal(t, second.PostFill.FundingCashDeltaUSDT, "0")
	for _, point := range got.Points {
		if (point.Cycle == nil) == (point.Accounting == nil) {
			t.Fatal("invalid union")
		}
		if point.Cycle != nil && point.Cycle.Prepared.RunID != got.Identity.RunID {
			t.Fatal("run identity")
		}
	}
	last := got.Points[len(got.Points)-1]
	if !last.Terminal || last.Cycle != nil || last.Accounting == nil || len(last.Accounting.FundingPostings) != 0 {
		t.Fatal("terminal")
	}
	if !reflect.DeepEqual(before, BaselineCycleInput{Model: input.Model, Dataset: input.Dataset}) {
		t.Fatal("input mutation")
	}
	slices.Reverse(input.Dataset.Manifest.Streams)
	input.Start = input.Start.In(time.FixedZone("east", 10800))
	again, err := ReplayBaseline(context.Background(), input)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal("repeat")
	}
	first.Batch.Positions["BTCUSDT"] = model.LongPosition{}
	first.Batch.Postings[0].Fill.ExecutionID = "BROKEN"
	got.Points[1].Accounting.Valuations[0].Position = model.LongPosition{}
	got.Identity.Symbols[0] = "BROKEN"
	third, err := ReplayBaseline(context.Background(), input)
	if err != nil || !reflect.DeepEqual(third, again) {
		t.Fatal("output alias")
	}
}

// TestRunnerStartPolicies проверяет onboarding, недели и окно без цикла.
func TestRunnerStartPolicies(t *testing.T) {
	for _, policy := range []string{"now", "next_monday"} {
		input := runnerFixture(t, policy, "")
		input.Start = input.Start.Add(time.Hour)
		input.End = input.Start.Add(time.Hour)
		got, err := ReplayBaseline(context.Background(), input)
		timelineRequire(t, err)
		if (got.Points[0].Cycle != nil) != (policy == "now") {
			t.Fatal(policy)
		}
		if got.Points[1].Cycle != nil {
			t.Fatal("terminal cycle")
		}
		input = runnerFixture(t, policy, "")
		input.End = input.Start.Add(time.Hour)
		got, err = ReplayBaseline(context.Background(), input)
		if err != nil || got.Points[0].Cycle == nil {
			t.Fatal("inclusive Monday", err)
		}
	}
}

// runnerContext отменяет запуск на детерминированной проверке контекста.
type runnerContext struct {
	context.Context
	calls, cancelAt int
}

// Err возвращает отмену на заранее выбранной границе чистых операций.
func (c *runnerContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

// TestRunnerFailureAtomicity проверяет binding, late-cancel и нулевой результат.
func TestRunnerFailureAtomicity(t *testing.T) {
	input := runnerFixture(t, "now", "")
	cases := []struct {
		name  string
		edit  func(*BaselineReplayInput)
		ctx   context.Context
		cause error
	}{
		{"nil context", func(*BaselineReplayInput) {}, nil, nil},
		{"cancel first", func(*BaselineReplayInput) {}, &runnerContext{Context: context.Background(), cancelAt: 1}, context.Canceled},
		{"cancel between", func(*BaselineReplayInput) {}, &runnerContext{Context: context.Background(), cancelAt: 8}, context.Canceled},
		{"zero model", func(i *BaselineReplayInput) { i.Model = SimulationModel{} }, context.Background(), nil},
		{"foreign model", func(i *BaselineReplayInput) { i.Config = runnerPolicy(t, i.Dataset, "next_monday") }, context.Background(), nil},
		{"bad warmup", func(*BaselineReplayInput) {}, context.Background(), ErrReplaySignal},
		{"bad range", func(i *BaselineReplayInput) { i.End = i.Start }, context.Background(), nil},
		{"bad marks", func(i *BaselineReplayInput) {
			i.Dataset.Artifacts[i.Dataset.Manifest.Streams[0].Content.SHA256][0] = '!'
		}, context.Background(), nil},
		{"late reserve", func(*BaselineReplayInput) {}, context.Background(), ErrZeroSlipBaselineReserve},
		{"late residual", func(*BaselineReplayInput) {}, context.Background(), ErrBaselineFillResidual},
		{"late equity", func(*BaselineReplayInput) {}, context.Background(), planner.ReasonNonPositiveEquity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fresh := runnerFixture(t, "now", tc.name)
			tc.edit(&fresh)
			before := cloneCycleInput(BaselineCycleInput{Model: fresh.Model, Dataset: fresh.Dataset})
			got, err := ReplayBaseline(tc.ctx, fresh)
			if !errors.Is(err, ErrBaselineReplay) || tc.cause != nil && !errors.Is(err, tc.cause) || !reflect.DeepEqual(got, BaselineReplayResult{}) || !reflect.DeepEqual(before, BaselineCycleInput{Model: fresh.Model, Dataset: fresh.Dataset}) {
				t.Fatal("partial", err)
			}
		})
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	expiredResult, expiredError := ReplayBaseline(expired, input)
	if !errors.Is(expiredError, context.DeadlineExceeded) || !reflect.DeepEqual(expiredResult, BaselineReplayResult{}) {
		t.Fatal("deadline classification")
	}
	contextProbe := &runnerContext{Context: context.Background(), cancelAt: 10000}
	_, err := ReplayBaseline(contextProbe, input)
	timelineRequire(t, err)
	final := &runnerContext{Context: context.Background(), cancelAt: contextProbe.calls}
	got, err := ReplayBaseline(final, input)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, BaselineReplayResult{}) {
		t.Fatal("final cancellation")
	}
}

// TestRunnerFeeAndNoTrade проверяет единственную fee и полный no-op replay.
func TestRunnerFeeAndNoTrade(t *testing.T) {
	for _, variant := range []string{"fee", "no trades"} {
		got, err := ReplayBaseline(context.Background(), runnerFixture(t, "now", variant))
		if err != nil || got.Status != "complete" {
			t.Fatal(variant, err)
		}
		first := got.Points[0].Cycle
		if variant == "fee" {
			assertDecimal(t, first.Batch.FeesUSDT, "8.433109")
			assertDecimal(t, first.PostFill.WalletUSDT, "8991.566891")
		} else if len(first.Batch.Postings) != 0 || len(got.Points[168].Cycle.Batch.Postings) != 0 {
			t.Fatal("no-op fills")
		}
	}
}
