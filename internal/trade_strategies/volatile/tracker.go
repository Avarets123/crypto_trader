package volatile

import "sync"

// TradeTracker — потокобезопасное хранилище активных сделок.
type TradeTracker struct {
	mu     sync.Mutex
	trades map[string]*VolatileTrade // symbol → сделка
}

// NewTradeTracker создаёт TradeTracker.
func NewTradeTracker() *TradeTracker {
	return &TradeTracker{
		trades: make(map[string]*VolatileTrade),
	}
}

// Add добавляет сделку.
func (t *TradeTracker) Add(trade *VolatileTrade) {
	t.mu.Lock()
	t.trades[trade.Symbol] = trade
	t.mu.Unlock()
}

// Get возвращает сделку по символу.
func (t *TradeTracker) Get(symbol string) (*VolatileTrade, bool) {
	t.mu.Lock()
	trade, ok := t.trades[symbol]
	t.mu.Unlock()
	return trade, ok
}

// Remove удаляет сделку по символу.
func (t *TradeTracker) Remove(symbol string) {
	t.mu.Lock()
	delete(t.trades, symbol)
	t.mu.Unlock()
}

// Has возвращает true если есть открытая сделка по символу.
func (t *TradeTracker) Has(symbol string) bool {
	t.mu.Lock()
	_, ok := t.trades[symbol]
	t.mu.Unlock()
	return ok
}

// Count возвращает количество активных сделок.
func (t *TradeTracker) Count() int {
	t.mu.Lock()
	n := len(t.trades)
	t.mu.Unlock()
	return n
}
