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

// CalcBullScore вычисляет составной сигнал направления движения цены.
//
// Компоненты и веса:
//   0.25 * OBI        — давление стакана прямо сейчас
//   0.15 * obiDelta   — изменение OBI с baseline (momentum стакана)
//   0.25 * tfi        — поток реальных сделок (не спуфится)
//   0.15 * volImb     — объём реальных сделок
//   0.10 * askDrain   — уход продавцов из стакана (>0 = bullish)
//   0.10 * aggrRatio  — агрессивность покупателей: avgBuySize vs avgSellSize
//
// Все входные значения должны быть в [-1, +1].
// Результат > +0.40 — вероятен рост, < -0.40 — вероятно падение.
func CalcBullScore(obi, obiDelta, tfi, volImb, askDrain, aggrRatio float64) float64 {
	return 0.25*obi +
		0.15*clampF(obiDelta, -1, 1) +
		0.25*tfi +
		0.15*volImb +
		0.10*askDrain +
		0.10*aggrRatio
}

// BullSignal возвращает эмодзи-индикатор по значению BullScore.
func BullSignal(score float64) string {
	switch {
	case score >= 0.40:
		return "🚀"
	case score <= -0.40:
		return "📉"
	default:
		return "➡️"
	}
}

func clampF(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
