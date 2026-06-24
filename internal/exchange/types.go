package exchange

import (
	"context"
	"time"
)

// Side описывает направление позиции или ликвидации.
type Side string

const (
	SideLong  Side = "LONG"
	SideShort Side = "SHORT"
)

// Candle представляет одну свечу Bybit с OHLCV-данными.
type Candle struct {
	// Symbol - торговый инструмент, например BTCUSDT.
	Symbol string
	// OpenTime - время открытия свечи.
	OpenTime time.Time
	// Open, High, Low, Close - цены свечи.
	Open  float64
	High  float64
	Low   float64
	Close float64
	// Volume - объем базового актива за свечу.
	Volume float64
	// Turnover - оборот свечи в котируемой валюте.
	Turnover float64
	// Confirmed показывает, закрыта ли свеча окончательно.
	Confirmed bool
}

// OpenInterest хранит значение открытого интереса на конкретный момент времени.
type OpenInterest struct {
	// Symbol - торговый инструмент.
	Symbol string
	// Time - время получения или публикации значения.
	Time time.Time
	// Value - величина open interest.
	Value float64
}

// Liquidation описывает событие ликвидации из WebSocket-потока Bybit.
type Liquidation struct {
	// Symbol - торговый инструмент.
	Symbol string
	// Side - сторона ликвидированной позиции.
	Side Side
	// Price - цена ликвидации.
	Price float64
	// Qty - объем ликвидации в базовом активе.
	Qty float64
	// Time - время события.
	Time time.Time
}

func (l Liquidation) USDValue() float64 {
	return l.Price * l.Qty
}

// OrderRequest содержит параметры заявки, которую executor передает бирже.
type OrderRequest struct {
	// Symbol - торговый инструмент.
	Symbol string
	// Side - направление сделки.
	Side Side
	// Qty - рассчитанный размер позиции.
	Qty float64
	// StopLoss и TakeProfit - защитные уровни, рассчитанные risk manager.
	StopLoss   float64
	TakeProfit float64
}

// Order описывает результат успешной отправки заявки на биржу.
type Order struct {
	// ID - идентификатор ордера на стороне биржи.
	ID string
	// Symbol - торговый инструмент.
	Symbol string
	// Side - направление сделки.
	Side Side
	// Qty - фактический объем заявки.
	Qty float64
	// Price - цена исполнения или средняя цена, если она известна.
	Price float64
	// CreatedAt - время создания заявки.
	CreatedAt time.Time
}

// Position описывает открытую позицию по одному символу.
type Position struct {
	// Symbol - торговый инструмент.
	Symbol string
	// Side - направление позиции.
	Side Side
	// Qty - текущий размер позиции.
	Qty float64
	// EntryPrice - средняя цена входа.
	EntryPrice float64
	// OpenedAt - время открытия позиции.
	OpenedAt time.Time
}

// Client задает минимальный контракт биржевого клиента для MVP.
type Client interface {
	GetCandles(ctx context.Context, symbol string, limit int) ([]Candle, error)
	GetOpenInterest(ctx context.Context, symbol string) (OpenInterest, error)
	PlaceOrder(ctx context.Context, order OrderRequest) (Order, error)
	GetPosition(ctx context.Context, symbol string) (*Position, error)
	ClosePosition(ctx context.Context, symbol string) error
}
