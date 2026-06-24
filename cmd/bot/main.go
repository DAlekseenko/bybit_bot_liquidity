package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"bybit_bot_liquidity/internal/config"
	applogger "bybit_bot_liquidity/internal/logger"
)

func main() {
	configPath := flag.String("config", "configs/config.yaml", "path to YAML config")
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

	<-ctx.Done()
	log.Info("bot stopped")
}
