package main

import (
	"context"
	"log/slog"
	"time"

	"bybit_bot_liquidity/internal/exchange"
	"bybit_bot_liquidity/internal/marketdata"
	"bybit_bot_liquidity/internal/papertrade"
	"bybit_bot_liquidity/internal/storage"
	"bybit_bot_liquidity/internal/strategy"
)

type snapshotCollector interface {
	Snapshot(ctx context.Context, candleLimit int, liquidations []exchange.Liquidation) (marketdata.Snapshot, error)
}

type signalEvaluator interface {
	EvaluateDetailed(input strategy.Input) strategy.Evaluation
}

type signalStore interface {
	SaveSignalRecord(ctx context.Context, record storage.SignalRecord) (int64, error)
	CloseVirtualSignal(ctx context.Context, signalID int64, outcome storage.VirtualOutcome) error
}

type liquidationSource interface {
	Snapshot(now time.Time) marketdata.LiquidationSnapshot
	AcquireReadyGeneration(generation uint64, now time.Time) (func(), bool)
}

type readOnlyRunner struct {
	log                  *slog.Logger
	symbol               string
	collector            snapshotCollector
	evaluator            signalEvaluator
	store                signalStore
	paper                *papertrade.Engine
	liquidations         liquidationSource
	candleLimit          int
	now                  func() time.Time
	previousOpenInterest *exchange.OpenInterest
	previousOIGeneration uint64
}

// newReadOnlyRunner связывает зависимости цикла без включения торгового исполнения.
func newReadOnlyRunner(log *slog.Logger, symbol string, collector snapshotCollector, evaluator signalEvaluator, store signalStore, paper *papertrade.Engine, liquidations liquidationSource, candleLimit int) *readOnlyRunner {
	if log == nil {
		log = slog.Default()
	}

	return &readOnlyRunner{
		log:          log,
		symbol:       symbol,
		collector:    collector,
		evaluator:    evaluator,
		store:        store,
		paper:        paper,
		liquidations: liquidations,
		candleLimit:  candleLimit,
		now:          time.Now,
	}
}

// runReadOnlyLoop выполняет snapshots с заданной паузой после завершения запроса.
func runReadOnlyLoop(ctx context.Context, log *slog.Logger, runner *readOnlyRunner, interval time.Duration) {
	if log == nil {
		log = slog.Default()
	}
	if interval <= 0 {
		log.Error("invalid read-only loop interval", "interval", interval)
		return
	}

	select {
	case <-ctx.Done():
		return
	default:
		runner.Tick(ctx)
	}

	for {
		// Schedule the next collection after the previous one finishes. This
		// deliberately avoids catch-up bursts when a network call took longer
		// than the polling interval.
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			runner.log.Debug("running read-only loop")
			runner.Tick(ctx)
		}
	}
}

