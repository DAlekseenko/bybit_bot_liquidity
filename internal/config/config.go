package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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
}

// StrategyConfig описывает числовые пороги торговой стратегии из AGENT.md.
type StrategyConfig struct {
	// OIGrowthPercent задает минимальный рост open interest в процентах.
	OIGrowthPercent float64
	// VolumeMultiplier задает множитель к SMA20 объема текущей свечи.
	VolumeMultiplier float64
	// LiquidationThreshold задает минимальную сумму ликвидаций за 5 минут в USD.
	LiquidationThreshold float64
}

// RiskConfig задает фиксированный риск и расстояния до stop loss / take profit.
type RiskConfig struct {
	// RiskPerTradePercent задает долю баланса, которой бот рискует в одной сделке.
	RiskPerTradePercent float64
	// StopLossPercent задает расстояние stop loss от входа в процентах.
	StopLossPercent float64
	// TakeProfitPercent задает расстояние take profit от входа в процентах.
	TakeProfitPercent float64
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
		if isRootLine && strings.HasSuffix(line, ":") {
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
			OIGrowthPercent:      3,
			VolumeMultiplier:     2,
			LiquidationThreshold: 100000,
		},
		Risk: RiskConfig{
			RiskPerTradePercent: 1,
			StopLossPercent:     0.8,
			TakeProfitPercent:   1.6,
		},
		Storage: StorageConfig{
			Driver: "sqlite",
			DSN:    "data/trades.db",
		},
		Logging: LoggingConfig{Level: "info"},
	}
}

func Validate(cfg Config) error {
	if cfg.Symbol == "" {
		return errors.New("symbol is required")
	}
	if cfg.Strategy.OIGrowthPercent <= 0 {
		return errors.New("strategy.oi_growth_percent must be positive")
	}
	if cfg.Strategy.VolumeMultiplier <= 0 {
		return errors.New("strategy.volume_multiplier must be positive")
	}
	if cfg.Strategy.LiquidationThreshold <= 0 {
		return errors.New("strategy.liquidation_threshold must be positive")
	}
	if cfg.Risk.RiskPerTradePercent <= 0 {
		return errors.New("risk.risk_per_trade_percent must be positive")
	}
	if cfg.Risk.StopLossPercent <= 0 {
		return errors.New("risk.stop_loss_percent must be positive")
	}
	if cfg.Risk.TakeProfitPercent <= 0 {
		return errors.New("risk.take_profit_percent must be positive")
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
		case "oi_growth_percent":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Strategy.OIGrowthPercent = parsed
		case "volume_multiplier":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Strategy.VolumeMultiplier = parsed
		case "liquidation_threshold":
			parsed, err := parseFloatConfig(value, section, key)
			if err != nil {
				return err
			}
			cfg.Strategy.LiquidationThreshold = parsed
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

func parseFloatConfig(value, section, key string) (float64, error) {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s.%s must be a number: %w", section, key, err)
	}

	return parsed, nil
}
