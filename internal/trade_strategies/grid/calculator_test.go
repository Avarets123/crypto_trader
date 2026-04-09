package grid

import (
	"math"
	"testing"
)

func TestCalcRatio(t *testing.T) {
	lower := 1000.0
	upper := 2000.0
	grids := 10

	ratio := CalcRatio(lower, upper, grids)

	// lower * ratio^grids должно равняться upper
	got := lower * math.Pow(ratio, float64(grids))
	if math.Abs(got-upper)/upper > 1e-9 {
		t.Errorf("CalcRatio: lower*ratio^grids = %f, want %f", got, upper)
	}
}

func TestCalcLevels_Count(t *testing.T) {
	lower := 1000.0
	upper := 2000.0
	grids := 20

	levels := CalcLevels(lower, upper, grids)

	if len(levels) != grids+1 {
		t.Errorf("CalcLevels: got %d levels, want %d", len(levels), grids+1)
	}
}

func TestCalcLevels_Monotone(t *testing.T) {
	lower := 1000.0
	upper := 2000.0
	grids := 20

	levels := CalcLevels(lower, upper, grids)

	for i := 1; i < len(levels); i++ {
		if levels[i] <= levels[i-1] {
			t.Errorf("CalcLevels: not monotone at index %d: %f <= %f", i, levels[i], levels[i-1])
		}
	}
}

func TestCalcLevels_Bounds(t *testing.T) {
	lower := 1000.0
	upper := 2000.0
	grids := 20

	levels := CalcLevels(lower, upper, grids)

	if math.Abs(levels[0]-lower)/lower > 1e-9 {
		t.Errorf("CalcLevels: first level = %f, want %f", levels[0], lower)
	}
	if math.Abs(levels[grids]-upper)/upper > 1e-9 {
		t.Errorf("CalcLevels: last level = %f, want %f", levels[grids], upper)
	}
}

func TestCalcQtyPerLevel(t *testing.T) {
	lower := 1000.0
	upper := 2000.0
	grids := 20
	totalUSDT := 200.0

	levels := CalcLevels(lower, upper, grids)
	qty := CalcQtyPerLevel(totalUSDT, levels)

	// Суммарный USDT = qty * grids * avgPrice = totalUSDT / 2
	avgPrice := (lower + upper) / 2
	totalUsed := qty * float64(grids) * avgPrice
	if totalUsed > totalUSDT+1e-6 {
		t.Errorf("CalcQtyPerLevel: total USDT used %f > totalUSDT %f", totalUsed, totalUSDT)
	}
	if qty <= 0 {
		t.Errorf("CalcQtyPerLevel: qty = %f, want > 0", qty)
	}
}

func TestCalcStopLoss(t *testing.T) {
	lower := 1000.0
	stopLossPct := 2.0

	sl := CalcStopLoss(lower, stopLossPct)

	expected := lower * (1 - stopLossPct/100)
	if math.Abs(sl-expected) > 1e-9 {
		t.Errorf("CalcStopLoss: got %f, want %f", sl, expected)
	}
	if sl >= lower {
		t.Errorf("CalcStopLoss: sl %f must be below lower %f", sl, lower)
	}
}
