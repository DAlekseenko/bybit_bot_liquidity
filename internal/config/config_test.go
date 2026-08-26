package config

import (
	"math"
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

func TestLoadRejectsEmptySymbol(t *testing.T) {
	path := writeConfig(t, `
symbol:
`)

	if _, err := Load(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadRejectsNonFiniteNumericValue(t *testing.T) {
	tests := []struct {
		name    string
		section string
		key     string
		value   string
	}{
		{name: "strategy NaN", section: "strategy", key: "oi_growth_percent", value: "NaN"},
		{name: "risk positive infinity", section: "risk", key: "risk_per_trade_percent", value: "+Inf"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeConfig(t, "\n"+test.section+":\n  "+test.key+": "+test.value+"\n")
			if _, err := Load(path); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestValidateRejectsNonFiniteNumbers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "strategy OI growth", mutate: func(cfg *Config) { cfg.Strategy.OIGrowthPercent = math.NaN() }},
		{name: "strategy volume", mutate: func(cfg *Config) { cfg.Strategy.VolumeMultiplier = math.Inf(1) }},
		{name: "strategy liquidations", mutate: func(cfg *Config) { cfg.Strategy.LiquidationThreshold = math.NaN() }},
		{name: "risk per trade", mutate: func(cfg *Config) { cfg.Risk.RiskPerTradePercent = math.Inf(1) }},
		{name: "risk stop loss", mutate: func(cfg *Config) { cfg.Risk.StopLossPercent = math.NaN() }},
		{name: "risk take profit", mutate: func(cfg *Config) { cfg.Risk.TakeProfitPercent = math.Inf(1) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.mutate(&cfg)
			if err := Validate(cfg); err == nil {
				t.Fatal("expected error")
			}
		})
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
