package news_momentum

import (
	"sync"
	"time"
)

// MomentumTrade — одна открытая позиция стратегии.
type MomentumTrade struct {
	ID         int64
	Symbol     string
	Source     string // источник новости
	NewsGUID   string
	EntryPrice float64
	TPPrice    float64
	SLPrice    float64
	Qty        float64
	OpenedAt   time.Time

	HighestPrice float64 // для trailing stop
	TrailActive  bool
	TrailSL      float64

	PriceCh chan float64
	StopCh  chan struct{}
}

// Tracker — потокобезопасный стор открытых позиций по символу.
type Tracker struct {
	mu     sync.Mutex
	trades map[string]*MomentumTrade
}

func NewTracker() *Tracker {
	return &Tracker{trades: make(map[string]*MomentumTrade)}
}

func (t *Tracker) Add(trade *MomentumTrade) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.trades[trade.Symbol] = trade
}

func (t *Tracker) Get(symbol string) (*MomentumTrade, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr, ok := t.trades[symbol]
	return tr, ok
}

func (t *Tracker) Remove(symbol string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.trades, symbol)
}

func (t *Tracker) Has(symbol string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.trades[symbol]
	return ok
}

func (t *Tracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.trades)
}
