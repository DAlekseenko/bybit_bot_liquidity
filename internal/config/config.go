package config

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	BybitAPIKeyEnvironment    = "BYBIT_API_KEY"
	BybitAPISecretEnvironment = "BYBIT_API_SECRET"

	testnetRESTHost      = "api-testnet.bybit.com"
	testnetWebSocketHost = "stream-testnet.bybit.com"
	testnetWebSocketPath = "/v5/public/linear"
	maxVolumeSMAPeriod   = 998
	maxDurationMinutes   = int64(time.Duration(1<<63-1) / time.Minute)
)

// Config объединяет все настройки, необходимые для запуска одного экземпляра бота.
type Config struct {
	// Exchange описывает подключение к REST и WebSocket API биржи.
	Exchange ExchangeConfig
	// Symbol задает единственный торговый инструмент для MVP.
	Symbol string
	// Strategy содержит пороги, по которым формируются LONG/SHORT сигналы.
	Strategy StrategyConfig
	// Risk управляет размером позиции и уровнями SL/TP.
	Risk RiskConfig
	// Trading содержит параметры paper-trade и будущего торгового исполнения.
	Trading TradingConfig
	// Storage задает SQL-драйвер и путь к базе сделок.
	Storage StorageConfig
	// Logging задает уровень логирования.
	Logging LoggingConfig
}

// ExchangeConfig хранит адреса и режим работы Bybit API.
type ExchangeConfig struct {
	// Testnet включает торговлю в тестовой среде Bybit.
	Testnet bool
	// BaseURL используется для REST-запросов Bybit V5.
	BaseURL string
	// WebsocketURL используется для публичных потоков, включая ликвидации.
	WebsocketURL string
	// Credentials загружаются только из переменных окружения.
	Credentials Credentials
}

// Credentials хранит API credentials без их вывода через fmt.Stringer.
type Credentials struct {
	apiKey    string
	apiSecret string
}

func (c Credentials) Configured() bool {
	return c.apiKey != "" && c.apiSecret != ""
}

func (c Credentials) APIKey() string {
	return c.apiKey
}

func (c Credentials) APISecret() string {
	return c.apiSecret
}

func (c Credentials) String() string {
	return fmt.Sprintf("{configured:%t}", c.Configured())
}

func (c Credentials) GoString() string {
	return c.String()
}

// StrategyConfig описывает интервалы и пороги торговой стратегии.
type StrategyConfig struct {
	// OIWindowMinutes задает интервал Bybit open interest в минутах.
	OIWindowMinutes int
	// OIGrowthPercent задает минимальный рост open interest в процентах.
	OIGrowthPercent float64
	// VolumeSMAPeriod задает число завершённых свечей для среднего объёма.
	VolumeSMAPeriod int
	// VolumeMultiplier задает множитель к среднему объёму.
	VolumeMultiplier float64
	// LiquidationWindowMinutes задает окно накопления ликвидаций в минутах.
	LiquidationWindowMinutes int
	// LiquidationThresholdUSD задает минимальную сумму ликвидаций за окно в USD.
	LiquidationThresholdUSD float64
}

// RiskConfig задает фиксированный риск, уровни выхода и резервы размера позиции.
type RiskConfig struct {
	// RiskPerTradePercent задает долю баланса, которой бот рискует в одной сделке.
	RiskPerTradePercent float64
	// StopLossPercent задает расстояние stop loss от входа в процентах.
	StopLossPercent float64
	// TakeProfitPercent задает расстояние take profit от входа в процентах.
	TakeProfitPercent float64
	// EstimatedRoundTripFeePercent резервирует комиссию входа и выхода.
	EstimatedRoundTripFeePercent float64
	// SlippageAllowancePercent резервирует ожидаемое проскальзывание.
	SlippageAllowancePercent float64
}

// TradingConfig задает paper-trade модель и лимиты будущего исполнения.
type TradingConfig struct {
	// DryRun запрещает отправку заявок при подключённом торговом runtime.
	DryRun bool
	// VirtualBalanceUSD задает баланс воспроизводимой paper-trade аналитики.
	VirtualBalanceUSD float64
	// MaxOpenPositions ограничивает число одновременно открытых позиций.
	MaxOpenPositions int
	// CooldownMinutes задает паузу после закрытия позиции в минутах.
	CooldownMinutes int
	// MaxTradesPerDay ограничивает число сделок за календарный день.
	MaxTradesPerDay int
}

// StorageConfig описывает подключение к локальному SQL-хранилищу.
type StorageConfig struct {
	// Driver - имя SQL-драйвера, например sqlite.
	Driver string
	// DSN - строка подключения или путь к файлу базы данных.
	DSN string
}

// LoggingConfig хранит параметры slog-логгера.
type LoggingConfig struct {
	// Level задает минимальный уровень логирования: debug, info, warn или error.
	Level string
}

func Load(path string) (Config, error) {
	return load(path, os.LookupEnv)
}

