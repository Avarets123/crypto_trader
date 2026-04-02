package binance

import "encoding/json"

// StreamMessage — оболочка комбинированного стрима Binance.
type StreamMessage struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

// TradeEvent — событие совершённой сделки (@trade).
type TradeEvent struct {
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	Symbol    string `json:"s"`
	Price     string `json:"p"`
	Quantity  string `json:"q"`
	TradeTime int64  `json:"T"`
	IsMaker   bool   `json:"m"`
}

// MiniTickerEvent — событие обновления тикера (@miniTicker).
type MiniTickerEvent struct {
	EventType   string `json:"e"`
	EventTime   int64  `json:"E"`
	Symbol      string `json:"s"`
	LastPrice   string `json:"c"`
	OpenPrice   string `json:"o"`
	HighPrice   string `json:"h"`
	LowPrice    string `json:"l"`
	BaseVolume  string `json:"v"`
	QuoteVolume string `json:"q"`
}
