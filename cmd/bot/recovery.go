package main

import (
	"context"
	"fmt"
	"log/slog"

	"bybit_bot_liquidity/internal/exchange"
	"bybit_bot_liquidity/internal/papertrade"
	"bybit_bot_liquidity/internal/storage"
)

func restorePaperTrade(ctx context.Context, log *slog.Logger, store *storage.Store, engine *papertrade.Engine) error {
	open, err := store.OpenVirtualSignals(ctx)
	if err != nil {
		return err
	}
	if len(open) == 0 {
		return nil
	}
	if len(open) != 1 {
		return fmt.Errorf("refuse paper recovery: found %d open virtual signals", len(open))
	}
	item := open[0]
	plan := papertrade.Plan{
		HypotheticalEntry: item.Plan.HypotheticalEntry,
		StopLoss:          item.Plan.StopLoss,
		TakeProfit:        item.Plan.TakeProfit,
		EstimatedFees:     item.Plan.EstimatedFees,
		EstimatedSlippage: item.Plan.EstimatedSlippage,
		Qty:               item.Plan.Qty,
		SignalCandleAt:    item.Plan.SignalCandleAt,
	}
	switch item.Side {
	case "LONG":
		plan.Side = exchange.SideLong
	case "SHORT":
		plan.Side = exchange.SideShort
	default:
		return fmt.Errorf("refuse paper recovery: unsupported side %q", item.Side)
	}
	if err := engine.Restore(item.SignalID, plan); err != nil {
		return fmt.Errorf("restore paper signal %d: %w", item.SignalID, err)
	}
	log.Info("paper trade restored", "signal_id", item.SignalID)
	return nil
}
