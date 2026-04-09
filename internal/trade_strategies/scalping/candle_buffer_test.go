package scalping

import (
	"testing"
	"time"
)

func makeCandle(close float64) Candle {
	return Candle{
		OpenTime: time.Now(),
		Close:    close,
		IsClosed: true,
	}
}

func TestCandleBufferOrder(t *testing.T) {
	buf := NewCandleBuffer(5)

	// Добавляем 8 свечей в буфер размером 5
	for i := 1; i <= 8; i++ {
		buf.Push(makeCandle(float64(i)))
	}

	snap := buf.Snapshot()
	if len(snap) != 5 {
		t.Fatalf("expected 5 candles, got %d", len(snap))
	}

	// Ожидаем последние 5: 4,5,6,7,8
	for i, c := range snap {
		want := float64(i + 4)
		if c.Close != want {
			t.Errorf("snap[%d].Close = %.0f, want %.0f", i, c.Close, want)
		}
	}
}

func TestCandleBufferUnderflow(t *testing.T) {
	buf := NewCandleBuffer(10)
	buf.Push(makeCandle(1))
	buf.Push(makeCandle(2))

	snap := buf.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 candles, got %d", len(snap))
	}
	if snap[0].Close != 1 || snap[1].Close != 2 {
		t.Errorf("unexpected order: %.0f, %.0f", snap[0].Close, snap[1].Close)
	}
}

func TestCandleBufferEmpty(t *testing.T) {
	buf := NewCandleBuffer(5)
	snap := buf.Snapshot()
	if snap != nil {
		t.Errorf("expected nil snapshot for empty buffer, got %v", snap)
	}
	if buf.Len() != 0 {
		t.Errorf("expected Len()=0, got %d", buf.Len())
	}
}
