package grid

import "time"

// GridLevel — один уровень сетки.
type GridLevel struct {
	Index    int
	Price    float64
	OrderID  string    // ID активного ордера на бирже
	Side     string    // "buy" или "sell"
	Filled   bool
	FilledAt time.Time
}

// GridState — состояние сетки для одного символа.
type GridState struct {
	Symbol       string
	LowerBound   float64
	UpperBound   float64
	StopLoss     float64
	Ratio        float64
	Levels       []*GridLevel
	QtyPerLevel  float64     // объём в Base Currency на каждый уровень
	StartedAt    time.Time
	Active       bool
	CurrentPrice float64
	PriceCh      chan float64 // канал цен от OnTicker для watchGrid

	// Статистика прибыли
	TotalPnL    float64 // накопленный реализованный PnL за сессию (USDT)
	FilledCycles int    // количество завершённых циклов buy→sell
}
