package tinkoff_daytrading

import (
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/osman/bot-traider/internal/exchange_orders"
	"github.com/osman/bot-traider/internal/orderbook"
)

const (
	tradeHistoryCap = 100
	deltaWindow     = 60 * time.Second
	historyWindow   = 2 * deltaWindow
	streamTimeout   = 10 * time.Second
	whaleMultiplier = 3.0
	imbalanceLevels = 5
	wallLevels      = 10
	wallMultiplier  = 5.0
)

type tradeEntry struct {
	t   time.Time
	qty float64
	dir int // +1 buy, -1 sell
}

// whaleEvent — зафиксированная аномальная сделка.
type whaleEvent struct {
	side string // "buy" | "sell"
	t    time.Time
}

// MetricSnapshot — потокобезопасная копия метрик символа.
type MetricSnapshot struct {
	AvgTradeVolume float64
	Delta1m        float64
	Delta1mPrev    float64
	DeltaChange    float64
	Imbalance      float64
	BidWall        float64
	AskWall        float64
	BidPrice       float64
	AskPrice       float64
	LastWhale      *whaleEvent
}

// SymbolMetrics хранит состояние метрик для одного символа.
type SymbolMetrics struct {
	mu sync.Mutex

	// AvgTradeVolume — скользящее среднее по последним 100 сделкам
	recent    []float64
	recentSum float64

	// История сделок за последние 2 минуты (для delta1m и delta1mPrev)
	history       []tradeEntry
	lastTradeTime time.Time

	// Whale
	lastWhale *whaleEvent

	// Стакан
	imbalance float64
	bidWall   float64
	askWall   float64
	bidPrice  float64
	askPrice  float64
}

func newSymbolMetrics() *SymbolMetrics {
	return &SymbolMetrics{}
}

// OnTrade обрабатывает входящую сделку и обновляет метрики.
func (m *SymbolMetrics) OnTrade(o exchange_orders.ExchangeOrder) {
	m.mu.Lock()
	defer m.mu.Unlock()

	qty, err := strconv.ParseFloat(o.Quantity, 64)
	if err != nil || qty <= 0 {
		return
	}
	now := o.TradeTime

	// Сброс при обрыве стрима
	if !m.lastTradeTime.IsZero() && now.Sub(m.lastTradeTime) > streamTimeout {
		m.resetLocked()
	}
	m.lastTradeTime = now

	// AvgTradeVolume: скользящее среднее по 100 сделкам
	if len(m.recent) >= tradeHistoryCap {
		m.recentSum -= m.recent[0]
		m.recent = m.recent[1:]
	}
	m.recent = append(m.recent, qty)
	m.recentSum += qty

	// Направление
	var dir int
	switch o.Side {
	case "buy":
		dir = 1
	case "sell":
		dir = -1
	default:
		return
	}

	// Whale-детекция
	if avg := m.avgVolumeLocked(); avg > 0 && qty > avg*whaleMultiplier {
		m.lastWhale = &whaleEvent{side: o.Side, t: now}
	}

	// Добавить в историю и обрезать старее 2 минут
	m.history = append(m.history, tradeEntry{t: now, qty: qty, dir: dir})
	cutoff := now.Add(-historyWindow)
	start := 0
	for start < len(m.history) && m.history[start].t.Before(cutoff) {
		start++
	}
	m.history = m.history[start:]
}

// OnOrderBook обрабатывает снимок стакана и обновляет Imbalance и стены.
func (m *SymbolMetrics) OnOrderBook(ob *orderbook.OrderBook) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(ob.Bids) > 0 {
		m.bidPrice, _ = strconv.ParseFloat(ob.Bids[0].Price, 64)
	}
	if len(ob.Asks) > 0 {
		m.askPrice, _ = strconv.ParseFloat(ob.Asks[0].Price, 64)
	}

	// Imbalance по топ-5 уровням
	var bidSum, askSum float64
	for i := 0; i < imbalanceLevels && i < len(ob.Bids); i++ {
		q, _ := strconv.ParseFloat(ob.Bids[i].Qty, 64)
		bidSum += q
	}
	for i := 0; i < imbalanceLevels && i < len(ob.Asks); i++ {
		q, _ := strconv.ParseFloat(ob.Asks[i].Qty, 64)
		askSum += q
	}
	switch {
	case askSum == 0:
		m.imbalance = math.Inf(1)
	case bidSum == 0:
		m.imbalance = 0
	default:
		m.imbalance = bidSum / askSum
	}

	m.bidWall = detectWall(ob.Bids, wallLevels, false)
	m.askWall = detectWall(ob.Asks, wallLevels, true)
}

