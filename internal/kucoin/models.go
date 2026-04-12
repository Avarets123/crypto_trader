package kucoin

import "encoding/json"

// WsMessage — входящее сообщение KuCoin WebSocket.
type WsMessage struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`    // "welcome", "message", "ping", "ack", "error"
	Topic   string          `json:"topic"`
	Subject string          `json:"subject"`
	Data    json.RawMessage `json:"data"`
}

// WsTokenResponse — ответ на POST /api/v1/bullet-public.
type WsTokenResponse struct {
	Code string      `json:"code"`
	Data WsTokenData `json:"data"`
}

// WsTokenData — данные токена подключения к WS.
type WsTokenData struct {
	Token           string           `json:"token"`
	InstanceServers []WsInstanceServer `json:"instanceServers"`
}

// WsInstanceServer — WS-сервер KuCoin.
type WsInstanceServer struct {
	Endpoint     string `json:"endpoint"`
	Protocol     string `json:"protocol"`
	Encrypt      bool   `json:"encrypt"`
	PingInterval int    `json:"pingInterval"` // миллисекунды
	PingTimeout  int    `json:"pingTimeout"`  // миллисекунды
}

// SnapshotWrapper оборачивает вложенные данные снимка рынка.
type SnapshotWrapper struct {
	Sequence string       `json:"sequence"`
	Data     SnapshotData `json:"data"`
}

// SnapshotData — данные из топика /market/snapshot:{symbol}.
type SnapshotData struct {
	Symbol          string `json:"symbol"`
	High            string `json:"high"`
	Low             string `json:"low"`
	Vol             string `json:"vol"`      // объём в базовой валюте
	VolValue        string `json:"volValue"` // объём в USDT
	LastTradedPrice string `json:"lastTradedPrice"`
	ChangePrice     string `json:"changePrice"` // абсолютное изменение цены за 24ч
	ChangeRate      string `json:"changeRate"`  // относительное изменение, десятичное (напр. -0.05 = -5%)
	Close           string `json:"close"`
	Buy             string `json:"buy"`  // лучший bid
	Sell            string `json:"sell"` // лучший ask
	Datetime        int64  `json:"datetime"`
	Trading         bool   `json:"trading"`
}

// KuCoin REST API generic response.
type apiResponse struct {
	Code string          `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}
