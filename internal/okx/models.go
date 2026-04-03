package okx

// instrumentsResponse — ответ REST /api/v5/public/instruments.
type instrumentsResponse struct {
	Data []instrumentInfo `json:"data"`
}

type instrumentInfo struct {
	InstID   string `json:"instId"`
	QuoteCcy string `json:"quoteCcy"`
	State    string `json:"state"`
}

// tickerMessage — входящее WebSocket-сообщение с данными тикера.
type tickerMessage struct {
	Arg  tickerArg    `json:"arg"`
	Data []tickerData `json:"data"`
}

type tickerArg struct {
	Channel string `json:"channel"`
	InstID  string `json:"instId"`
}

// tickerData — данные тикера из канала tickers.
type tickerData struct {
	InstID  string `json:"instId"`
	Last    string `json:"last"`
	Open24h string `json:"open24h"`
	High24h string `json:"high24h"`
	Low24h  string `json:"low24h"`
	Vol24h  string `json:"vol24h"`
	SodUtc8 string `json:"sodUtc8"`
}

// subscribeMsg — сообщение подписки, отправляемое после Dial.
type subscribeMsg struct {
	Op   string              `json:"op"`
	Args []map[string]string `json:"args"`
}
