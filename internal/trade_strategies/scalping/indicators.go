package scalping

import "math"

// EMA вычисляет Exponential Moving Average для среза цен.
// Использует SMA как начальное значение, затем Wilder's smoothing.
func EMA(prices []float64, period int) float64 {
	n := len(prices)
	if n == 0 || period <= 0 {
		return 0
	}
	if n < period {
		// Недостаточно данных: SMA по доступным
		sum := 0.0
		for _, p := range prices {
			sum += p
		}
		return sum / float64(n)
	}
	// SMA первых period элементов как seed
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += prices[i]
	}
	ema := sum / float64(period)
	k := 2.0 / float64(period+1)
	for i := period; i < n; i++ {
		ema = prices[i]*k + ema*(1-k)
	}
	return ema
}

// RSI вычисляет Relative Strength Index по методу Wilder's smoothing.
func RSI(prices []float64, period int) float64 {
	n := len(prices)
	if n < period+1 {
		return 50 // недостаточно данных — нейтральное значение
	}

	// Начальные средние gain/loss за первые period свечей
	var avgGain, avgLoss float64
	for i := 1; i <= period; i++ {
		change := prices[i] - prices[i-1]
		if change > 0 {
			avgGain += change
		} else {
			avgLoss -= change
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)

	// Wilder's smoothing для остальных
	for i := period + 1; i < n; i++ {
		change := prices[i] - prices[i-1]
		gain, loss := 0.0, 0.0
		if change > 0 {
			gain = change
		} else {
			loss = -change
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
	}

	if avgLoss == 0 {
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - (100 / (1 + rs))
}

// BollingerBands вычисляет верхнюю, среднюю и нижнюю полосы Боллинджера.
// Использует последние period цен.
func BollingerBands(prices []float64, period int, stdDev float64) (upper, middle, lower float64) {
	n := len(prices)
	if n < period {
		return 0, 0, 0
	}
	// SMA по последним period ценам
	recent := prices[n-period:]
	sum := 0.0
	for _, p := range recent {
		sum += p
	}
	middle = sum / float64(period)

	// Стандартное отклонение
	variance := 0.0
	for _, p := range recent {
		d := p - middle
		variance += d * d
	}
	variance /= float64(period)
	sd := math.Sqrt(variance)

	upper = middle + stdDev*sd
	lower = middle - stdDev*sd
	return
}

// ATR вычисляет Average True Range по методу Wilder's smoothing.
// high, low, close должны иметь одинаковую длину.
func ATR(high, low, close []float64, period int) float64 {
	n := len(close)
	if n < 2 || len(high) < n || len(low) < n {
		return 0
	}

	// True Range для каждой свечи (кроме первой)
	trs := make([]float64, n-1)
	for i := 1; i < n; i++ {
		hl := high[i] - low[i]
		hc := math.Abs(high[i] - close[i-1])
		lc := math.Abs(low[i] - close[i-1])
		trs[i-1] = math.Max(hl, math.Max(hc, lc))
	}

	if len(trs) < period {
		// Недостаточно данных: SMA по доступным
		sum := 0.0
		for _, tr := range trs {
			sum += tr
		}
		if len(trs) == 0 {
			return 0
		}
		return sum / float64(len(trs))
	}

	// Начальный ATR = SMA первых period TR
	atr := 0.0
	for i := 0; i < period; i++ {
		atr += trs[i]
	}
	atr /= float64(period)

	// Wilder's smoothing
	for i := period; i < len(trs); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}
	return atr
}

// MACD вычисляет MACD Line, Signal Line и Histogram.
// Возвращает последние значения.
func MACD(prices []float64, fast, slow, signal int) (macdLine, signalLine, histogram float64) {
	if len(prices) < slow {
		return 0, 0, 0
	}

	// Вычисляем серию MACD значений начиная с момента когда есть slow свечей
	macdSeries := make([]float64, 0, len(prices)-slow+1)
	for i := slow - 1; i < len(prices); i++ {
		sub := prices[:i+1]
		macdSeries = append(macdSeries, EMA(sub, fast)-EMA(sub, slow))
	}

	if len(macdSeries) == 0 {
		return 0, 0, 0
	}

	macdLine = macdSeries[len(macdSeries)-1]

	if len(macdSeries) < signal {
		return macdLine, 0, macdLine
	}

	signalLine = EMA(macdSeries, signal)
	histogram = macdLine - signalLine
	return
}
