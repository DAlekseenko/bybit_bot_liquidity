package model

import "time"

// MarkPriceCandle хранит точную mark-price OHLC свечу.
type MarkPriceCandle struct {
	OpenTime               time.Time
	Open, High, Low, Close Decimal
	Confirmed              bool
}

// MarkPricePage хранит независимый mark-price поток и точные границы источника.
type MarkPricePage struct {
	Environment, Source, Category, Endpoint, Symbol, IntervalCode string
	StartTime, EndTime, ServerTime                                time.Time
	Candles                                                       []MarkPriceCandle
}
