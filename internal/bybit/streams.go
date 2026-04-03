package bybit

const (
	// MaxSymbolsPerConn — лимит Bybit: не более 10 топиков на соединение.
	MaxSymbolsPerConn = 10
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

// BuildTopics формирует список аргументов подписки вида ["tickers.BTCUSDT", ...].
func BuildTopics(symbols []string) []string {
	topics := make([]string, len(symbols))
	for i, s := range symbols {
		topics[i] = "tickers." + s
	}
	return topics
}
