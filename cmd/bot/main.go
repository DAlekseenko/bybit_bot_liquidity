package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"bybit_bot_liquidity/internal/config"
	"bybit_bot_liquidity/internal/exchange/bybit"
	"bybit_bot_liquidity/internal/execution"
	applogger "bybit_bot_liquidity/internal/logger"
	"bybit_bot_liquidity/internal/marketdata"
	"bybit_bot_liquidity/internal/observability"
	"bybit_bot_liquidity/internal/papertrade"
	"bybit_bot_liquidity/internal/risk"
	"bybit_bot_liquidity/internal/storage"
	"bybit_bot_liquidity/internal/strategy"
)

const (
	readOnlyPollInterval = 30 * time.Second
	websocketRetryDelay  = 5 * time.Second
)

// main собирает зависимости бота и запускает read-only цикл Testnet.
func main() {
	configPath := flag.String("config", "configs/config.yaml", "path to YAML config")
	checkStorage := flag.Bool("check-storage", false, "run SQLite quick_check and exit")
	metricsAddress := flag.String("metrics-address", "", "optional address for the read-only metrics endpoint")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	log := applogger.New(os.Stdout, cfg.Logging.Level)
	log.Info("bot initialized", "symbol", cfg.Symbol, "testnet", cfg.Exchange.Testnet)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	metrics := observability.NewRegistry()

	store, err := storage.Open(cfg.Storage.Driver, cfg.Storage.DSN)
	if err != nil {
		log.Error("failed to open storage", "error", err)
		os.Exit(1)
	}
	defer func(store *storage.Store) {
		err := store.Close()
		if err != nil {
			log.Error("failed to close storage", "error", err)
		}
	}(store)
	if *checkStorage {
		if err := store.Check(ctx); err != nil {
			log.Error("storage check failed", "error", err)
			os.Exit(1)
		}
		log.Info("storage check passed")
		return
	}
	if err := store.Migrate(ctx); err != nil {
		log.Error("failed to migrate storage", "error", err)
		if closeErr := store.Close(); closeErr != nil {
			log.Error("failed to close storage", "error", closeErr)
		}
		os.Exit(1)
	}
	log.Info("storage initialized", "driver", cfg.Storage.Driver)

	var exchangeClient *bybit.Client
	if cfg.Exchange.Credentials.Configured() {
		exchangeClient, err = bybit.NewAuthenticatedClient(
			cfg.Exchange.BaseURL,
			cfg.Exchange.Credentials.APIKey(),
			cfg.Exchange.Credentials.APISecret(),
			nil,
		)
	} else {
		exchangeClient, err = bybit.NewClient(cfg.Exchange.BaseURL, nil)
	}
	if err != nil {
		log.Error("failed to create Bybit client", "error", err)
		return
	}
	reportExecutionReadiness(
		ctx,
		log,
		cfg.Exchange.Credentials.Configured(),
		execution.NewExecutionReadinessPreflight(exchangeClient),
		cfg.Symbol,
		metrics,
	)
	collector := marketdata.NewCollector(exchangeClient, cfg.Symbol, cfg.Strategy.OIWindowMinutes)
	liquidationClient := bybit.NewLiquidationClient(cfg.Exchange.WebsocketURL, nil)
	liquidationWindow := time.Duration(cfg.Strategy.LiquidationWindowMinutes) * time.Minute
	liquidations := marketdata.NewLiquidationWindow(liquidationWindow)
	liquidationEvents := make(chan generationLiquidation, 256)
	var background sync.WaitGroup
	background.Add(2)
	startMetricsServer(ctx, log, *metricsAddress, metrics, &background)
	go func() {
		defer background.Done()
		policy := defaultReconnectPolicy()
		policy.InitialDelay = websocketRetryDelay
		runLiquidationStreamWithPolicy(ctx, log, liquidationClient, cfg.Symbol, liquidationEvents, policy, liquidations.BeginGeneration, liquidations.Reset, nil, metrics)
	}()
	go func() {
		defer background.Done()
		collectGenerationLiquidations(ctx, liquidationEvents, liquidations)
	}()
	signalStrategy := strategy.New(strategy.Config{
		OIGrowthPercent:      cfg.Strategy.OIGrowthPercent,
		VolumeSMAPeriod:      cfg.Strategy.VolumeSMAPeriod,
		VolumeMultiplier:     cfg.Strategy.VolumeMultiplier,
		LiquidationWindow:    liquidationWindow,
		LiquidationThreshold: cfg.Strategy.LiquidationThresholdUSD,
	})
	paper, err := papertrade.New(papertrade.Config{
		VirtualBalanceUSD: cfg.Trading.VirtualBalanceUSD,
		Risk: risk.Config{
			RiskPerTradePercent:          cfg.Risk.RiskPerTradePercent,
			StopLossPercent:              cfg.Risk.StopLossPercent,
			TakeProfitPercent:            cfg.Risk.TakeProfitPercent,
			EstimatedRoundTripFeePercent: cfg.Risk.EstimatedRoundTripFeePercent,
			SlippageAllowancePercent:     cfg.Risk.SlippageAllowancePercent,
		},
		Cooldown:        time.Duration(cfg.Trading.CooldownMinutes) * time.Minute,
		MaxTradesPerDay: cfg.Trading.MaxTradesPerDay,
	})
	if err != nil {
		log.Error("failed to initialize paper trading", "error", err)
		os.Exit(1)
	}
	if err := restorePaperTrade(ctx, log, store, paper, cfg.Symbol); err != nil {
		log.Error("failed to restore paper trade", "error", err)
		os.Exit(1)
	}
	log.Info("paper trading initialized", "virtual_balance_usd", cfg.Trading.VirtualBalanceUSD)

	candleLimit := cfg.Strategy.VolumeSMAPeriod + 2
	runner := newReadOnlyRunner(log, cfg.Symbol, collector, signalStrategy, store, paper, liquidations, candleLimit, metrics)

	runReadOnlyLoop(ctx, log, runner, readOnlyPollInterval)
	background.Wait()
	log.Info("bot stopped")
}
