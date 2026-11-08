package model

import "time"

// CandlePage хранит исходную страницу и явное происхождение публичных данных.
type CandlePage struct {
	Environment string
	Source      string
	Category    string
	Endpoint    string
	ServerTime  time.Time
	Candles     []Candle
}
