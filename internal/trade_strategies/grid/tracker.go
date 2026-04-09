package grid

import "sync"

// GridTracker — потокобезопасное хранилище состояний сетки по символам.
type GridTracker struct {
	mu     sync.Mutex
	states map[string]*GridState
}

// NewGridTracker создаёт GridTracker.
func NewGridTracker() *GridTracker {
	return &GridTracker{
		states: make(map[string]*GridState),
	}
}

// Set сохраняет или заменяет состояние сетки для символа.
func (t *GridTracker) Set(state *GridState) {
	t.mu.Lock()
	t.states[state.Symbol] = state
	t.mu.Unlock()
}

// Get возвращает состояние сетки по символу.
func (t *GridTracker) Get(symbol string) (*GridState, bool) {
	t.mu.Lock()
	s, ok := t.states[symbol]
	t.mu.Unlock()
	return s, ok
}

// SetActive устанавливает флаг Active для символа.
func (t *GridTracker) SetActive(symbol string, active bool) {
	t.mu.Lock()
	if s, ok := t.states[symbol]; ok {
		s.Active = active
	}
	t.mu.Unlock()
}

// UpdatePrice обновляет текущую цену в состоянии сетки.
func (t *GridTracker) UpdatePrice(symbol string, price float64) {
	t.mu.Lock()
	if s, ok := t.states[symbol]; ok {
		s.CurrentPrice = price
	}
	t.mu.Unlock()
}

// All возвращает копию списка всех активных состояний.
func (t *GridTracker) All() []*GridState {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]*GridState, 0, len(t.states))
	for _, s := range t.states {
		result = append(result, s)
	}
	return result
}