func load(path string, lookupEnv func(string) (string, bool)) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()

	cfg := Default()
	var section string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		rawLine := scanner.Text()
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		isRootLine := !strings.HasPrefix(rawLine, " ") && !strings.HasPrefix(rawLine, "\t")
		if isRootLine && strings.HasSuffix(line, ":") && line != "symbol:" {
			section = strings.TrimSuffix(line, ":")
			continue
		}
		if isRootLine {
			section = ""
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Config{}, fmt.Errorf("invalid config line: %s", line)
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if err := applyValue(&cfg, section, key, value); err != nil {
			return Config{}, err
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, err
	}

	cfg.Exchange.Credentials = credentialsFromEnvironment(lookupEnv)
	return cfg, Validate(cfg)
}

func Default() Config {
	return Config{
		Exchange: ExchangeConfig{
			Testnet:      true,
			BaseURL:      "https://api-testnet.bybit.com",
			WebsocketURL: "wss://stream-testnet.bybit.com/v5/public/linear",
		},
		Symbol: "BTCUSDT",
		Strategy: StrategyConfig{
			OIWindowMinutes:          5,
			OIGrowthPercent:          0.5,
			VolumeSMAPeriod:          20,
			VolumeMultiplier:         2,
			LiquidationWindowMinutes: 5,
			LiquidationThresholdUSD:  100000,
		},
		Risk: RiskConfig{
			RiskPerTradePercent:          0.25,
			StopLossPercent:              0.8,
			TakeProfitPercent:            1.6,
			EstimatedRoundTripFeePercent: 0.11,
			SlippageAllowancePercent:     0.04,
		},
		Trading: TradingConfig{
			DryRun:            true,
			VirtualBalanceUSD: 1000,
			MaxOpenPositions:  1,
			CooldownMinutes:   10,
			MaxTradesPerDay:   10,
		},
		Storage: StorageConfig{
			Driver: "sqlite",
			DSN:    "data/trades.db",
		},
		Logging: LoggingConfig{Level: "info"},
	}
}

func Validate(cfg Config) error {
	if !cfg.Exchange.Testnet {
		return errors.New("exchange.testnet must be true: mainnet is not supported")
	}
	if err := validateEndpoint("exchange.base_url", cfg.Exchange.BaseURL, "https", testnetRESTHost, ""); err != nil {
		return err
	}
	if err := validateEndpoint("exchange.websocket_url", cfg.Exchange.WebsocketURL, "wss", testnetWebSocketHost, testnetWebSocketPath); err != nil {
		return err
	}
	if (cfg.Exchange.Credentials.apiKey == "") != (cfg.Exchange.Credentials.apiSecret == "") {
		return fmt.Errorf("%s and %s must be set together", BybitAPIKeyEnvironment, BybitAPISecretEnvironment)
	}
	if cfg.Symbol == "" {
		return errors.New("symbol is required")
	}
	if strings.TrimSpace(cfg.Symbol) != cfg.Symbol || strings.ToUpper(cfg.Symbol) != cfg.Symbol {
		return errors.New("symbol must be uppercase without surrounding whitespace")
	}
	if !supportedOIWindow(cfg.Strategy.OIWindowMinutes) {
		return errors.New("strategy.oi_window_minutes must be one of 5, 15, 30, 60, 240, 1440")
	}
	if !positiveFinite(cfg.Strategy.OIGrowthPercent) {
		return errors.New("strategy.oi_growth_percent must be positive")
	}
	if cfg.Strategy.VolumeSMAPeriod <= 0 || cfg.Strategy.VolumeSMAPeriod > maxVolumeSMAPeriod {
		return fmt.Errorf("strategy.volume_sma_period must be between 1 and %d", maxVolumeSMAPeriod)
	}
	if !positiveFinite(cfg.Strategy.VolumeMultiplier) {
		return errors.New("strategy.volume_multiplier must be positive")
	}
	if cfg.Strategy.LiquidationWindowMinutes <= 0 {
		return errors.New("strategy.liquidation_window_minutes must be positive")
	}
	if int64(cfg.Strategy.LiquidationWindowMinutes) > maxDurationMinutes {
		return errors.New("strategy.liquidation_window_minutes is too large")
	}
	if !positiveFinite(cfg.Strategy.LiquidationThresholdUSD) {
		return errors.New("strategy.liquidation_threshold_usd must be positive")
	}
	if !positiveFinite(cfg.Risk.RiskPerTradePercent) {
		return errors.New("risk.risk_per_trade_percent must be positive")
	}
	if !positiveFinite(cfg.Risk.StopLossPercent) {
		return errors.New("risk.stop_loss_percent must be positive")
	}
	if !positiveFinite(cfg.Risk.TakeProfitPercent) {
		return errors.New("risk.take_profit_percent must be positive")
	}
	if !nonNegativeFinite(cfg.Risk.EstimatedRoundTripFeePercent) {
		return errors.New("risk.estimated_round_trip_fee_percent must be non-negative")
	}
	if !nonNegativeFinite(cfg.Risk.SlippageAllowancePercent) {
		return errors.New("risk.slippage_allowance_percent must be non-negative")
	}
	if cfg.Trading.MaxOpenPositions != 1 {
		return errors.New("trading.max_open_positions must be 1 for the single-symbol MVP")
	}
	if !positiveFinite(cfg.Trading.VirtualBalanceUSD) {
		return errors.New("trading.virtual_balance_usd must be positive")
	}
	if cfg.Trading.CooldownMinutes <= 0 {
		return errors.New("trading.cooldown_minutes must be positive")
	}
	if int64(cfg.Trading.CooldownMinutes) > maxDurationMinutes {
		return errors.New("trading.cooldown_minutes is too large")
	}
	if cfg.Trading.MaxTradesPerDay <= 0 {
		return errors.New("trading.max_trades_per_day must be positive")
	}
	if cfg.Storage.Driver != "sqlite" {
		return errors.New("storage.driver must be sqlite")
	}
	if strings.TrimSpace(cfg.Storage.DSN) == "" {
		return errors.New("storage.dsn is required")
	}
	switch strings.ToLower(cfg.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("logging.level must be one of debug, info, warn, error")
	}
	return nil
}

