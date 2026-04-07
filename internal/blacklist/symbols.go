package blacklist

// symbols — чёрный список символов, исключённых из торговли и мониторинга.
// Используется map для O(1) проверки.
var symbols = map[string]struct{}{
	"UUSDT":    {},
	"GLMRUSDT": {},
	"BTTCUSDT": {}, // микроцена (~0.0000018 USDT), qty выходит за рамки баланса
}

// IsBlacklisted возвращает true, если символ находится в чёрном списке.
func IsBlacklisted(symbol string) bool {
	_, ok := symbols[symbol]
	return ok
}

// FilterSymbols возвращает новый срез без символов из чёрного списка.
func FilterSymbols(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !IsBlacklisted(s) {
			out = append(out, s)
		}
	}
	return out
}
