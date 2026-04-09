package grid

import "math"

// RoundPrice округляет цену до точности, приемлемой для большинства USDT-пар на Binance/Bybit.
// Binance использует tickSize=0.01 для большинства пар >= 1 USDT (BTC, ETH, SOL, BNB и др.).
func RoundPrice(price float64) float64 {
	switch {
	case price >= 10:
		return math.Round(price*100) / 100 // 2 знака: SOL=78.86, ETH=2182.47, BTC=65000.10
	case price >= 1:
		return math.Round(price*1000) / 1000 // 3 знака: напр. 3.456
	case price >= 0.01:
		return math.Round(price*10000) / 10000 // 4 знака
	default:
		return math.Round(price*100000) / 100000 // 5 знаков
	}
}

// CalcRatio вычисляет геометрический шаг сетки.
// Ratio = (upper / lower) ^ (1 / grids)
func CalcRatio(lower, upper float64, grids int) float64 {
	return math.Pow(upper/lower, 1.0/float64(grids))
}

// CalcLevels возвращает цены уровней сетки: Price[i] = lower * Ratio^i, i = 0..grids.
func CalcLevels(lower, upper float64, grids int) []float64 {
	ratio := CalcRatio(lower, upper, grids)
	levels := make([]float64, grids+1)
	for i := range levels {
		levels[i] = lower * math.Pow(ratio, float64(i))
	}
	return levels
}

// CalcQtyPerLevel вычисляет объём в Base Currency на каждый уровень.
// qty = (totalUSDT / 2) / grids / avgPrice
func CalcQtyPerLevel(totalUSDT float64, levels []float64) float64 {
	if len(levels) < 2 {
		return 0
	}
	grids := len(levels) - 1
	lower := levels[0]
	upper := levels[len(levels)-1]
	avgPrice := (lower + upper) / 2
	if avgPrice <= 0 {
		return 0
	}
	return (totalUSDT / 2) / float64(grids) / avgPrice
}

// CalcStopLoss вычисляет уровень стоп-лосса ниже нижней границы.
// stopLoss = lower * (1 - stopLossPct/100)
func CalcStopLoss(lower float64, stopLossPct float64) float64 {
	return lower * (1 - stopLossPct/100)
}
