package model

import "time"

// FundingEvent содержит событие funding в точных единицах источника.
type FundingEvent struct {
	FundingTime time.Time
	Rate        Decimal
}

// FundingPage хранит отдельный поток событий, provenance и обе включительные границы запроса.
type FundingPage struct {
	Environment, Source, Category, Endpoint, Symbol string
	StartTime, EndTime, ServerTime                  time.Time
	Events                                          []FundingEvent
}
