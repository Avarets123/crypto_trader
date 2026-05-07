package exchange

import (
	"context"
	"time"
)

// Kline — одна свеча (OHLCV) c одной биржи.
type Kline struct {
	OpenTime  time.Time
	CloseTime time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}

// KlineProvider — интерфейс для получения исторических свечей.
// Выделен отдельно от RestClient, чтобы биржи без поддержки klines
// (например, Tinkoff в текущей реализации) не были обязаны его реализовывать.
//
// interval: "1m", "5m", "15m", "1h", "4h", "1d" и т.д. (формат биржи).
// limit: количество последних свечей (включая текущую формирующуюся).
type KlineProvider interface {
	GetKlines(ctx context.Context, symbol, interval string, limit int) ([]Kline, error)
}
