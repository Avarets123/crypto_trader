package exchange_orders

import (
	"strconv"
	"sync"
)

// TradeStats — агрегированная статистика сделок за интервал.
type TradeStats struct {
	BuyVolume  float64
	SellVolume float64
	BuyCount   int
	SellCount  int
}

// TradeAggregator накапливает объём покупок/продаж по символам в памяти.
// Потокобезопасен. Используется для периодической отправки статистики в Telegram.
type TradeAggregator struct {
	mu    sync.Mutex
	stats map[string]*TradeStats
}

// NewTradeAggregator создаёт TradeAggregator.
func NewTradeAggregator() *TradeAggregator {
	return &TradeAggregator{stats: make(map[string]*TradeStats)}
}

// OnTrade добавляет сделку в накопитель.
func (a *TradeAggregator) OnTrade(order ExchangeOrder) {
	price, _ := strconv.ParseFloat(order.Price, 64)
	qty, _ := strconv.ParseFloat(order.Quantity, 64)
	vol := price * qty

	a.mu.Lock()
	s, ok := a.stats[order.Symbol]
	if !ok {
		s = &TradeStats{}
		a.stats[order.Symbol] = s
	}
	if order.Side == "buy" {
		s.BuyVolume += vol
		s.BuyCount++
	} else {
		s.SellVolume += vol
		s.SellCount++
	}
	a.mu.Unlock()
}

// Flush возвращает накопленную статистику по символу и сбрасывает её.
func (a *TradeAggregator) Flush(symbol string) TradeStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.stats[symbol]
	if !ok {
		return TradeStats{}
	}
	result := *s
	delete(a.stats, symbol)
	return result
}
