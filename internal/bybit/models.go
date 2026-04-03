package bybit

import "encoding/json"

// instrumentsResponse — ответ REST /v5/market/instruments-info.
type instrumentsResponse struct {
	Result struct {
		List []instrumentInfo `json:"list"`
	} `json:"result"`
}

type instrumentInfo struct {
	Symbol    string `json:"symbol"`
	QuoteCoin string `json:"quoteCoin"`
	Status    string `json:"status"`
}

// topicMessage — оболочка входящего WebSocket-сообщения по топику.
type topicMessage struct {
	Topic string          `json:"topic"`
	Type  string          `json:"type"` // "snapshot" | "delta"
	Data  json.RawMessage `json:"data"`
}

// TickerData — данные тикера из события tickers.<SYMBOL>.
type TickerData struct {
	Symbol       string `json:"symbol"`
	LastPrice    string `json:"lastPrice"`
	OpenPrice    string `json:"openPrice"`
	HighPrice24h string `json:"highPrice24h"`
	LowPrice24h  string `json:"lowPrice24h"`
	Volume24h    string `json:"volume24h"`
	Price24hPcnt string `json:"price24hPcnt"`
}

// opMessage — служебное сообщение ping/pong.
type opMessage struct {
	Op string `json:"op"`
}

// subscribeMsg — сообщение подписки, отправляемое после Dial.
type subscribeMsg struct {
	Op   string   `json:"op"`
	Args []string `json:"args"`
}
