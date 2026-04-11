package volatile

import "time"

// VolatileTrade описывает одну открытую сделку Volatile стратегии.
type VolatileTrade struct {
	ID         int64
	Symbol     string
	Exchange   string    // биржа где куплен актив
	EntryPrice float64
	PeakPrice  float64   // максимальная цена с момента входа (для trailing stop)
	Qty        float64
	BullScore  float64   // значение BullScore в момент входа
	OpenedAt   time.Time
	PriceCh    chan float64  // канал текущих цен для горутины мониторинга
	CrashCh    chan struct{} // сигнал crash-события для немедленного выхода
}