// Tick загружает рынок и оценивает сигнал только при готовом актуальном окне ликвидаций.
func (r *readOnlyRunner) Tick(ctx context.Context) {
	now := r.now().UTC()
	liquidationSnapshot := r.liquidations.Snapshot(now)
	if r.previousOpenInterest != nil && r.previousOIGeneration != liquidationSnapshot.Generation {
		r.previousOpenInterest = nil
	}
	snapshot, err := r.collector.Snapshot(ctx, r.candleLimit, liquidationSnapshot.Events)
	if err != nil {
		r.log.Error("market snapshot failed", "symbol", r.symbol, "error", err)
		return
	}

	r.advanceVirtualTrade(ctx, snapshot.Candles)
	if !liquidationSnapshot.Ready {
		r.previousOpenInterest = nil
		return
	}
	releaseGeneration, acquired := r.liquidations.AcquireReadyGeneration(liquidationSnapshot.Generation, r.now().UTC())
	if !acquired {
		r.previousOpenInterest = nil
		return
	}
	defer releaseGeneration()

	if r.previousOpenInterest == nil {
		r.setPreviousOpenInterest(snapshot.OpenInterest, liquidationSnapshot.Generation)
		r.log.Info(
			"market snapshot initialized",
			"symbol", r.symbol,
			"candles", len(snapshot.Candles),
			"open_interest", snapshot.OpenInterest.Value,
		)
		return
	}

	if !snapshot.OpenInterest.Time.After(r.previousOpenInterest.Time) {
		if snapshot.OpenInterest.Time.Equal(r.previousOpenInterest.Time) {
			r.setPreviousOpenInterest(snapshot.OpenInterest, liquidationSnapshot.Generation)
			r.log.Debug(
				"open interest window has not advanced",
				"symbol", r.symbol,
				"timestamp", snapshot.OpenInterest.Time,
			)
		} else {
			r.log.Warn(
				"stale open interest snapshot ignored",
				"symbol", r.symbol,
				"previous_timestamp", r.previousOpenInterest.Time,
				"current_timestamp", snapshot.OpenInterest.Time,
			)
		}
		return
	}

	if snapshot.OpenInterest.Value != r.previousOpenInterest.Value {
		r.log.Info(
			"OI changed",
			"symbol", r.symbol,
			"previous", r.previousOpenInterest.Value,
			"current", snapshot.OpenInterest.Value,
		)
	}

	evaluationTime := snapshot.CollectedAt
	if evaluationTime.IsZero() {
		evaluationTime = now
	}
	evaluation := r.evaluator.EvaluateDetailed(strategy.Input{
		Symbol:               r.symbol,
		Candles:              snapshot.Candles,
		PreviousOpenInterest: *r.previousOpenInterest,
		CurrentOpenInterest:  snapshot.OpenInterest,
		Liquidations:         snapshot.Liquidations,
		Now:                  evaluationTime,
	})
	signal := evaluation.Signal

	record := storage.SignalRecord{
		Signal:     signal,
		Indicators: &evaluation.Indicators,
	}
	candle, hasSignalCandle := latestConfirmedCandle(snapshot.Candles)
	if hasSignalCandle {
		signalPrice := candle.Close
		record.SignalPrice = &signalPrice
	}

	var virtualPlan *papertrade.Plan
	if r.paper != nil &&
		hasSignalCandle &&
		(signal.Type == strategy.SignalLong || signal.Type == strategy.SignalShort) {
		plan, err := r.paper.PlanSignal(signal, candle)
		if err != nil {
			record.ExitReason = papertrade.SkipReason(err)
			if record.ExitReason == "" {
				r.log.Error("plan virtual signal failed", "symbol", signal.Symbol, "type", signal.Type, "error", err)
			}
		} else {
			virtualPlan = &plan
			record.Virtual = &storage.VirtualTrade{
				SignalCandleAt:    plan.SignalCandleAt,
				HypotheticalEntry: plan.HypotheticalEntry,
				StopLoss:          plan.StopLoss,
				TakeProfit:        plan.TakeProfit,
				EstimatedFees:     plan.EstimatedFees,
				EstimatedSlippage: plan.EstimatedSlippage,
				Qty:               plan.Qty,
			}
		}
	}

	signalID, err := r.store.SaveSignalRecord(ctx, record)
	if err != nil {
		r.log.Error("save signal failed", "symbol", signal.Symbol, "type", signal.Type, "error", err)
		return
	}
	if virtualPlan != nil {
		if err := r.paper.Activate(signalID, *virtualPlan); err != nil {
			r.log.Error("activate virtual signal failed", "symbol", signal.Symbol, "signal_id", signalID, "error", err)
		} else {
			r.log.Info(
				"virtual signal opened",
				"symbol", signal.Symbol,
				"signal_id", signalID,
				"entry", virtualPlan.HypotheticalEntry,
				"stop_loss", virtualPlan.StopLoss,
				"take_profit", virtualPlan.TakeProfit,
			)
		}
	}
	r.log.Info("signal generated", "symbol", signal.Symbol, "type", signal.Type, "reason", signal.Reason)

	r.setPreviousOpenInterest(snapshot.OpenInterest, liquidationSnapshot.Generation)
}

// advanceVirtualTrade обновляет только аналитическую виртуальную позицию.
func (r *readOnlyRunner) advanceVirtualTrade(ctx context.Context, candles []exchange.Candle) {
	if r.paper == nil {
		return
	}

	outcome := r.paper.Advance(candles)
	if outcome == nil {
		return
	}
	err := r.store.CloseVirtualSignal(ctx, outcome.SignalID, storage.VirtualOutcome{
		ExitPrice:  outcome.ExitPrice,
		ExitReason: outcome.Reason,
		PnL:        outcome.PnL,
		ClosedAt:   outcome.ClosedAt,
	})
	if err != nil {
		r.log.Error("close virtual signal failed", "signal_id", outcome.SignalID, "error", err)
		return
	}

	r.paper.ConfirmClosed()
	r.log.Info(
		"virtual signal closed",
		"signal_id", outcome.SignalID,
		"exit_reason", outcome.Reason,
		"exit_price", outcome.ExitPrice,
		"virtual_pnl", outcome.PnL,
	)
}

// latestConfirmedCandle возвращает наиболее новую подтверждённую свечу.
func latestConfirmedCandle(candles []exchange.Candle) (exchange.Candle, bool) {
	var latest exchange.Candle
	found := false
	for _, candle := range candles {
		if !candle.Confirmed {
			continue
		}
		if !found || candle.OpenTime.After(latest.OpenTime) {
			latest = candle
			found = true
		}
	}
	return latest, found
}

// setPreviousOpenInterest сохраняет базовый OI для следующего шага.
func (r *readOnlyRunner) setPreviousOpenInterest(openInterest exchange.OpenInterest, generation uint64) {
	copied := openInterest
	r.previousOpenInterest = &copied
	r.previousOIGeneration = generation
}
