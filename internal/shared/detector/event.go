package detector

import "time"

// DetectorEvent описывает одно обнаруженное ценовое движение.
type DetectorEvent struct {
	ID             int64
	Type           string    // "pump" или "crash"
	Symbol         string
	Exchange       string
	DetectedAt     time.Time
	WindowSec      int
	PriceBefore    float64
	PriceNow       float64
	ChangePct      float64 // изменение за окно детектора (%)
	TickerChangePct string  // изменение за 24ч от биржи (строка, например "+1.23%")
}
