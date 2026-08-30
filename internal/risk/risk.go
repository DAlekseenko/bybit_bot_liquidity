package risk

import (
	"errors"
	"math"

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
	// EstimatedRoundTripFeePercent - резерв комиссии входа и выхода.
	EstimatedRoundTripFeePercent float64
	// SlippageAllowancePercent - резерв ожидаемого проскальзывания.
	SlippageAllowancePercent float64
}

// Plan содержит готовые параметры позиции после расчета риска.
type Plan struct {
	// Qty - размер позиции с учётом stop loss и оценочных издержек.
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
	if !positiveFinite(balance) ||
		!positiveFinite(entryPrice) ||
		!positiveFinite(m.cfg.RiskPerTradePercent) ||
		!positiveFinite(m.cfg.StopLossPercent) ||
		!positiveFinite(m.cfg.TakeProfitPercent) ||
		!nonNegativeFinite(m.cfg.EstimatedRoundTripFeePercent) ||
		!nonNegativeFinite(m.cfg.SlippageAllowancePercent) {
		return Plan{}, ErrInvalidInput
	}

	riskUSD := balance * m.cfg.RiskPerTradePercent / 100
	totalLossPercent := m.cfg.StopLossPercent +
		m.cfg.EstimatedRoundTripFeePercent +
		m.cfg.SlippageAllowancePercent
	if !positiveFinite(totalLossPercent) {
		return Plan{}, ErrInvalidInput
	}
	priceRisk := entryPrice * totalLossPercent / 100
	qty := riskUSD / priceRisk
	plan := Plan{
		Qty:     qty,
		RiskUSD: riskUSD,
	}

	switch side {
	case exchange.SideLong:
		plan.StopLoss = entryPrice * (1 - m.cfg.StopLossPercent/100)
		plan.TakeProfit = entryPrice * (1 + m.cfg.TakeProfitPercent/100)
	case exchange.SideShort:
		plan.StopLoss = entryPrice * (1 + m.cfg.StopLossPercent/100)
		plan.TakeProfit = entryPrice * (1 - m.cfg.TakeProfitPercent/100)
	default:
		return Plan{}, ErrInvalidInput
	}

	if !positiveFinite(plan.Qty) ||
		!positiveFinite(plan.StopLoss) ||
		!positiveFinite(plan.TakeProfit) ||
		!positiveFinite(plan.RiskUSD) {
		return Plan{}, ErrInvalidInput
	}

	return plan, nil
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func nonNegativeFinite(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
