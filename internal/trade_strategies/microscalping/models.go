package microscalping

import "time"

// MicroscalpingTrade описывает одну открытую сделку микроскальпинг стратегии.
type MicroscalpingTrade struct {
	ID           int64
	Symbol       string
	Exchange     string
	Side         string      // "long" | "short"
	EntryPrice   float64
	TPPrice      float64     // целевая цена тейк-профита
	TPTargetPct  float64     // целевой % прибыли (для информации)
	SupportWall  float64     // уровень Bid-стены поддержки (SL для лонга; 0 = не найдена)
	Qty          float64
	OpenedAt     time.Time
	HighestPrice float64     // максимальная цена с момента входа (для trailing stop лонга)
	TrailActive  bool        // флаг: trailing stop активирован
	TrailSL      float64     // текущий уровень trailing SL (0 = не активен)
	PriceCh      chan float64 // канал текущих цен
	CrashCh      chan struct{} // сигнал crash-события
}
