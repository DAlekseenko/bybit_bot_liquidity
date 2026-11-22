package backtest

import (
	"context"
	"errors"
	"time"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"bybit_bot_liquidity/internal/momentum/model"
)

// ErrBaselineReplay обозначает отказ полного исторического воспроизведения.
var ErrBaselineReplay = errors.New("invalid baseline replay")

// BaselineReplayInput задаёт неизменяемые входы нового виртуального запуска.
type BaselineReplayInput struct {
	Config     config.Config
	Model      SimulationModel
	Dataset    FrozenDataset
	Start, End time.Time
}

// BaselineReplayIdentity связывает результат с точными входами.
type BaselineReplayIdentity struct {
	RunID, ConfigSHA256, DatasetSHA256, ModelSHA256 string
	SourceModelAssumptions                          data.ManifestArtifact
	Start, End                                      time.Time
	Symbols                                         []string
	StartPolicy                                     string
}

// BaselineReplayCycle хранит три последовательные фазы одного цикла.
type BaselineReplayCycle struct {
	Prepared PreparedBaselineCycle
	Batch    BaselineFillBatchResult
	PostFill AccountingResult
}

// BaselineReplayPoint содержит ровно один тип результата временной точки.
type BaselineReplayPoint struct {
	At         time.Time
	Terminal   bool
	Accounting *AccountingResult
	Cycle      *BaselineReplayCycle
}

// BaselineReplayResult возвращает полный упорядоченный результат.
type BaselineReplayResult struct {
	Identity BaselineReplayIdentity
	Status   string
	Initial  AccountingResult
	Points   []BaselineReplayPoint
}

// failBaselineReplay сохраняет sentinel и исходную классификацию ошибки.
func failBaselineReplay(err error) (BaselineReplayResult, error) {
	return BaselineReplayResult{}, errors.Join(ErrBaselineReplay, err)
}

// replayBaselineTimeline применяет полную ленту к принадлежащему запуску состоянию.
func replayBaselineTimeline(
	ctx context.Context,
	input BaselineReplayInput,
	schedule []ReplaySchedulePoint,
	result BaselineReplayResult,
	wallet model.Decimal,
	positions map[string]model.LongPosition,
) (BaselineReplayResult, error) {
	cfg, parameters := input.Config.Values(), input.Model.Parameters()
	start, end, runID := input.Start.Round(0).UTC(), input.End.Round(0).UTC(), result.Identity.RunID
	for index, point := range schedule {
		if err := ctx.Err(); err != nil {
			return failBaselineReplay(err)
		}
		marks := replayMarks(point.Market.Marks)
		out := BaselineReplayPoint{At: point.Market.At, Terminal: point.Market.Terminal}
		kind := ""
		if !out.Terminal {
			if cfg.StartPolicy == "now" && index == 0 {
				kind = "onboarding"
			} else if point.NominalWeekly {
				kind = "weekly"
			}
		}
		if kind != "" {
			cycleInput := BaselineCycleInput{Config: input.Config, Model: input.Model,
				Dataset: input.Dataset, Start: start, End: end, At: out.At, Kind: kind,
				WalletUSDT: wallet, Positions: positions, Marks: marks, Funding: point.Funding,
			}
			prepared, err := PrepareBaselineCycle(cycleInput)
			if err != nil {
				return failBaselineReplay(err)
			}
			if prepared.RunID != runID {
				return failBaselineReplay(nil)
			}
			if err := ctx.Err(); err != nil {
				return failBaselineReplay(err)
			}
			batch, err := ApplyZeroSlipBaselineCycle(cycleInput, prepared, point.Market.TradeOpens)
			if err != nil {
				return failBaselineReplay(err)
			}
			if err := ctx.Err(); err != nil {
				return failBaselineReplay(err)
			}
			post, err := ProjectAccountingStep(AccountingInput{Symbols: cfg.Symbols,
				AsOf: out.At, MarkMaxAge: parameters.MarkMaxAge, WalletUSDT: batch.WalletUSDT,
				Positions: batch.Positions, Marks: marks,
			})
			if err != nil {
				return failBaselineReplay(err)
			}
			out.Cycle = &BaselineReplayCycle{Prepared: prepared, Batch: batch, PostFill: post}
			wallet, positions = batch.WalletUSDT, batch.Positions
		} else {
			if out.Terminal && (point.NominalWeekly || len(point.Funding) != 0 || len(point.Market.TradeOpens) != 0) {
				return failBaselineReplay(nil)
			}
			value, err := ProjectAccountingStep(AccountingInput{Symbols: cfg.Symbols,
				AsOf: out.At, MarkMaxAge: parameters.MarkMaxAge, WalletUSDT: wallet,
				Positions: positions, Marks: marks, Funding: point.Funding,
			})
			if err != nil {
				return failBaselineReplay(err)
			}
			out.Accounting = &value
			wallet = value.WalletUSDT
		}
		result.Points = append(result.Points, out)
	}
	if err := ctx.Err(); err != nil {
		return failBaselineReplay(err)
	}
	result.Status = "complete"
	return result, nil
}

// replayMarks создаёт собственный индекс mark-наблюдений временной точки.
func replayMarks(observations []model.LongMarkObservation) map[string]model.LongMarkObservation {
	marks := make(map[string]model.LongMarkObservation, len(observations))
	for _, mark := range observations {
		marks[mark.Symbol] = mark
	}
	return marks
}
