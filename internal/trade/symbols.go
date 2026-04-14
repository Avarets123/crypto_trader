package trade

// OpenTradeSymbols возвращает уникальные символы всех открытых позиций.
func OpenTradeSymbols(svc *Service) []string {
	trades := svc.GetOpenTrades()
	seen := make(map[string]struct{}, len(trades))
	result := make([]string, 0, len(trades))
	for _, t := range trades {
		if _, ok := seen[t.Symbol]; !ok {
			seen[t.Symbol] = struct{}{}
			result = append(result, t.Symbol)
		}
	}
	return result
}

// MergeSymbols объединяет два среза символов без дублей.
func MergeSymbols(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	result := make([]string, 0, len(base)+len(extra))
	for _, s := range base {
		seen[s] = struct{}{}
		result = append(result, s)
	}
	for _, s := range extra {
		if _, ok := seen[s]; !ok {
			result = append(result, s)
		}
	}
	return result
}
