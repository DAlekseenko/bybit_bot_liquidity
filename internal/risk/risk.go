package risk

import (
	"errors"

	"bybit_bot_liquidity/internal/exchange"
)

var ErrInvalidInput = errors.New("invalid risk input")

// Config задает фиксированный риск на сделку и уровни выхода.
type Config struct {
	// RiskPerTradePercent - процент баланса, которым бот рискует в сделке.
	RiskPerTradePercent float64
	// StopLossPercent - расстояние stop loss от цены входа.
	StopLossPercent float64
	// TakeProfitPercent - расстояние take profit от цены входа.
	TakeProfitPercent float64
}

// Plan содержит готовые параметры позиции после расчета риска.
type Plan struct {
	// Qty - размер позиции, рассчитанный из риска и расстояния до stop loss.
	Qty float64
	// StopLoss и TakeProfit - цены защитных ордеров.
	StopLoss   float64
	TakeProfit float64
	// RiskUSD - абсолютный риск сделки в USD.
	RiskUSD float64
}

// Manager рассчитывает размер позиции и уровни SL/TP.
type Manager struct {
	cfg Config
}

func New(cfg Config) *Manager {
	return &Manager{cfg: cfg}
}

func (m *Manager) Plan(balance, entryPrice float64, side exchange.Side) (Plan, error) {
	if balance <= 0 || entryPrice <= 0 || m.cfg.RiskPerTradePercent <= 0 || m.cfg.StopLossPercent <= 0 || m.cfg.TakeProfitPercent <= 0 {
		return Plan{}, ErrInvalidInput
	}

	riskUSD := balance * m.cfg.RiskPerTradePercent / 100
	priceRisk := entryPrice * m.cfg.StopLossPercent / 100
	qty := riskUSD / priceRisk

	switch side {
	case exchange.SideLong:
		return Plan{
			Qty:        qty,
			StopLoss:   entryPrice * (1 - m.cfg.StopLossPercent/100),
			TakeProfit: entryPrice * (1 + m.cfg.TakeProfitPercent/100),
			RiskUSD:    riskUSD,
		}, nil
	case exchange.SideShort:
		return Plan{
			Qty:        qty,
			StopLoss:   entryPrice * (1 + m.cfg.StopLossPercent/100),
			TakeProfit: entryPrice * (1 - m.cfg.TakeProfitPercent/100),
			RiskUSD:    riskUSD,
		}, nil
	default:
		return Plan{}, ErrInvalidInput
	}
}
