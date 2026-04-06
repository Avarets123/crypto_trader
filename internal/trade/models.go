package trade

import (
	"encoding/json"
	"time"
)

// Trade описывает одну сделку (тестовую или реальную).
type Trade struct {
	ID             int64
	Strategy       string          // 'arb', 'pump'
	Mode           string          // 'test' | 'prod'
	SignalExchange string          // биржа-источник сигнала ('binance')
	TradeExchange  string          // биржа исполнения ('bybit')
	Symbol         string
	Qty            float64
	EntryPrice     float64
	TargetPrice    *float64        // цель TP
	StopLossPrice  *float64        // уровень SL
	ExitPrice      *float64
	ExitReason     string          // 'tp' | 'sl' | 'timeout' | 'manual'
	PnlUSDT        *float64
	EntryOrderID   string          // ID ордера открытия (пусто в test-режиме)
	ExitOrderID    string          // ID ордера закрытия (пусто в test-режиме)
	SignalData     json.RawMessage // сырой JSON сигнала
	OpenedAt       time.Time
	ClosedAt       *time.Time
}


// // OpenTrade — параметры открытия позиции.
// type OpenTrade struct {
// 	Strategy       string
// 	SignalExchange string
// 	TradeExchange  string
// 	Symbol         string
// 	Qty            float64
// 	EntryPrice     float64
// 	TargetPrice    *float64
// 	StopLossPrice  *float64
// 	SignalData     json.RawMessage
// }

// // inMemoryTrade — открытая позиция в памяти.
// type inMemoryTrade struct {
// 	id             int64
// 	strategy       string
// 	mode           string
// 	signalExchange string
// 	tradeExchange  string
// 	symbol         string
// 	qty            float64
// 	entryPrice     float64
// 	targetPrice    *float64
// 	stopLossPrice  *float64
// 	entryOrderID   string
// 	signalData     json.RawMessage
// 	openedAt       time.Time
// }