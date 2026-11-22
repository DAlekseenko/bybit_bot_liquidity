package model

import (
	"strings"
	"time"
)

// AccountMode хранит проверенный режим Unified account и серверное время чтения.
type AccountMode struct {
	UnifiedMarginStatus int
	MarginMode          string
	ServerTime          time.Time
}

// IsolatedBalance хранит точный USDT coin баланс isolated margin счёта.
//
// Available вычислен по документированной формуле брокера для isolated margin:
// walletBalance - totalPositionIM - totalOrderIM - locked - bonus. Account-wide
// поля (totalAvailableBalance и т.п.) в isolated режиме неприменимы и не читаются.
type IsolatedBalance struct {
	Coin                          string
	Equity, WalletBalance         Decimal
	TotalPositionIM, TotalOrderIM Decimal
	Locked, Bonus, Available      Decimal
	ServerTime                    time.Time
}

// AccountPosition хранит точную позицию счёта по паре symbol/positionIdx.
//
// PositionIdx: 0 — one-way, 1/2 — стороны hedge mode. Side пуст для нулевой
// позиции. Leverage и AutoAddMargin — текущие настройки биржи, не цели стратегии.
type AccountPosition struct {
	Symbol        string
	PositionIdx   int
	Side          string
	Size          Decimal
	AvgPrice      Decimal
	MarkPrice     Decimal
	Leverage      Decimal
	PositionIM    Decimal
	AutoAddMargin bool
	Status        string
	UpdatedTime   time.Time
	ServerTime    time.Time
}

// AccountOrder хранит точную активную заявку счёта.
//
// OrderLinkID может быть пустым для ручной заявки; принадлежность стратегии
// определяет только OrderOwnedBy, а не наличие заявки на символе стратегии.
type AccountOrder struct {
	OrderID, OrderLinkID  string
	Symbol, Side, Status  string
	PositionIdx           int
	Qty, LeavesQty, Price Decimal
	ReduceOnly            bool
	// CloseOnTrigger с ReduceOnly и Qty=0 обозначает close-all условную заявку.
	CloseOnTrigger bool
	CreatedTime    time.Time
	UpdatedTime    time.Time
}

// AccountInventory хранит все позиции и активные заявки счёта, а не только символы стратегии.
//
// ServerTime — самое раннее время сервера среди всех прочитанных страниц.
type AccountInventory struct {
	Positions  []AccountPosition
	Orders     []AccountOrder
	ServerTime time.Time
}

// OrderOwnedBy сообщает, принадлежит ли заявка namespace LinkID стратегии.
//
// Пустой namespace или пустой LinkID никогда не дают принадлежности; позиции
// не имеют LinkID и по этому признаку стратегии не приписываются.
func OrderOwnedBy(order AccountOrder, namespace string) bool {
	return namespace != "" && order.OrderLinkID != "" && strings.HasPrefix(order.OrderLinkID, namespace)
}
