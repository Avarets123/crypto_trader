package arbitration

import (
	"sync"
	"time"
)

// ArbPosition описывает одну открытую арб-позицию.
type ArbPosition struct {
	ID             int64
	Symbol         string
	TradeExchange  string  // биржа где куплен актив (следим за её тикерами)
	SignalExchange string  // биржа которая дала сигнал (более высокая цена)
	EntryPrice     float64
	TargetPrice    float64 // цена биржи-сигнала в момент открытия
	StopLosPrice   float64
	Qty            float64
	OpenedAt       time.Time
	PriceCh        chan float64
	SpreadClosedCh chan struct{} // закрывается когда схлопывается спред
}

// PositionTracker хранит активные позиции в памяти.
type PositionTracker struct {
	mu        sync.Mutex
	positions map[string]*ArbPosition // symbol → позиция
}

// NewPositionTracker создаёт PositionTracker.
func NewPositionTracker() *PositionTracker {
	return &PositionTracker{
		positions: make(map[string]*ArbPosition),
	}
}

// Add добавляет позицию.
func (t *PositionTracker) Add(pos *ArbPosition) {
	t.mu.Lock()
	t.positions[pos.Symbol] = pos
	t.mu.Unlock()
}

// Remove удаляет позицию по символу.
func (t *PositionTracker) Remove(symbol string) {
	t.mu.Lock()
	delete(t.positions, symbol)
	t.mu.Unlock()
}

// GetBySymbol возвращает позицию по символу.
func (t *PositionTracker) GetBySymbol(symbol string) (*ArbPosition, bool) {
	t.mu.Lock()
	pos, ok := t.positions[symbol]
	t.mu.Unlock()
	return pos, ok
}

