package scalping

import (
	"sync"
	"time"
)

// Candle — OHLCV свеча с признаком закрытия.
type Candle struct {
	OpenTime time.Time
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
	IsClosed bool // true если свеча закрылась
}

// CandleBuffer — потокобезопасный кольцевой буфер свечей фиксированного размера.
// Свечи пишутся из одной горутины KlineProvider, читаются из OnCandle.
type CandleBuffer struct {
	mu      sync.Mutex
	candles []Candle
	size    int
	head    int // индекс следующей записи
	count   int // количество хранимых свечей
}

// NewCandleBuffer создаёт буфер заданного размера.
func NewCandleBuffer(size int) *CandleBuffer {
	return &CandleBuffer{
		candles: make([]Candle, size),
		size:    size,
	}
}

// Push добавляет свечу в буфер. Если буфер заполнен — перезаписывает самую старую.
func (b *CandleBuffer) Push(c Candle) {
	b.mu.Lock()
	b.candles[b.head] = c
	b.head = (b.head + 1) % b.size
	if b.count < b.size {
		b.count++
	}
	b.mu.Unlock()
}

// Snapshot возвращает все хранимые свечи в хронологическом порядке (старые → новые).
func (b *CandleBuffer) Snapshot() []Candle {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == 0 {
		return nil
	}

	result := make([]Candle, b.count)
	start := (b.head - b.count + b.size) % b.size
	for i := 0; i < b.count; i++ {
		result[i] = b.candles[(start+i)%b.size]
	}
	return result
}

// Len возвращает текущее количество хранимых свечей.
func (b *CandleBuffer) Len() int {
	b.mu.Lock()
	n := b.count
	b.mu.Unlock()
	return n
}
