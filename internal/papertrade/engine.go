package papertrade

import (
	"errors"
	"math"
	"sort"
	"time"

	"bybit_bot_liquidity/internal/exchange"
	"bybit_bot_liquidity/internal/risk"
	"bybit_bot_liquidity/internal/strategy"
)

var (
	ErrInvalidConfig     = errors.New("invalid paper trade config")
	ErrInvalidSignal     = errors.New("invalid paper trade signal")
	ErrPositionLimit     = errors.New("paper trade position limit reached")
	ErrCooldownActive    = errors.New("paper trade cooldown is active")
	ErrDailyLimitReached = errors.New("paper trade daily limit reached")
)

const (
	ExitStopLoss            = "STOP_LOSS"
	ExitTakeProfit          = "TAKE_PROFIT"
	SkipPositionLimit       = "SKIPPED_POSITION_LIMIT"
	SkipCooldown            = "SKIPPED_COOLDOWN"
	SkipDailyLimit          = "SKIPPED_DAILY_LIMIT"
	oneMinuteCandleDuration = time.Minute
)

// Config задает воспроизводимую модель виртуального исполнения.
type Config struct {
	VirtualBalanceUSD float64
	Risk              risk.Config
	Cooldown          time.Duration
	MaxTradesPerDay   int
}

// Plan фиксирует расчётные параметры виртуальной позиции на момент сигнала.
type Plan struct {
	Side              exchange.Side
	SignalPrice       float64
	HypotheticalEntry float64
	StopLoss          float64
	TakeProfit        float64
	EstimatedFees     float64
	EstimatedSlippage float64
	Qty               float64
	SignalCandleAt    time.Time
}

// Outcome описывает виртуальное закрытие по одной из последующих свечей.
type Outcome struct {
	SignalID  int64
	ExitPrice float64
	Reason    string
	PnL       float64
	ClosedAt  time.Time
}

type activeTrade struct {
	signalID      int64
	plan          Plan
	lastProcessed time.Time
}

// Engine держит не более одной виртуальной позиции в памяти процесса.
type Engine struct {
	cfg         Config
	riskManager *risk.Manager
	active      *activeTrade
	pending     *Outcome
	lastClosed  time.Time
	tradeDay    string
	tradesToday int
}

// New создаёт движок виртуальной торговли с проверенной конфигурацией.
func New(cfg Config) (*Engine, error) {
	if !positiveFinite(cfg.VirtualBalanceUSD) ||
		cfg.Cooldown <= 0 ||
		cfg.MaxTradesPerDay <= 0 ||
		!nonNegativeFinite(cfg.Risk.EstimatedRoundTripFeePercent) ||
		!nonNegativeFinite(cfg.Risk.SlippageAllowancePercent) {
		return nil, ErrInvalidConfig
	}

	return &Engine{
		cfg:         cfg,
		riskManager: risk.New(cfg.Risk),
	}, nil
}

// PlanSignal рассчитывает вход и применяет in-memory position/cooldown/day gates.
func (e *Engine) PlanSignal(signal strategy.Signal, candle exchange.Candle) (Plan, error) {
	side, err := signalSide(signal.Type)
	if err != nil {
		return Plan{}, err
	}
	if !candle.Confirmed || !positiveFinite(candle.Close) || candle.OpenTime.IsZero() {
		return Plan{}, ErrInvalidSignal
	}

	signalAt := candle.OpenTime.UTC().Add(oneMinuteCandleDuration)
	if e.active != nil {
		return Plan{}, ErrPositionLimit
	}
	if !e.lastClosed.IsZero() && signalAt.Sub(e.lastClosed) < e.cfg.Cooldown {
		return Plan{}, ErrCooldownActive
	}

	day := signalAt.Format("2006-01-02")
	tradesToday := e.tradesToday
	if day != e.tradeDay {
		tradesToday = 0
	}
	if tradesToday >= e.cfg.MaxTradesPerDay {
		return Plan{}, ErrDailyLimitReached
	}

	entry := candle.Close
	switch side {
	case exchange.SideLong:
		entry *= 1 + e.cfg.Risk.SlippageAllowancePercent/100
	case exchange.SideShort:
		entry *= 1 - e.cfg.Risk.SlippageAllowancePercent/100
	}
	if !positiveFinite(entry) {
		return Plan{}, ErrInvalidSignal
	}

	riskPlan, err := e.riskManager.Plan(e.cfg.VirtualBalanceUSD, entry, side)
	if err != nil {
		return Plan{}, err
	}
	estimatedFees := riskPlan.Qty * entry * e.cfg.Risk.EstimatedRoundTripFeePercent / 100
	estimatedSlippage := riskPlan.Qty * math.Abs(entry-candle.Close)

	return Plan{
		Side:              side,
		SignalPrice:       candle.Close,
		HypotheticalEntry: entry,
		StopLoss:          riskPlan.StopLoss,
		TakeProfit:        riskPlan.TakeProfit,
		EstimatedFees:     estimatedFees,
		EstimatedSlippage: estimatedSlippage,
		Qty:               riskPlan.Qty,
		SignalCandleAt:    candle.OpenTime.UTC(),
	}, nil
}

