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
// Ценовые поля объявлены как FlexString: KuCoin иногда присылает их как JSON-число вместо строки.
type SnapshotData struct {
	Symbol          string     `json:"symbol"`
	High            FlexString `json:"high"`
	Low             FlexString `json:"low"`
	Vol             FlexString `json:"vol"`      // объём в базовой валюте
	VolValue        FlexString `json:"volValue"` // объём в USDT
	LastTradedPrice FlexString `json:"lastTradedPrice"`
	ChangePrice     FlexString `json:"changePrice"` // абсолютное изменение цены за 24ч
	ChangeRate      FlexString `json:"changeRate"`  // относительное изменение, десятичное (напр. -0.05 = -5%)
	Close           FlexString `json:"close"`
	Buy             FlexString `json:"buy"`  // лучший bid
	Sell            FlexString `json:"sell"` // лучший ask
	Datetime        int64      `json:"datetime"`
	Trading         bool       `json:"trading"`
}

// KuCoin REST API generic response.
type apiResponse struct {
	Code string          `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}
