package microscalping

import (
	"sync"
	"time"
)

const (
	cdWindow1m       = 60 * time.Second
	volClusterWindow = 3 * time.Minute
	priceHistWindow  = 15 * time.Minute
	tradeHistWindow  = time.Hour
	maxTrades500     = 500
)

type tradeEntry struct {
	t       time.Time
	qty     float64 // базовая валюта
	qtyUSDT float64 // объём в USDT (qty * price)
	isBuy   bool
}

type priceEntry struct {
	t     time.Time
	price float64
}

// SymbolMetrics отслеживает скользящие метрики для одного символа:
// CD_1m (кумулятивная дельта за 1 мин), VolumeCluster (3 мин),
// история цен (15 мин), средний объём сделки (последние 500 сделок),
// EMA цены (фильтр тренда).
type SymbolMetrics struct {
	mu          sync.Mutex
	trades1h    []tradeEntry // сделки за последний час (для CD, VolumeCluster)
	trades500   []tradeEntry // последние 500 сделок (для AvgTradeSizeUSDT)
	priceHist   []priceEntry // цены за последние 15 минут
	ema         float64      // экспоненциальная скользящая средняя цены
	emaInited   bool         // флаг: EMA получила первое значение
	emaPeriod   int          // период EMA в количестве точек
	lastEmaTick time.Time    // последний апдейт EMA — для дросселирования
}

func newSymbolMetrics() *SymbolMetrics {
	return newSymbolMetricsWithEMA(20)
}

func newSymbolMetricsWithEMA(period int) *SymbolMetrics {
	if period < 2 {
		period = 2
	}
	return &SymbolMetrics{emaPeriod: period}
}

// OnTrade регистрирует новую сделку (qty в базовой валюте, price в USDT).
func (m *SymbolMetrics) OnTrade(qty, price float64, isBuy bool) {
	now := time.Now()
	e := tradeEntry{
		t:       now,
		qty:     qty,
		qtyUSDT: qty * price,
		isBuy:   isBuy,
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.trades1h = append(m.trades1h, e)
	cutoff1h := now.Add(-tradeHistWindow)
	first := 0
	for first < len(m.trades1h) && m.trades1h[first].t.Before(cutoff1h) {
		first++
	}
	if first > 0 {
		m.trades1h = m.trades1h[first:]
	}

	m.trades500 = append(m.trades500, e)
	if len(m.trades500) > maxTrades500 {
		m.trades500 = m.trades500[len(m.trades500)-maxTrades500:]
	}
}

// OnPrice регистрирует текущую цену для истории 15 минут (для ShortPumpPct)
// и обновляет EMA цены не чаще одного раза в секунду (грубый эквивалент 1-сек таймфрейма).
func (m *SymbolMetrics) OnPrice(price float64) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.priceHist = append(m.priceHist, priceEntry{t: now, price: price})
	cutoff := now.Add(-priceHistWindow)
	first := 0
	for first < len(m.priceHist) && m.priceHist[first].t.Before(cutoff) {
		first++
	}
	if first > 0 {
		m.priceHist = m.priceHist[first:]
	}

	// EMA: дросселируем до 1 апдейта в секунду, чтобы период EMA соответствовал секундам.
	if !m.emaInited {
		m.ema = price
		m.emaInited = true
		m.lastEmaTick = now
	} else if now.Sub(m.lastEmaTick) >= time.Second {
		alpha := 2.0 / (float64(m.emaPeriod) + 1.0)
		m.ema = alpha*price + (1-alpha)*m.ema
		m.lastEmaTick = now
	}
}

// EMA возвращает текущее значение EMA и флаг готовности (после WarmupSec данные стабильны).
func (m *SymbolMetrics) EMA() (float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ema, m.emaInited
}

// CD1m возвращает кумулятивную дельту объёма (USDT) за последнюю минуту.
// Положительное значение — давление покупателей, отрицательное — продавцов.
func (m *SymbolMetrics) CD1m() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-cdWindow1m)
	var cd float64
	for i := len(m.trades1h) - 1; i >= 0; i-- {
		e := m.trades1h[i]
		if e.t.Before(cutoff) {
			break
		}
		if e.isBuy {
			cd += e.qtyUSDT
		} else {
			cd -= e.qtyUSDT
		}
	}
	return cd
}

// VolumeCluster3m возвращает суммарный объём сделок (USDT) за последние 3 минуты.
func (m *SymbolMetrics) VolumeCluster3m() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-volClusterWindow)
	var vol float64
	for i := len(m.trades1h) - 1; i >= 0; i-- {
		e := m.trades1h[i]
		if e.t.Before(cutoff) {
			break
		}
		vol += e.qtyUSDT
	}
	return vol
}

// VolumeCluster1hAvg возвращает среднее VolumeCluster3m за последний час.
// Рассчитывается как суммарный объём за час / 20 (количество 3-минутных периодов в часе).
func (m *SymbolMetrics) VolumeCluster1hAvg() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var total float64
	for _, e := range m.trades1h {
		total += e.qtyUSDT
	}
	return total / 20.0
}

// AvgTradeSizeUSDT возвращает средний объём сделки (USDT) за последние 500 сделок.
func (m *SymbolMetrics) AvgTradeSizeUSDT() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.trades500)
	if n == 0 {
		return 0
	}
	var sum float64
	for _, e := range m.trades500 {
		sum += e.qtyUSDT
	}
	return sum / float64(n)
}

// PriceChangePct15m возвращает изменение цены за последние 15 минут в процентах.
// Возвращает 0 если истории недостаточно.
func (m *SymbolMetrics) PriceChangePct15m() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.priceHist) < 2 {
		return 0
	}
	oldest := m.priceHist[0].price
	newest := m.priceHist[len(m.priceHist)-1].price
	if oldest <= 0 {
		return 0
	}
	return (newest - oldest) / oldest * 100
}
