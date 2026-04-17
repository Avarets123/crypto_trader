package tinkoff_daytrading

import (
	"sync"
	"time"
)

const (
	signalWindowDur = 5 * time.Second
	whaleWindow     = 10 * time.Second
)

// SignalWindow отслеживает временны́е метки выполнения условий входа
// для проверки 5-секундного окна совпадения всех трёх условий.
type SignalWindow struct {
	mu sync.Mutex

	longImbalance time.Time
	longWhale     time.Time
	longDelta     time.Time

	shortImbalance time.Time
	shortWhale     time.Time
	shortDelta     time.Time
}

func newSignalWindow() *SignalWindow {
	return &SignalWindow{}
}

// UpdateLong обновляет временны́е метки условий лонг-входа.
func (sw *SignalWindow) UpdateLong(imbalanceOK, whaleOK, deltaOK bool, now time.Time) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if imbalanceOK {
		sw.longImbalance = now
	}
	if whaleOK {
		sw.longWhale = now
	}
	if deltaOK {
		sw.longDelta = now
	}
}

// UpdateShort обновляет временны́е метки условий шорт-входа.
func (sw *SignalWindow) UpdateShort(imbalanceOK, whaleOK, deltaOK bool, now time.Time) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if imbalanceOK {
		sw.shortImbalance = now
	}
	if whaleOK {
		sw.shortWhale = now
	}
	if deltaOK {
		sw.shortDelta = now
	}
}

// IsLong возвращает true если все три лонг-условия выполнены в пределах signalWindowDur.
func (sw *SignalWindow) IsLong(now time.Time) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return allWithinWindow(now, signalWindowDur, sw.longImbalance, sw.longWhale, sw.longDelta)
}

// IsShort возвращает true если все три шорт-условия выполнены в пределах signalWindowDur.
func (sw *SignalWindow) IsShort(now time.Time) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return allWithinWindow(now, signalWindowDur, sw.shortImbalance, sw.shortWhale, sw.shortDelta)
}

// Reset сбрасывает все временны́е метки.
func (sw *SignalWindow) Reset() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	*sw = SignalWindow{}
}

// allWithinWindow возвращает true если:
// 1. ни одна метка не нулевая
// 2. все метки не старше window*2 от now
// 3. разрыв между самой ранней и самой поздней ≤ window
func allWithinWindow(now time.Time, window time.Duration, times ...time.Time) bool {
	var earliest, latest time.Time
	for _, t := range times {
		if t.IsZero() {
			return false
		}
		if now.Sub(t) > window*2 {
			return false
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
		if latest.IsZero() || t.After(latest) {
			latest = t
		}
	}
	return latest.Sub(earliest) <= window
}
