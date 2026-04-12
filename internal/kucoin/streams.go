package kucoin

import "strings"

const (
	// MaxSymbolsPerConn — максимум символов на одно WS-соединение.
	// KuCoin поддерживает до 100 символов в одном топике.
	MaxSymbolsPerConn = 100
)

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

// BuildSnapshotTopic формирует топик подписки на снимки рынка для группы символов.
// Символы конвертируются в формат KuCoin: "BTCUSDT" → "BTC-USDT".
// Пример: /market/snapshot:BTC-USDT,ETH-USDT
func BuildSnapshotTopic(symbols []string) string {
	parts := make([]string, 0, len(symbols))
	for _, s := range symbols {
		parts = append(parts, toKucoinSymbol(s))
	}
	return "/market/snapshot:" + strings.Join(parts, ",")
}
