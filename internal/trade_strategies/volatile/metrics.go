package volatile

import (
	"math"
	"sync"
	"time"

	"github.com/osman/bot-traider/internal/shared/utils"
	"go.uber.org/zap"
)

const (
	cvdWindow5s  = 5 * time.Second
	tradeWin1min = 60 * time.Second
	cvdHistory1h = time.Hour
)

type tradeEntry struct {
	t     time.Time
	qty   float64 // объём в базовой валюте
	isBuy bool
}

type cvdSnapshot struct {
	t   time.Time
	val float64
}

// SymbolMetrics отслеживает скользящие метрики для одного символа:
// CVD_5s, AvgTradeVol_1min и историю CVD за час для расчёта порога.
type SymbolMetrics struct {
	mu         sync.Mutex
	trades     []tradeEntry  // сделки за последний час
	cvdHistory []cvdSnapshot // снимки CVD_5s за последний час
	lastQty    float64
	lastIsBuy  bool
	hasLast    bool
	log *zap.Logger
}

func newSymbolMetrics(log *zap.Logger) *SymbolMetrics {
	return &SymbolMetrics{
		log: log,
	}
}

// OnTrade регистрирует новую сделку и записывает снимок CVD_5s в историю.
func (m *SymbolMetrics) OnTrade(qty float64, isBuy bool) {
	defer utils.TimeTracker(m.log, "OnTrade volatile")()
	now := time.Now()
	cutoff := now.Add(-cvdHistory1h)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.trades = append(m.trades, tradeEntry{t: now, qty: qty, isBuy: isBuy})
	m.lastQty = qty
	m.lastIsBuy = isBuy
	m.hasLast = true

	// Обрезаем сделки старше 1 часа
	first := 0
	for first < len(m.trades) && m.trades[first].t.Before(cutoff) {
		first++
	}
	if first > 0 {
		m.trades = m.trades[first:]
	}

	// Записываем текущий снимок CVD_5s
	cvd := m.calcCVD5sLocked(now)
	m.cvdHistory = append(m.cvdHistory, cvdSnapshot{t: now, val: cvd})

	// Обрезаем старые снимки CVD
	first = 0
	for first < len(m.cvdHistory) && m.cvdHistory[first].t.Before(cutoff) {
		first++
	}
	if first > 0 {
		m.cvdHistory = m.cvdHistory[first:]
	}
}

// CVD5s возвращает кумулятивную дельту объёма за последние 5 секунд.
func (m *SymbolMetrics) CVD5s() float64 {
	defer utils.TimeTracker(m.log, "CDV5S volatile")()

	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calcCVD5sLocked(time.Now())
}

// calcCVD5sLocked вычисляет CVD_5s без захвата мьютекса (мьютекс уже должен быть захвачен).
// Итерируем с конца — сделки отсортированы по времени (новые в конце).
func (m *SymbolMetrics) calcCVD5sLocked(now time.Time) float64 {
	cutoff := now.Add(-cvdWindow5s)
	var cvd float64
	for i := len(m.trades) - 1; i >= 0; i-- {
		e := m.trades[i]
		if e.t.Before(cutoff) {
			break
		}
		if e.isBuy {
			cvd += e.qty
		} else {
			cvd -= e.qty
		}
	}
	return cvd
}

// AvgTradeVol1min возвращает средний объём сделки в базовой валюте за последнюю минуту.
func (m *SymbolMetrics) AvgTradeVol1min() float64 {
	defer utils.TimeTracker(m.log, "AvgTradeVol1min volatile")()

	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-tradeWin1min)
	var sum float64
	var count int
	for i := len(m.trades) - 1; i >= 0; i-- {
		e := m.trades[i]
		if e.t.Before(cutoff) {
			break
		}
		sum += e.qty
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// CVDStats возвращает среднее и стандартное отклонение CVD_5s за последний час.
func (m *SymbolMetrics) CVDStats() (avg, std float64) {
	defer utils.TimeTracker(m.log, "CVDStats volatile")()

	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.cvdHistory)
	if n == 0 {
		return 0, 0
	}
	var sum float64
	for _, s := range m.cvdHistory {
		sum += s.val
	}
	avg = sum / float64(n)
	var variance float64
	for _, s := range m.cvdHistory {
		d := s.val - avg
		variance += d * d
	}
	variance /= float64(n)
	std = math.Sqrt(variance)
	return
}

// CVDThreshold возвращает avg_CVD_1h + 2*std_CVD_1h — порог аномального CVD_5s.
func (m *SymbolMetrics) CVDThreshold() float64 {
	
	avg, std := m.CVDStats()
	return avg + 2*std
}

// AvgCVD1h возвращает среднее CVD_5s за последний час.
func (m *SymbolMetrics) AvgCVD1h() float64 {
	avg, _ := m.CVDStats()
	return avg
}

// GetLastTrade возвращает последнюю зарегистрированную сделку.
func (m *SymbolMetrics) GetLastTrade() (qty float64, isBuy bool, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasLast {
		return 0, false, false
	}
	return m.lastQty, m.lastIsBuy, true
}
