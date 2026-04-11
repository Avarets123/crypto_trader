package orderbook

// CalcOBI вычисляет Order Book Imbalance — дисбаланс стакана.
//
// OBI = (bidVol - askVol) / (bidVol + askVol)
//
// Результат в диапазоне [-1, +1]:
//   +1 — весь объём на стороне покупателей
//   -1 — весь объём на стороне продавцов
//    0 — баланс
func CalcOBI(bidVol, askVol float64) float64 {
	total := bidVol + askVol
	if total == 0 {
		return 0
	}
	return (bidVol - askVol) / total
}

// OBISignal возвращает эмодзи-индикатор давления по значению OBI.
func OBISignal(obi float64) string {
	switch {
	case obi >= 0.3:
		return "🟢"
	case obi <= -0.3:
		return "🔴"
	default:
		return "⚪"
	}
}