func applyValue(cfg *Config, section, key, value string) error {
	switch section {
	case "":
		if key == "symbol" {
			cfg.Symbol = value
		}
	case "exchange":
		switch key {
		case "api_key", "api_secret":
			return fmt.Errorf("exchange.%s is forbidden in config file; use environment variables", key)
		case "testnet":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return err
			}
			cfg.Exchange.Testnet = parsed
		case "base_url":
			cfg.Exchange.BaseURL = value
		case "websocket_url":
			cfg.Exchange.WebsocketURL = value
		}
	case "strategy":
		switch key {
		case "oi_window_minutes":
			parsed, err := parseIntConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Strategy.OIWindowMinutes = parsed
		case "oi_growth_percent":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Strategy.OIGrowthPercent = parsed
		case "volume_sma_period":
			parsed, err := parseIntConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Strategy.VolumeSMAPeriod = parsed
		case "volume_multiplier":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Strategy.VolumeMultiplier = parsed
		case "liquidation_window_minutes":
			parsed, err := parseIntConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Strategy.LiquidationWindowMinutes = parsed
		case "liquidation_threshold_usd":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Strategy.LiquidationThresholdUSD = parsed
		case "liquidation_window", "liquidation_threshold":
			return fmt.Errorf("strategy.%s is deprecated; use minute/USD-suffixed strategy keys", key)
		}
	case "risk":
		switch key {
		case "risk_per_trade_percent":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Risk.RiskPerTradePercent = parsed
		case "stop_loss_percent":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Risk.StopLossPercent = parsed
		case "take_profit_percent":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Risk.TakeProfitPercent = parsed
		case "estimated_round_trip_fee_percent":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Risk.EstimatedRoundTripFeePercent = parsed
		case "slippage_allowance_percent":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Risk.SlippageAllowancePercent = parsed
		}
	case "trading":
		switch key {
		case "dry_run":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("trading.dry_run must be a boolean: %w", err)
			}
			cfg.Trading.DryRun = parsed
		case "virtual_balance_usd":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Trading.VirtualBalanceUSD = parsed
		case "max_open_positions":
			parsed, err := parseIntConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Trading.MaxOpenPositions = parsed
		case "cooldown_minutes":
			parsed, err := parseIntConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Trading.CooldownMinutes = parsed
		case "max_trades_per_day":
			parsed, err := parseIntConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Trading.MaxTradesPerDay = parsed
		}
	case "storage":
		switch key {
		case "driver":
			cfg.Storage.Driver = value
		case "dsn":
			cfg.Storage.DSN = value
		}
	case "logging":
		if key == "level" {
			cfg.Logging.Level = value
		}
	}

	return nil
}

func credentialsFromEnvironment(lookupEnv func(string) (string, bool)) Credentials {
	apiKey, _ := lookupEnv(BybitAPIKeyEnvironment)
	apiSecret, _ := lookupEnv(BybitAPISecretEnvironment)
	return Credentials{
		apiKey:    strings.TrimSpace(apiKey),
		apiSecret: strings.TrimSpace(apiSecret),
	}
}

func validateEndpoint(name, value, expectedScheme, expectedHost, expectedPath string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != expectedScheme || !strings.EqualFold(parsed.Host, expectedHost) {
		return fmt.Errorf("%s must use %s://%s", name, expectedScheme, expectedHost)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain credentials, query or fragment", name)
	}
	path := strings.TrimSuffix(parsed.EscapedPath(), "/")
	if path != expectedPath {
		return fmt.Errorf("%s path must be %q", name, expectedPath)
	}
	return nil
}

func parseFloatConfig(value, section, key string) (float64, error) {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s.%s must be a number: %w", section, key, err)
	}

	return parsed, nil
}

func parseIntConfig(value, section, key string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s.%s must be an integer: %w", section, key, err)
	}

	return parsed, nil
}

func supportedOIWindow(minutes int) bool {
	switch minutes {
	case 5, 15, 30, 60, 240, 1440:
		return true
	default:
		return false
	}
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func nonNegativeFinite(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
