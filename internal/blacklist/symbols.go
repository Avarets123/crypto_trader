package blacklist

// symbols — чёрный список символов, исключённых из торговли и мониторинга.
// Используется map для O(1) проверки.
var symbols = map[string]struct{}{
	"UUSDT":    {},
	"GLMRUSDT": {},
	"BTTCUSDT": {}, // микроцена (~0.0000018 USDT), qty выходит за рамки баланса
}

// IsBlacklisted возвращает true, если символ находится в чёрном списке
// или содержит не-ASCII символы (мем-токены с иероглифами и т.п.).
func IsBlacklisted(symbol string) bool {
	if _, ok := symbols[symbol]; ok {
		return true
	}
	for _, r := range symbol {
		if r > 127 {
			return true
		}
	}
	return false
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
