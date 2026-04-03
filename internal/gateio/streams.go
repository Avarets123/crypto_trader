package gateio

import (
	"encoding/json"
	"time"
)

const MaxSymbolsPerConn = 200

// ChunkSymbols разбивает срез символов на группы заданного размера.
func ChunkSymbols(symbols []string, chunkSize int) [][]string {
	var chunks [][]string
	for i := 0; i < len(symbols); i += chunkSize {
		end := min(i+chunkSize, len(symbols))
		chunk := make([]string, end-i)
		copy(chunk, symbols[i:end])
		chunks = append(chunks, chunk)
	}
	return chunks
}

type subscribeMsg struct {
	Time    int64    `json:"time"`
	Channel string   `json:"channel"`
	Event   string   `json:"event"`
	Payload []string `json:"payload"`
}

// BuildSubscribeMsg формирует JSON-сообщение подписки на spot.tickers для Gate.io WebSocket v4.
// Символы передаются в формате BTC_USDT (верхний регистр, подчёркивание).
func BuildSubscribeMsg(symbols []string) []byte {
	msg := subscribeMsg{
		Time:    time.Now().Unix(),
		Channel: "spot.tickers",
		Event:   "subscribe",
		Payload: symbols,
	}
	b, _ := json.Marshal(msg)
	return b
}
