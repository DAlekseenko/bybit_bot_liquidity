package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadParsesCurrentConfigShape(t *testing.T) {
	path := writeConfig(t, `
# Комментарии должны игнорироваться загрузчиком.
exchange:
  testnet: true
  base_url: https://api-testnet.bybit.com
  websocket_url: wss://stream-testnet.bybit.com/v5/public/linear

symbol: ETHUSDT

strategy:
  oi_window_minutes: 15
  oi_growth_percent: 4.5
  volume_sma_period: 30
  volume_multiplier: 2.25
  liquidation_window_minutes: 7
  liquidation_threshold_usd: 150000

risk:
  risk_per_trade_percent: 0.5
  stop_loss_percent: 0.7
  take_profit_percent: 1.4
  estimated_round_trip_fee_percent: 0.12
  slippage_allowance_percent: 0.05

trading:
  dry_run: false
  virtual_balance_usd: 2500
  max_open_positions: 1
  cooldown_minutes: 17
  max_trades_per_day: 25

storage:
  driver: sqlite
  dsn: data/test.db

logging:
  level: debug
`)

	cfg, err := loadTestConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if !cfg.Exchange.Testnet {
		t.Fatal("expected testnet=true")
	}
	if cfg.Symbol != "ETHUSDT" {
		t.Fatalf("Symbol = %q", cfg.Symbol)
	}
	if cfg.Strategy.OIWindowMinutes != 15 ||
		cfg.Strategy.OIGrowthPercent != 4.5 ||
		cfg.Strategy.VolumeSMAPeriod != 30 ||
		cfg.Strategy.VolumeMultiplier != 2.25 ||
		cfg.Strategy.LiquidationWindowMinutes != 7 ||
		cfg.Strategy.LiquidationThresholdUSD != 150000 {
		t.Fatalf("unexpected strategy config: %+v", cfg.Strategy)
	}
	if cfg.Risk.RiskPerTradePercent != 0.5 ||
		cfg.Risk.StopLossPercent != 0.7 ||
		cfg.Risk.TakeProfitPercent != 1.4 ||
		cfg.Risk.EstimatedRoundTripFeePercent != 0.12 ||
		cfg.Risk.SlippageAllowancePercent != 0.05 {
		t.Fatalf("unexpected risk config: %+v", cfg.Risk)
	}
	if cfg.Trading.DryRun ||
		cfg.Trading.VirtualBalanceUSD != 2500 ||
		cfg.Trading.MaxOpenPositions != 1 ||
		cfg.Trading.CooldownMinutes != 17 ||
		cfg.Trading.MaxTradesPerDay != 25 {
		t.Fatalf("unexpected trading config: %+v", cfg.Trading)
	}
	if cfg.Storage.Driver != "sqlite" || cfg.Storage.DSN != "data/test.db" {
		t.Fatalf("unexpected storage config: %+v", cfg.Storage)
	}
	if cfg.Logging.Level != "debug" {
		t.Fatalf("Logging.Level = %q", cfg.Logging.Level)
	}
}

