package gateio

import "encoding/json"

// WsMessage — оболочка входящего WebSocket-сообщения Gate.io v4.
type WsMessage struct {
	Channel string          `json:"channel"`
	Event   string          `json:"event"`
	Result  json.RawMessage `json:"result"`
}

// TickerResult — данные тикера из события spot.tickers.
type TickerResult struct {
	CurrencyPair     string `json:"currency_pair"`
	Last             string `json:"last"`
	LowestAsk        string `json:"lowest_ask"`
	HighestBid       string `json:"highest_bid"`
	OpenPrice        string `json:"open_24h"`
	HighPrice        string `json:"high_24h"`
	LowPrice         string `json:"low_24h"`
	BaseVolume       string `json:"base_volume"`
	QuoteVolume      string `json:"quote_volume"`
	ChangePercentage string `json:"change_percentage"`
}
