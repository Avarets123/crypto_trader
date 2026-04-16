package trade

import "time"

// Trade описывает одну сделку (тестовую или реальную).
type Trade struct {
	ID             int64
	Strategy       string   // 'arb', 'pump'
	Mode           string   // 'test' | 'prod'
	SignalExchange string   // биржа-источник сигнала ('binance')
	TradeExchange  string   // биржа исполнения ('binance')
	Symbol         string
	Side           string   // "buy" (лонг) | "sell" (шорт); по умолчанию "buy"
	Qty            float64
	EntryPrice     float64
	TargetPrice    *float64 // цель TP
	StopLossPrice  *float64 // уровень SL
	ExitPrice      *float64
	ExitReason     string   // 'tp' | 'sl' | 'timeout' | 'manual'
	PnlUSDT        *float64
	CommissionUSDT *float64 // суммарная комиссия (вход + выход, 0.1% за сторону)
	EntryOrderID   string   // ID ордера открытия (пусто в test-режиме)
	ExitOrderID    string   // ID ордера закрытия (пусто в test-режиме)
	SpreadID       *int64   // ID спреда из таблицы spreads, с которого открыта сделка
	OpenedAt       time.Time
	ClosedAt       *time.Time
}
