package okx

const (
	// MaxSymbolsPerConn — лимит OKX: не более 100 топиков на соединение.
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

// BuildArgs формирует аргументы подписки вида [{"channel":"tickers","instId":"BTC-USDT"}, ...].
func BuildArgs(symbols []string) []map[string]string {
	args := make([]map[string]string, len(symbols))
	for i, s := range symbols {
		args[i] = map[string]string{
			"channel": "tickers",
			"instId":  s,
		}
	}
	return args
}
