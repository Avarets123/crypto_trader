package momentum

import "time"

// MomentumTrade описывает одну открытую сделку Momentum стратегии.
type MomentumTrade struct {
	ID             int64
	Symbol         string
	TradeExchange  string  // биржа где куплен актив
	SignalExchange string  // биржа-источник pump-сигнала
	EntryPrice     float64
	PeakPrice      float64   // максимальная цена с момента входа (для trailing stop)
	Qty            float64
	OpenedAt       time.Time
	PriceCh        chan float64  // канал текущих цен для горутины мониторинга
	CrashCh        chan struct{} // сигнал crash-события для немедленного выхода
}
