package comparator

import "time"

// SpreadEvent описывает один эпизод межбиржевого спреда.
type SpreadEvent struct {
	ID           int64
	Symbol       string
	ExchangeHigh string
	ExchangeLow  string
	OpenedAt     time.Time
	MaxSpreadPct float64
	PriceHigh    float64
	PriceLow     float64
}
