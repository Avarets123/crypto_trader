// Package indicators реализует базовые технические индикаторы поверх свечей.
//
// Все функции принимают свежую→старую или старую→свежую упорядоченность,
// если иное не указано. По умолчанию ожидается старая→свежая (как возвращает
// большинство биржевых API) — последний элемент slice это самая свежая свеча.
package indicators

import (
	"math"

	"github.com/osman/bot-traider/internal/shared/exchange"
)

// ATR — Average True Range за period свечей.
// Использует классическую формулу Wilder: первое значение = SMA(TR, period),
// последующие = (prev*(period-1) + TR) / period.
// Возвращает 0 если свечей меньше period+1.
func ATR(klines []exchange.Kline, period int) float64 {
	if period < 1 || len(klines) < period+1 {
		return 0
	}
	trs := trueRanges(klines)
	// Первое значение — простое среднее первых period TR.
	var sum float64
	for i := 0; i < period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)
	for i := period; i < len(trs); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}
	return atr
}

// ADX — Average Directional Index за period свечей.
// Возвращает 0..100, где >25 обычно считается трендом, <20 — боковиком.
// Требует не менее 2*period+1 свечей.
func ADX(klines []exchange.Kline, period int) float64 {
	if period < 1 || len(klines) < 2*period+1 {
		return 0
	}
	n := len(klines)

	// True Range, +DM, -DM
	tr := make([]float64, n-1)
	plusDM := make([]float64, n-1)
	minusDM := make([]float64, n-1)

	for i := 1; i < n; i++ {
		curr := klines[i]
		prev := klines[i-1]

		highDiff := curr.High - prev.High
		lowDiff := prev.Low - curr.Low

		var pDM, mDM float64
		if highDiff > lowDiff && highDiff > 0 {
			pDM = highDiff
		}
		if lowDiff > highDiff && lowDiff > 0 {
			mDM = lowDiff
		}

		hl := curr.High - curr.Low
		hc := math.Abs(curr.High - prev.Close)
		lc := math.Abs(curr.Low - prev.Close)
		t := math.Max(hl, math.Max(hc, lc))

		idx := i - 1
		tr[idx] = t
		plusDM[idx] = pDM
		minusDM[idx] = mDM
	}

	// Wilder smoothing для tr/plusDM/minusDM за period значений.
	atr := smoothWilder(tr, period)
	pSm := smoothWilder(plusDM, period)
	mSm := smoothWilder(minusDM, period)
	if len(atr) == 0 {
		return 0
	}

	// DI и DX.
	dx := make([]float64, len(atr))
	for i := range atr {
		if atr[i] == 0 {
			dx[i] = 0
			continue
		}
		plusDI := 100 * pSm[i] / atr[i]
		minusDI := 100 * mSm[i] / atr[i]
		sum := plusDI + minusDI
		if sum == 0 {
			dx[i] = 0
			continue
		}
		dx[i] = 100 * math.Abs(plusDI-minusDI) / sum
	}

	// ADX = Wilder average of DX за period.
	if len(dx) < period {
		return 0
	}
	var adx float64
	for i := 0; i < period; i++ {
		adx += dx[i]
	}
	adx /= float64(period)
	for i := period; i < len(dx); i++ {
		adx = (adx*float64(period-1) + dx[i]) / float64(period)
	}
	return adx
}

// PriceChangePct возвращает изменение цены последней свечи относительно first
// свечи в slice (в процентах). Используется для anti-frontrun проверок.
func PriceChangePct(klines []exchange.Kline) float64 {
	if len(klines) < 2 {
		return 0
	}
	first := klines[0].Close
	last := klines[len(klines)-1].Close
	if first <= 0 {
		return 0
	}
	return (last - first) / first * 100
}

func trueRanges(klines []exchange.Kline) []float64 {
	if len(klines) < 2 {
		return nil
	}
	out := make([]float64, len(klines)-1)
	for i := 1; i < len(klines); i++ {
		curr := klines[i]
		prev := klines[i-1]
		hl := curr.High - curr.Low
		hc := math.Abs(curr.High - prev.Close)
		lc := math.Abs(curr.Low - prev.Close)
		out[i-1] = math.Max(hl, math.Max(hc, lc))
	}
	return out
}

// smoothWilder применяет Wilder smoothing (по сути EMA с alpha=1/period)
// к ряду values, возвращает массив сглаженных значений длиной len(values)-period+1.
// Первое значение = sum(values[0..period-1]), далее: prev - prev/period + curr.
func smoothWilder(values []float64, period int) []float64 {
	if period < 1 || len(values) < period {
		return nil
	}
	out := make([]float64, len(values)-period+1)
	var sum float64
	for i := 0; i < period; i++ {
		sum += values[i]
	}
	out[0] = sum
	for i := period; i < len(values); i++ {
		out[i-period+1] = out[i-period] - out[i-period]/float64(period) + values[i]
	}
	return out
}
