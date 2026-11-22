package model

import "time"

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
