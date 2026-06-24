package execution

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"bybit_bot_liquidity/internal/exchange"
	"bybit_bot_liquidity/internal/risk"
	"bybit_bot_liquidity/internal/strategy"
)

var (
	ErrPositionAlreadyOpen = errors.New("position already open")
	ErrCooldownActive      = errors.New("cooldown is active")
	ErrUnsupportedSignal   = errors.New("unsupported signal")
)

// Exchange ограничивает executor только методами, нужными для исполнения сделок.
type Exchange interface {
	PlaceOrder(ctx context.Context, order exchange.OrderRequest) (exchange.Order, error)
	GetPosition(ctx context.Context, symbol string) (*exchange.Position, error)
	ClosePosition(ctx context.Context, symbol string) error
}

// SignalStore сохраняет все рассчитанные сигналы до попытки исполнения.
type SignalStore interface {
	SaveSignal(ctx context.Context, signal strategy.Signal) error
}

// Executor применяет торговые ограничения и отправляет ордера на биржу.
type Executor struct {
	exchange    Exchange
	riskManager *risk.Manager
	store       SignalStore
	log         *slog.Logger
	cooldown    time.Duration
	now         func() time.Time
	lastClosed  time.Time
}

func New(exchange Exchange, riskManager *risk.Manager, store SignalStore, log *slog.Logger, cooldown time.Duration) *Executor {
	if log == nil {
		log = slog.Default()
	}

	return &Executor{
		exchange:    exchange,
		riskManager: riskManager,
		store:       store,
		log:         log,
		cooldown:    cooldown,
		now:         time.Now,
	}
}

func (e *Executor) Execute(ctx context.Context, signal strategy.Signal, balance, entryPrice float64) (exchange.Order, error) {
	if e.store != nil {
		if err := e.store.SaveSignal(ctx, signal); err != nil {
			return exchange.Order{}, err
		}
	}

	switch signal.Type {
	case strategy.SignalNone:
		return exchange.Order{}, nil
	case strategy.SignalLong, strategy.SignalShort:
	default:
		return exchange.Order{}, ErrUnsupportedSignal
	}

	if !e.lastClosed.IsZero() && e.now().Sub(e.lastClosed) < e.cooldown {
		return exchange.Order{}, ErrCooldownActive
	}

	position, err := e.exchange.GetPosition(ctx, signal.Symbol)
	if err != nil {
		return exchange.Order{}, err
	}
	if position != nil {
		return exchange.Order{}, ErrPositionAlreadyOpen
	}

	side := exchange.SideLong
	if signal.Type == strategy.SignalShort {
		side = exchange.SideShort
	}

	plan, err := e.riskManager.Plan(balance, entryPrice, side)
	if err != nil {
		return exchange.Order{}, err
	}

	order, err := e.exchange.PlaceOrder(ctx, exchange.OrderRequest{
		Symbol:     signal.Symbol,
		Side:       side,
		Qty:        plan.Qty,
		StopLoss:   plan.StopLoss,
		TakeProfit: plan.TakeProfit,
	})
	if err != nil {
		return exchange.Order{}, err
	}

	e.log.Info("position opened", "symbol", signal.Symbol, "side", side, "qty", plan.Qty)
	return order, nil
}

func (e *Executor) MarkPositionClosed(at time.Time) {
	e.lastClosed = at
}

func (e *Executor) SetNow(now func() time.Time) {
	e.now = now
}
