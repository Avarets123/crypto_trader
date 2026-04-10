package exchange_orders

import "time"

// ExchangeOrder — одна торговая сделка с биржи.
type ExchangeOrder struct {
	Exchange  string
	Symbol    string
	TradeID   int64
	Price     string
	Quantity  string
	Side      string // "buy" | "sell"
	TradeTime time.Time
}
