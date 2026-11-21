package backtest

import (
	"context"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// ReplayBaseline выполняет новый виртуальный запуск на фиксированных входах.
func ReplayBaseline(ctx context.Context, input BaselineReplayInput) (BaselineReplayResult, error) {
	if ctx == nil {
		return failBaselineReplay(nil)
	}
	if err := ctx.Err(); err != nil {
		return failBaselineReplay(err)
	}
	cfg := input.Config.Values()
	parameters := input.Model.Parameters()
	_, modelHash, err := input.Model.Snapshot()
	if err != nil {
		return failBaselineReplay(err)
	}
	bound, err := NewSimulationModel(input.Config, input.Dataset.Manifest, parameters)
	if err != nil {
		return failBaselineReplay(err)
	}
	_, boundHash, err := bound.Snapshot()
	if err != nil || boundHash != modelHash {
		return failBaselineReplay(err)
	}
	schedule, err := BuildReplaySchedule(input.Dataset, cfg.Symbols, input.Start, input.End)
	if err != nil {
		return failBaselineReplay(err)
	}
	if err := ctx.Err(); err != nil {
		return failBaselineReplay(err)
	}
	_, configHash, err := input.Config.Snapshot()
	if err != nil {
		return failBaselineReplay(err)
	}
	start, end := input.Start.Round(0).UTC(), input.End.Round(0).UTC()
	runID := baselineIdentity(baselineReplayIdentity{
		Format: "momentum/baseline-replay/v1", ConfigSHA256: configHash,
		DatasetSHA256: cfg.DataSource.ManifestSHA256, ModelSHA256: modelHash,
		Start: start.Format(time.RFC3339Nano), End: end.Format(time.RFC3339Nano),
	})
	result := BaselineReplayResult{Identity: BaselineReplayIdentity{
		RunID: runID, ConfigSHA256: configHash, DatasetSHA256: cfg.DataSource.ManifestSHA256,
		ModelSHA256: modelHash, SourceModelAssumptions: input.Dataset.Manifest.ModelAssumptions,
		Start: start, End: end, Symbols: cfg.Symbols, StartPolicy: cfg.StartPolicy,
	}, Points: make([]BaselineReplayPoint, 0, len(schedule))}
	wallet, err := model.ParsePrice(cfg.InitialEquity)
	if err != nil {
		return failBaselineReplay(err)
	}
	positions := make(map[string]model.LongPosition, len(cfg.Symbols))
	for _, symbol := range cfg.Symbols {
		positions[symbol] = model.LongPosition{Quantity: model.Zero(), CostBasisUSDT: model.Zero()}
	}
	accounting := AccountingInput{Symbols: cfg.Symbols, AsOf: schedule[0].Market.At,
		MarkMaxAge: parameters.MarkMaxAge, WalletUSDT: wallet, Positions: positions,
		Marks: replayMarks(schedule[0].Market.Marks),
	}
	result.Initial, err = ProjectAccountingStep(accounting)
	if err != nil {
		return failBaselineReplay(err)
	}
	return replayBaselineTimeline(ctx, input, schedule, result, wallet, positions)
}
