package scalping

import "time"

// ScalpTrade описывает одну открытую сделку Scalping стратегии.
type ScalpTrade struct {
	ID             int64
	Symbol         string
	Exchange       string
	EntryPrice     float64
	Qty            float64
	PeakPrice      float64  // максимальная цена с момента входа (для trailing stop)
	TrailingActive bool     // активирован ли trailing stop
	TrailingStop   float64  // текущий уровень trailing stop
	TakeProfit     float64
	StopLoss       float64
	OpenedAt       time.Time
	LimitOrderID   string      // ID лимитного ордера на вход (до исполнения)
	PriceCh        chan float64 // канал текущих цен от OnTicker
}
