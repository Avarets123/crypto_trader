package exchange_orders

import (
	"strconv"
	"sync"
	"time"

	"github.com/osman/bot-traider/internal/shared/utils"
	"go.uber.org/zap"
)

// TradeStats — агрегированная статистика сделок за интервал.
type TradeStats struct {
	BuyVolume  float64
	SellVolume float64
	BuyCount   int
	SellCount  int
}

// tradeEvent — одна сделка с меткой времени для скользящего окна.
type tradeEvent struct {
	t     time.Time
	vol   float64
	isBuy bool
}

// TradeAggregator накапливает объём покупок/продаж по символам в памяти.
// Потокобезопасен. Хранит как суммарную статистику (с момента подписки),
// так и список событий с таймстемпом для вычисления скользящего окна.
type TradeAggregator struct {
	mu     sync.Mutex
	stats  map[string]*TradeStats
	events map[string][]tradeEvent
}

// NewTradeAggregator создаёт TradeAggregator.
func NewTradeAggregator() *TradeAggregator {
	return &TradeAggregator{
		stats:  make(map[string]*TradeStats),
		events: make(map[string][]tradeEvent),
	}
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
	isBuy := order.Side == "buy"
	if isBuy {
		s.BuyVolume += vol
		s.BuyCount++
	} else {
		s.SellVolume += vol
		s.SellCount++
	}
	a.events[order.Symbol] = append(a.events[order.Symbol], tradeEvent{
		t:     time.Now(),
		vol:   vol,
		isBuy: isBuy,
	})
	a.mu.Unlock()
}

// Get возвращает суммарную статистику по символу с момента подписки (без сброса).
func (a *TradeAggregator) Get(symbol string, log *zap.Logger) TradeStats {
	defer utils.TimeTracker(log, "Get; exchange_orders aggregator", true)()

	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.stats[symbol]
	if !ok {
		return TradeStats{}
	}
	return *s
}

// GetWindow возвращает статистику только за последние window времени.
// Одновременно удаляет устаревшие события из памяти.
func (a *TradeAggregator) GetWindow(symbol string, window time.Duration, log *zap.Logger) TradeStats {
	defer utils.TimeTracker(log, "GetWindow; exchange_orders aggregator", true)()

	cutoff := time.Now().Add(-window)

	a.mu.Lock()
	defer a.mu.Unlock()

	evs, ok := a.events[symbol]
	if !ok {
		return TradeStats{}
	}

	// Найти первый актуальный индекс (события отсортированы по времени).
	first := 0
	for first < len(evs) && evs[first].t.Before(cutoff) {
		first++
	}
	// Обрезать устаревшие события.
	if first > 0 {
		evs = evs[first:]
		a.events[symbol] = evs
	}

	var result TradeStats
	for _, e := range evs {
		if e.isBuy {
			result.BuyVolume += e.vol
			result.BuyCount++
		} else {
			result.SellVolume += e.vol
			result.SellCount++
		}
	}
	return result
}

// Flush возвращает суммарную статистику по символу и сбрасывает её.
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

// Reset сбрасывает всю накопленную статистику и события по символу.
func (a *TradeAggregator) Reset(symbol string) {
	a.mu.Lock()
	delete(a.stats, symbol)
	delete(a.events, symbol)
	a.mu.Unlock()
}
