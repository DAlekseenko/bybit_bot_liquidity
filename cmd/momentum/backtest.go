package main

import (
	"bybit_bot_liquidity/internal/momentum/backtest"
	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/data"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

// runBacktest проверяет входы и каждый раз вычисляет полный offline результат.
func runBacktest(ctx context.Context, args []string, stdout io.Writer) error {
	if ctx == nil || stdout == nil {
		return errors.New("invalid backtest invocation")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	flags := flag.NewFlagSet("backtest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "")
	datasetPath := flags.String("dataset", "", "")
	modelPath := flags.String("model", "", "")
	startText := flags.String("start", "", "")
	endText := flags.String("end", "", "")
	output := flags.String("output", "", "")
	approval := flags.String("approval", "", "")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *configPath == "" || *datasetPath == "" || *modelPath == "" || *output == "" || *approval == "" {
		return errors.New("invalid backtest arguments")
	}
	start, err := time.Parse(time.RFC3339Nano, *startText)
	if err != nil {
		return errors.New("invalid backtest range")
	}
	end, err := time.Parse(time.RFC3339Nano, *endText)
	if err != nil || !start.Before(end) {
		return errors.New("invalid backtest range")
	}
	cfg, err := config.Load(*configPath)
	if err != nil || cfg.Values().Mode != "backtest" {
		return errors.New("invalid backtest config")
	}
	source := cfg.Values().DataSource
	dataset, err := backtest.LoadFrozenDataset(ctx, os.DirFS(*datasetPath), data.ManifestSelector{Environment: source.Environment, DatasetID: source.DatasetID, SHA256: source.ManifestSHA256})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	simulation, err := readBacktestModel(*modelPath, cfg, dataset.Manifest)
	if err != nil {
		return err
	}
	input := backtest.BaselineReplayInput{Config: cfg, Model: simulation, Dataset: dataset, Start: start, End: end}
	result, err := backtest.ReplayBaseline(ctx, input)
	if err != nil {
		return err
	}
	bundle, err := backtest.BuildReplayBundle(input, result, *approval)
	if err != nil {
		return err
	}
	if err := backtest.WriteReplayBundle(ctx, *output, bundle); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, result.Identity.RunID); err != nil {
		return errors.New("cannot write backtest result")
	}
	return nil
}