func TestLoadRepositoryConfig(t *testing.T) {
	cfg, err := loadTestConfig(filepath.Join("..", "..", "configs", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Symbol == "" {
		t.Fatal("repository config must define symbol")
	}
}

func TestLoadIgnoresUnknownKeysConsistently(t *testing.T) {
	path := writeConfig(t, `
symbol: BTCUSDT

strategy:
  unknown_strategy_key: not-a-number

risk:
  unknown_risk_key: not-a-number

trading:
  unknown_trading_key: not-a-number
`)

	cfg, err := loadTestConfig(path)
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
	if cfg.Trading != defaults.Trading {
		t.Fatalf("Trading = %+v, want defaults %+v", cfg.Trading, defaults.Trading)
	}
}

func TestLoadRejectsInvalidKnownNumericValue(t *testing.T) {
	path := writeConfig(t, `
symbol: BTCUSDT

strategy:
  oi_growth_percent: not-a-number
`)

	if _, err := loadTestConfig(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadRejectsEmptySymbol(t *testing.T) {
	path := writeConfig(t, `
symbol:
`)

	if _, err := loadTestConfig(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadRejectsWhitespaceSymbol(t *testing.T) {
	cfg := Default()
	cfg.Symbol = "   "

	if err := Validate(cfg); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadRejectsLowercaseSymbol(t *testing.T) {
	path := writeConfig(t, "symbol: btcusdt\n")

	if _, err := loadTestConfig(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadRejectsInvalidKnownIntegerValue(t *testing.T) {
	path := writeConfig(t, `
symbol: BTCUSDT

strategy:
  volume_sma_period: 2.5
`)

	if _, err := loadTestConfig(path); err == nil {
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
		{name: "trading virtual balance infinity", section: "trading", key: "virtual_balance_usd", value: "+Inf"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeConfig(t, "\n"+test.section+":\n  "+test.key+": "+test.value+"\n")
			if _, err := loadTestConfig(path); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLoadRejectsInvalidTradingBoolean(t *testing.T) {
	path := writeConfig(t, `
symbol: BTCUSDT

trading:
  dry_run: disabled
`)

	if _, err := loadTestConfig(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadRejectsDeprecatedStrategyKeys(t *testing.T) {
	path := writeConfig(t, `
symbol: BTCUSDT

strategy:
  liquidation_window: 5m
`)

	if _, err := loadTestConfig(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateRejectsInvalidParameters(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{
			name: "non-finite OI growth",
			change: func(cfg *Config) {
				cfg.Strategy.OIGrowthPercent = math.NaN()
			},
		},
		{
			name: "non-finite volume multiplier",
			change: func(cfg *Config) {
				cfg.Strategy.VolumeMultiplier = math.Inf(1)
			},
		},
		{
			name: "unsupported OI window",
			change: func(cfg *Config) {
				cfg.Strategy.OIWindowMinutes = 10
			},
		},
		{
			name: "zero volume SMA period",
			change: func(cfg *Config) {
				cfg.Strategy.VolumeSMAPeriod = 0
			},
		},
		{
			name: "volume SMA period above Bybit limit",
			change: func(cfg *Config) {
				cfg.Strategy.VolumeSMAPeriod = maxVolumeSMAPeriod + 1
			},
		},
		{
			name: "non-positive liquidation window",
			change: func(cfg *Config) {
				cfg.Strategy.LiquidationWindowMinutes = 0
			},
		},
		{
			name: "liquidation window overflows duration",
			change: func(cfg *Config) {
				minutes := maxDurationMinutes + 1
				cfg.Strategy.LiquidationWindowMinutes = int(minutes)
			},
		},
		{
			name: "negative fee reserve",
			change: func(cfg *Config) {
				cfg.Risk.EstimatedRoundTripFeePercent = -0.01
			},
		},
		{
			name: "negative slippage reserve",
			change: func(cfg *Config) {
				cfg.Risk.SlippageAllowancePercent = -0.01
			},
		},
		{
			name: "non-finite threshold",
			change: func(cfg *Config) {
				cfg.Strategy.LiquidationThresholdUSD = math.NaN()
			},
		},
		{
			name: "non-finite risk per trade",
			change: func(cfg *Config) {
				cfg.Risk.RiskPerTradePercent = math.Inf(1)
			},
		},
		{
			name: "non-finite stop loss",
			change: func(cfg *Config) {
				cfg.Risk.StopLossPercent = math.NaN()
			},
		},
		{
			name: "non-finite take profit",
			change: func(cfg *Config) {
				cfg.Risk.TakeProfitPercent = math.Inf(1)
			},
		},
		{
			name: "multiple open positions",
			change: func(cfg *Config) {
				cfg.Trading.MaxOpenPositions = 2
			},
		},
		{
			name: "non-positive virtual balance",
			change: func(cfg *Config) {
				cfg.Trading.VirtualBalanceUSD = 0
			},
		},
		{
			name: "non-finite virtual balance",
			change: func(cfg *Config) {
				cfg.Trading.VirtualBalanceUSD = math.Inf(1)
			},
		},
		{
			name: "non-positive cooldown",
			change: func(cfg *Config) {
				cfg.Trading.CooldownMinutes = 0
			},
		},
		{
			name: "cooldown overflows duration",
			change: func(cfg *Config) {
				minutes := maxDurationMinutes + 1
				cfg.Trading.CooldownMinutes = int(minutes)
			},
		},
		{
			name: "non-positive daily trade limit",
			change: func(cfg *Config) {
				cfg.Trading.MaxTradesPerDay = 0
			},
		},
		{
			name: "unsupported storage driver",
			change: func(cfg *Config) {
				cfg.Storage.Driver = "postgres"
			},
		},
		{
			name: "empty storage DSN",
			change: func(cfg *Config) {
				cfg.Storage.DSN = " "
			},
		},
		{
			name: "unsupported logging level",
			change: func(cfg *Config) {
				cfg.Logging.Level = "verbose"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.change(&cfg)
			if err := Validate(cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLoadReadsCredentialsOnlyFromEnvironment(t *testing.T) {
	path := writeConfig(t, "symbol: BTCUSDT\n")
	environment := map[string]string{
		BybitAPIKeyEnvironment:    "test-key",
		BybitAPISecretEnvironment: "test-secret",
	}

	cfg, err := load(path, func(name string) (string, bool) {
		value, ok := environment[name]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Exchange.Credentials.Configured() {
		t.Fatal("credentials must be configured")
	}
	if cfg.Exchange.Credentials.APIKey() != "test-key" || cfg.Exchange.Credentials.APISecret() != "test-secret" {
		t.Fatal("credentials do not match environment")
	}
	if got := cfg.Exchange.Credentials.String(); got != "{configured:true}" {
		t.Fatalf("Credentials.String() = %q", got)
	}
	if got := fmt.Sprintf("%#v", cfg.Exchange.Credentials); got != "{configured:true}" {
		t.Fatalf("formatted credentials = %q", got)
	}
}

func TestLoadRejectsPartialCredentials(t *testing.T) {
	path := writeConfig(t, "symbol: BTCUSDT\n")

	_, err := load(path, func(name string) (string, bool) {
		if name == BybitAPIKeyEnvironment {
			return "test-key", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadRejectsCredentialsInFile(t *testing.T) {
	path := writeConfig(t, `
exchange:
  api_key: must-not-be-here
`)

	if _, err := loadTestConfig(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateRejectsUnsafeExchangeConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{
			name: "mainnet flag",
			change: func(cfg *Config) {
				cfg.Exchange.Testnet = false
			},
		},
		{
			name: "mainnet REST host",
			change: func(cfg *Config) {
				cfg.Exchange.BaseURL = "https://api.bybit.com"
			},
		},
		{
			name: "insecure REST scheme",
			change: func(cfg *Config) {
				cfg.Exchange.BaseURL = "http://api-testnet.bybit.com"
			},
		},
		{
			name: "mainnet websocket host",
			change: func(cfg *Config) {
				cfg.Exchange.WebsocketURL = "wss://stream.bybit.com/v5/public/linear"
			},
		},
		{
			name: "unexpected websocket path",
			change: func(cfg *Config) {
				cfg.Exchange.WebsocketURL = "wss://stream-testnet.bybit.com/v5/private"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.change(&cfg)
			if err := Validate(cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func loadTestConfig(path string) (Config, error) {
	return load(path, func(string) (string, bool) {
		return "", false
	})
}
func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}
