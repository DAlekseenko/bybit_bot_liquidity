package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadParsesCurrentConfigShape(t *testing.T) {
	path := writeConfig(t, `
# Комментарии должны игнорироваться загрузчиком.
exchange:
  testnet: false
  base_url: https://api.example.test
  websocket_url: wss://stream.example.test/v5/public/linear

symbol: ETHUSDT

strategy:
  oi_growth_percent: 4.5
  volume_multiplier: 2.25
  liquidation_threshold: 150000

risk:
  risk_per_trade_percent: 0.5
  stop_loss_percent: 0.7
  take_profit_percent: 1.4

storage:
  driver: sqlite-test
  dsn: data/test.db

logging:
  level: debug
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Exchange.Testnet {
		t.Fatal("expected testnet=false")
	}
	if cfg.Symbol != "ETHUSDT" {
		t.Fatalf("Symbol = %q", cfg.Symbol)
	}
	if cfg.Strategy.OIGrowthPercent != 4.5 || cfg.Strategy.VolumeMultiplier != 2.25 || cfg.Strategy.LiquidationThreshold != 150000 {
		t.Fatalf("unexpected strategy config: %+v", cfg.Strategy)
	}
	if cfg.Risk.RiskPerTradePercent != 0.5 || cfg.Risk.StopLossPercent != 0.7 || cfg.Risk.TakeProfitPercent != 1.4 {
		t.Fatalf("unexpected risk config: %+v", cfg.Risk)
	}
	if cfg.Storage.Driver != "sqlite-test" || cfg.Storage.DSN != "data/test.db" {
		t.Fatalf("unexpected storage config: %+v", cfg.Storage)
	}
	if cfg.Logging.Level != "debug" {
		t.Fatalf("Logging.Level = %q", cfg.Logging.Level)
	}
}

func TestLoadIgnoresUnknownKeysConsistently(t *testing.T) {
	path := writeConfig(t, `
symbol: BTCUSDT

strategy:
  unknown_strategy_key: not-a-number

risk:
  unknown_risk_key: not-a-number
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	defaults := Default()
	if cfg.Strategy != defaults.Strategy {
		t.Fatalf("Strategy = %+v, want defaults %+v", cfg.Strategy, defaults.Strategy)
	}
	if cfg.Risk != defaults.Risk {
		t.Fatalf("Risk = %+v, want defaults %+v", cfg.Risk, defaults.Risk)
	}
}

func TestLoadRejectsInvalidKnownNumericValue(t *testing.T) {
	path := writeConfig(t, `
symbol: BTCUSDT

strategy:
  oi_growth_percent: not-a-number
`)

	if _, err := Load(path); err == nil {
		t.Fatal("expected error")
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}
