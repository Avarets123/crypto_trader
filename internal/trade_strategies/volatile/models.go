package volatile

import "time"

// VolatileTrade описывает одну открытую сделку микроскальпинг стратегии.
type VolatileTrade struct {
	ID         int64
	Symbol     string
	Exchange   string
	EntryPrice float64
	TPPrice    float64      // цена тейк-профита (стена сопротивления или fallback)
	Qty        float64
	OBI        float64      // OBI_1pct в момент входа
	OpenedAt   time.Time
	PriceCh    chan float64  // канал текущих цен для горутины мониторинга
	CrashCh    chan struct{} // сигнал crash-события для немедленного выхода
}