// Snapshot возвращает копию текущих метрик (потокобезопасно).
func (m *SymbolMetrics) Snapshot() MetricSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	d1m, d1mPrev := m.computeDeltasLocked(now)
	dc := deltaChangePct(d1m, d1mPrev)

	snap := MetricSnapshot{
		AvgTradeVolume: m.avgVolumeLocked(),
		Delta1m:        d1m,
		Delta1mPrev:    d1mPrev,
		DeltaChange:    dc,
		Imbalance:      m.imbalance,
		BidWall:        m.bidWall,
		AskWall:        m.askWall,
		BidPrice:       m.bidPrice,
		AskPrice:       m.askPrice,
	}
	if m.lastWhale != nil {
		w := *m.lastWhale
		snap.LastWhale = &w
	}
	return snap
}

func (m *SymbolMetrics) avgVolumeLocked() float64 {
	if len(m.recent) == 0 {
		return 0
	}
	return m.recentSum / float64(len(m.recent))
}

func (m *SymbolMetrics) computeDeltasLocked(now time.Time) (current, prev float64) {
	cutoff1 := now.Add(-deltaWindow)
	cutoff2 := now.Add(-historyWindow)
	for _, e := range m.history {
		val := e.qty * float64(e.dir)
		if e.t.After(cutoff1) {
			current += val
		} else if e.t.After(cutoff2) {
			prev += val
		}
	}
	return
}

func (m *SymbolMetrics) resetLocked() {
	m.history = nil
	m.recent = nil
	m.recentSum = 0
	m.lastWhale = nil
}

// detectWall находит ближайшую стену в срезе уровней стакана.
// nearestIsSmallest=true (Ask): возвращает наименьшую цену стены.
// nearestIsSmallest=false (Bid): возвращает наибольшую цену стены.
func detectWall(levels []orderbook.Entry, n int, nearestIsSmallest bool) float64 {
	if len(levels) < 2 {
		return 0
	}
	if n > len(levels) {
		n = len(levels)
	}
	var sum float64
	for i := 0; i < n; i++ {
		q, _ := strconv.ParseFloat(levels[i].Qty, 64)
		sum += q
	}
	avg := sum / float64(n)
	threshold := avg * wallMultiplier

	result := 0.0
	for _, e := range levels {
		q, _ := strconv.ParseFloat(e.Qty, 64)
		if q < threshold {
			continue
		}
		p, _ := strconv.ParseFloat(e.Price, 64)
		if result == 0 {
			result = p
		} else if nearestIsSmallest && p < result {
			result = p
		} else if !nearestIsSmallest && p > result {
			result = p
		}
	}
	return result
}

// wallDisappeared проверяет, исчезла ли стена на уровне wallPrice.
// isBid=true — проверяет сторону Bid (для SL лонга); false — Ask (для SL шорта).
// recentTradeAtWall — был ли трейд по этой цене за последние 2 секунды.
func wallDisappeared(wallPrice float64, isBid bool, ob *orderbook.OrderBook, recentTradeAtWall bool) bool {
	if wallPrice == 0 {
		return false
	}
	levels := ob.Bids
	if !isBid {
		levels = ob.Asks
	}

	n := wallLevels
	if n > len(levels) {
		n = len(levels)
	}
	if n == 0 {
		return recentTradeAtWall
	}
	var sum float64
	for i := 0; i < n; i++ {
		q, _ := strconv.ParseFloat(levels[i].Qty, 64)
		sum += q
	}
	threshold := (sum / float64(n)) * wallMultiplier

	for _, e := range levels {
		p, _ := strconv.ParseFloat(e.Price, 64)
		if math.Abs(p-wallPrice) > 0.0001 {
			continue
		}
		q, _ := strconv.ParseFloat(e.Qty, 64)
		return q < threshold && recentTradeAtWall
	}
	// Уровень исчез совсем
	return recentTradeAtWall
}

func deltaChangePct(current, prev float64) float64 {
	p := math.Abs(prev)
	c := math.Abs(current)
	if p == 0 {
		if c != 0 {
			return 1.0
		}
		return 0
	}
	return (c - p) / p
}