// Activate связывает сохранённую строку signals с рассчитанной позицией.
func (e *Engine) Activate(signalID int64, plan Plan) error {
	if signalID <= 0 || e.active != nil {
		return ErrInvalidSignal
	}

	day := plan.SignalCandleAt.Add(oneMinuteCandleDuration).Format("2006-01-02")
	if day != e.tradeDay {
		e.tradeDay = day
		e.tradesToday = 0
	}
	e.tradesToday++
	e.active = &activeTrade{
		signalID: signalID,
		plan:     plan,
	}
	return nil
}

// Restore восстанавливает одну проверенную виртуальную позицию после перезапуска.
func (e *Engine) Restore(signalID int64, plan Plan) error {
	invalidLevels := (plan.Side == exchange.SideLong && !(plan.StopLoss < plan.HypotheticalEntry && plan.HypotheticalEntry < plan.TakeProfit)) ||
		(plan.Side == exchange.SideShort && !(plan.TakeProfit < plan.HypotheticalEntry && plan.HypotheticalEntry < plan.StopLoss))
	if signalID <= 0 || e.active != nil || e.pending != nil || !e.lastClosed.IsZero() || e.tradeDay != "" || e.tradesToday != 0 ||
		(plan.Side != exchange.SideLong && plan.Side != exchange.SideShort) || plan.SignalCandleAt.IsZero() ||
		!positiveFinite(plan.HypotheticalEntry) || !positiveFinite(plan.StopLoss) ||
		!positiveFinite(plan.TakeProfit) || !positiveFinite(plan.Qty) ||
		!nonNegativeFinite(plan.EstimatedFees) || !nonNegativeFinite(plan.EstimatedSlippage) || invalidLevels {
		return ErrInvalidSignal
	}
	e.active = &activeTrade{signalID: signalID, plan: plan}
	return nil
}

// Advance проверяет новые подтверждённые свечи. Pending outcome повторяется до
// ConfirmClosed, чтобы ошибка SQLite не теряла виртуальное закрытие.
func (e *Engine) Advance(candles []exchange.Candle) *Outcome {
	if e.pending != nil {
		copied := *e.pending
		return &copied
	}
	if e.active == nil {
		return nil
	}

	ordered := append([]exchange.Candle(nil), candles...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].OpenTime.Before(ordered[j].OpenTime)
	})

	for _, candle := range ordered {
		if !candle.Confirmed ||
			!candle.OpenTime.After(e.active.plan.SignalCandleAt) ||
			!candle.OpenTime.After(e.active.lastProcessed) {
			continue
		}
		e.active.lastProcessed = candle.OpenTime
		if !positiveFinite(candle.High) || !positiveFinite(candle.Low) {
			continue
		}

		exitPrice, reason, closed := exitForCandle(e.active.plan, candle)
		if !closed {
			continue
		}

		grossPnL := (exitPrice - e.active.plan.HypotheticalEntry) * e.active.plan.Qty
		if e.active.plan.Side == exchange.SideShort {
			grossPnL = (e.active.plan.HypotheticalEntry - exitPrice) * e.active.plan.Qty
		}
		e.pending = &Outcome{
			SignalID:  e.active.signalID,
			ExitPrice: exitPrice,
			Reason:    reason,
			PnL:       grossPnL - e.active.plan.EstimatedFees,
			ClosedAt:  candle.OpenTime.UTC().Add(oneMinuteCandleDuration),
		}
		copied := *e.pending
		return &copied
	}
	return nil
}

// ConfirmClosed подтверждает сохранённое закрытие виртуальной позиции.
func (e *Engine) ConfirmClosed() {
	if e.pending == nil {
		return
	}
	e.lastClosed = e.pending.ClosedAt
	e.pending = nil
	e.active = nil
}

// HasActiveTrade сообщает, хранит ли движок активную виртуальную позицию.
func (e *Engine) HasActiveTrade() bool {
	return e.active != nil
}

// SkipReason преобразует ожидаемую блокировку входа в её стабильный код.
func SkipReason(err error) string {
	switch {
	case errors.Is(err, ErrPositionLimit):
		return SkipPositionLimit
	case errors.Is(err, ErrCooldownActive):
		return SkipCooldown
	case errors.Is(err, ErrDailyLimitReached):
		return SkipDailyLimit
	default:
		return ""
	}
}

// signalSide преобразует тип сигнала в направление виртуальной позиции.
func signalSide(signalType strategy.SignalType) (exchange.Side, error) {
	switch signalType {
	case strategy.SignalLong:
		return exchange.SideLong, nil
	case strategy.SignalShort:
		return exchange.SideShort, nil
	default:
		return "", ErrInvalidSignal
	}
}

// exitForCandle определяет виртуальный выход по подтверждённой свече.
func exitForCandle(plan Plan, candle exchange.Candle) (float64, string, bool) {
	switch plan.Side {
	case exchange.SideLong:
		if candle.Low <= plan.StopLoss {
			return plan.StopLoss, ExitStopLoss, true
		}
		if candle.High >= plan.TakeProfit {
			return plan.TakeProfit, ExitTakeProfit, true
		}
	case exchange.SideShort:
		if candle.High >= plan.StopLoss {
			return plan.StopLoss, ExitStopLoss, true
		}
		if candle.Low <= plan.TakeProfit {
			return plan.TakeProfit, ExitTakeProfit, true
		}
	}
	return 0, "", false
}

// positiveFinite проверяет положительное конечное число.
func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// nonNegativeFinite проверяет неотрицательное конечное число.
func nonNegativeFinite(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
