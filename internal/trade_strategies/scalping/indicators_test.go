package scalping

import (
	"math"
	"testing"
)

func TestEMA(t *testing.T) {
	// EMA(3) для последовательности [1,2,3,4,5]
	// SMA[0..2] = (1+2+3)/3 = 2.0; k = 2/(3+1) = 0.5
	// EMA(4) = 4*0.5 + 2.0*0.5 = 3.0
	// EMA(5) = 5*0.5 + 3.0*0.5 = 4.0
	prices := []float64{1, 2, 3, 4, 5}
	result := EMA(prices, 3)
	expected := 4.0
	if math.Abs(result-expected) > 1e-9 {
		t.Errorf("EMA(3) = %.6f, want %.6f", result, expected)
	}
}

func TestEMAShortData(t *testing.T) {
	// Если данных меньше period — возвращаем SMA
	prices := []float64{2, 4}
	result := EMA(prices, 5)
	expected := 3.0 // SMA(2,4) = 3
	if math.Abs(result-expected) > 1e-9 {
		t.Errorf("EMA short data = %.6f, want %.6f", result, expected)
	}
}

func TestRSI(t *testing.T) {
	// Последовательность: 7 ростов подряд по 1, затем 2 падения по 0.5
	// После 7 ростов: avgGain=1, avgLoss=0 → RSI=100
	// Потом при падениях RSI снизится, но проверим базовую корректность
	prices := []float64{10, 11, 12, 13, 14, 15, 16, 17, 16.5, 16}
	result := RSI(prices, 7)
	if result < 0 || result > 100 {
		t.Errorf("RSI out of [0,100] range: %.2f", result)
	}
	// После преобладающего роста RSI должен быть > 50
	if result <= 50 {
		t.Errorf("RSI expected > 50 after mostly upward prices, got %.2f", result)
	}
}

func TestRSIOversold(t *testing.T) {
	// Непрерывные снижения → RSI должен быть низким (< 30)
	prices := make([]float64, 20)
	for i := range prices {
		prices[i] = 100.0 - float64(i)*2
	}
	result := RSI(prices, 7)
	if result >= 30 {
		t.Errorf("RSI on declining prices expected < 30, got %.2f", result)
	}
}

func TestBollingerBands(t *testing.T) {
	// При данных: [2,2,2,2,2] — все равны mean
	// middle=2, upper=2+2*0=2, lower=2-2*0=2
	prices := []float64{2, 2, 2, 2, 2}
	upper, middle, lower := BollingerBands(prices, 5, 2.0)
	if math.Abs(middle-2.0) > 1e-9 {
		t.Errorf("BB middle = %.6f, want 2.0", middle)
	}
	if math.Abs(upper-middle) > 1e-9 || math.Abs(lower-middle) > 1e-9 {
		t.Errorf("BB upper=%.6f lower=%.6f should equal middle=%.6f for const prices", upper, lower, middle)
	}

	// lower = middle - 2*std, upper = middle + 2*std
	prices2 := []float64{1, 2, 3, 4, 5}
	upper2, middle2, lower2 := BollingerBands(prices2, 5, 2.0)
	// mean = 3, variance = (4+1+0+1+4)/5 = 2, std = sqrt(2)
	expectedMiddle := 3.0
	expectedStd := math.Sqrt(2.0)
	if math.Abs(middle2-expectedMiddle) > 1e-9 {
		t.Errorf("BB middle = %.6f, want %.6f", middle2, expectedMiddle)
	}
	if math.Abs(upper2-(middle2+2*expectedStd)) > 1e-6 {
		t.Errorf("BB upper = %.6f, want %.6f", upper2, middle2+2*expectedStd)
	}
	if math.Abs(lower2-(middle2-2*expectedStd)) > 1e-6 {
		t.Errorf("BB lower = %.6f, want %.6f", lower2, middle2-2*expectedStd)
	}
}

func TestATR(t *testing.T) {
	// 4 свечи, простые TR:
	// Свеча 1: H=12, L=10, C=11 (baseline close)
	// Свеча 2: H=13, L=11, C=12 → TR = max(13-11, |13-11|, |11-11|) = 2
	// Свеча 3: H=14, L=12, C=13 → TR = max(14-12, |14-12|, |12-12|) = 2
	// Свеча 4: H=15, L=13, C=14 → TR = max(15-13, |15-13|, |13-13|) = 2
	high := []float64{12, 13, 14, 15}
	low := []float64{10, 11, 12, 13}
	close := []float64{11, 12, 13, 14}
	result := ATR(high, low, close, 3)
	// TRs = [2,2,2], ATR(3) = SMA([2,2,2]) = 2
	if math.Abs(result-2.0) > 1e-6 {
		t.Errorf("ATR = %.6f, want 2.0", result)
	}
}
