package model

import "time"

// Candle хранит исходную свечу с точными значениями и временем открытия UTC.
type Candle struct {
	Symbol       string
	IntervalCode string
	OpenTime     time.Time
	Open         Decimal
	High         Decimal
	Low          Decimal
	Close        Decimal
	Volume       Decimal
	Turnover     Decimal
	Confirmed    bool
}
