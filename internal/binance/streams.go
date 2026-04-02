package binance

import "strings"

const (
	MaxStreamsPerConn = 1024
	StreamsPerSymbol  = 2                                    // trade + miniTicker
	MaxSymbolsPerConn = MaxStreamsPerConn / StreamsPerSymbol // = 512
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

// BuildStreamURL формирует URL комбинированного стрима для группы символов.
// Пример: wss://stream.binance.com:9443/stream?streams=btcusdt@trade/btcusdt@miniTicker/...
func BuildStreamURL(symbols []string) string {
	parts := make([]string, 0, len(symbols)*2)
	for _, s := range symbols {
		lower := strings.ToLower(s)
		parts = append(parts, lower+"@trade", lower+"@miniTicker")
	}
	return "wss://stream.binance.com:9443/stream?streams=" + strings.Join(parts, "/")
}
