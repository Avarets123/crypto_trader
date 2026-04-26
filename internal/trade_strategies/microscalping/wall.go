package microscalping

import (
	"strconv"
	"sync"
	"time"

	"github.com/osman/bot-traider/internal/orderbook"
	"go.uber.org/zap"
)

// WallEntry описывает «стену» в стакане — уровень с аномально высоким объёмом.
type WallEntry struct {
	Price      float64
	Volume     float64   // текущий объём уровня
	InitVolume float64   // объём при первом обнаружении
	BirthTime  time.Time // время первого обнаружения
	Side       string    // "ask" | "bid"
}

// Age возвращает возраст стены с момента первого обнаружения.
func (w *WallEntry) Age() time.Duration {
	return time.Since(w.BirthTime)
}

// AbsorptionPct возвращает процент поглощения объёма стены (0–100).
// 100 = полностью поглощена, 0 = не изменилась или выросла.
func (w *WallEntry) AbsorptionPct() float64 {
	if w.InitVolume <= 0 {
		return 0
	}
	pct := (w.InitVolume - w.Volume) / w.InitVolume * 100
	if pct < 0 {
		return 0
	}
	return pct
}

// IsGrowing возвращает true если текущий объём вырос относительно начального.
func (w *WallEntry) IsGrowing() bool {
	return w.Volume > w.InitVolume
}

// WallTracker отслеживает стены в стакане для одного символа.
type WallTracker struct {
	mu       sync.Mutex
	askWalls map[float64]*WallEntry
	bidWalls map[float64]*WallEntry
	log      *zap.Logger
}

// AskWallCount возвращает текущее число отслеживаемых Ask-стен.
func (wt *WallTracker) AskWallCount() int {
	wt.mu.Lock()
	defer wt.mu.Unlock()
	return len(wt.askWalls)
}

func newWallTracker(log *zap.Logger) *WallTracker {
	return &WallTracker{
		askWalls: make(map[float64]*WallEntry),
		bidWalls: make(map[float64]*WallEntry),
		log:      log,
	}
}

// Update обновляет карту стен на основе текущего снимка стакана.
// Новые уровни с объёмом > avg*anomalyMult получают BirthTime = now.
// Исчезнувшие уровни удаляются.
func (wt *WallTracker) Update(ob *orderbook.OrderBook, anomalyMult float64) {
	now := time.Now()
	askAvg := avgLevelVolume(ob.Asks)
	bidAvg := avgLevelVolume(ob.Bids)
	askThresh := askAvg * anomalyMult
	bidThresh := bidAvg * anomalyMult

	wt.mu.Lock()
	defer wt.mu.Unlock()

	// Ask walls
	seenAsk := make(map[float64]struct{}, 8)
	for _, e := range ob.Asks {
		p, _ := strconv.ParseFloat(e.Price, 64)
		q, _ := strconv.ParseFloat(e.Qty, 64)
		if p <= 0 || q <= 0 || askThresh <= 0 {
			continue
		}
		if q >= askThresh {
			seenAsk[p] = struct{}{}
			if wall, ok := wt.askWalls[p]; ok {
				wall.Volume = q
			} else {
				wt.askWalls[p] = &WallEntry{
					Price: p, Volume: q, InitVolume: q,
					BirthTime: now, Side: "ask",
				}
				wt.log.Debug("wall: new ask wall",
					zap.Float64("price", p),
					zap.Float64("volume", q),
					zap.Float64("threshold", askThresh),
				)
			}
		}
	}
	for p := range wt.askWalls {
		if _, ok := seenAsk[p]; !ok {
			delete(wt.askWalls, p)
		}
	}

	// Bid walls
	seenBid := make(map[float64]struct{}, 8)
	for _, e := range ob.Bids {
		p, _ := strconv.ParseFloat(e.Price, 64)
		q, _ := strconv.ParseFloat(e.Qty, 64)
		if p <= 0 || q <= 0 || bidThresh <= 0 {
			continue
		}
		if q >= bidThresh {
			seenBid[p] = struct{}{}
			if wall, ok := wt.bidWalls[p]; ok {
				wall.Volume = q
			} else {
				wt.bidWalls[p] = &WallEntry{
					Price: p, Volume: q, InitVolume: q,
					BirthTime: now, Side: "bid",
				}
				wt.log.Debug("wall: new bid wall",
					zap.Float64("price", p),
					zap.Float64("volume", q),
					zap.Float64("threshold", bidThresh),
				)
			}
		}
	}
	for p := range wt.bidWalls {
		if _, ok := seenBid[p]; !ok {
			delete(wt.bidWalls, p)
		}
	}
}

// GetAbsorbingAskWall возвращает ближайшую Ask стену выше abovePrice, которая:
// - старше minAgeSec секунд (прошла anti-spoofing фильтр)
// - поглощена на absorbPct% и более (её активно едят)
func (wt *WallTracker) GetAbsorbingAskWall(abovePrice float64, minAgeSec int, absorbPct float64) *WallEntry {
	minAge := time.Duration(minAgeSec) * time.Second
	wt.mu.Lock()
	defer wt.mu.Unlock()
	var best *WallEntry
	for _, wall := range wt.askWalls {
		if wall.Price <= abovePrice {
			continue
		}
		if wall.Age() < minAge {
			continue
		}
		if wall.AbsorptionPct() < absorbPct {
			continue
		}
		if best == nil || wall.Price < best.Price {
			best = wall
		}
	}
	return best
}

// GetStableAskWall возвращает ближайшую Ask стену выше abovePrice, которая:
// - старше minAgeSec секунд
// - не убывает (объём не уменьшился относительно InitVolume)
func (wt *WallTracker) GetStableAskWall(abovePrice float64, minAgeSec int) *WallEntry {
	minAge := time.Duration(minAgeSec) * time.Second
	wt.mu.Lock()
	defer wt.mu.Unlock()
	var best *WallEntry
	for _, wall := range wt.askWalls {
		if wall.Price <= abovePrice {
			continue
		}
		if wall.Age() < minAge {
			continue
		}
		if wall.AbsorptionPct() > 0 {
			continue // убывает — игнорируем
		}
		if best == nil || wall.Price < best.Price {
			best = wall
		}
	}
	return best
}

// GetBidSupportWall возвращает ближайшую Bid стену поддержки ниже belowPrice,
// старше minAgeSec секунд.
func (wt *WallTracker) GetBidSupportWall(belowPrice float64, minAgeSec int) *WallEntry {
	minAge := time.Duration(minAgeSec) * time.Second
	wt.mu.Lock()
	defer wt.mu.Unlock()
	var best *WallEntry
	for _, wall := range wt.bidWalls {
		if wall.Price >= belowPrice {
			continue
		}
		if wall.Age() < minAge {
			continue
		}
		if best == nil || wall.Price > best.Price {
			best = wall
		}
	}
	return best
}

// GetNewAskWall возвращает Ask стену выше abovePrice, которая:
// - старше minAgeSec (прошла anti-spoofing)
// - моложе maxAgeSec (новая стена, сигнал для шорта)
// - не убывает
func (wt *WallTracker) GetNewAskWall(abovePrice float64, minAgeSec, maxAgeSec int) *WallEntry {
	minAge := time.Duration(minAgeSec) * time.Second
	maxAge := time.Duration(maxAgeSec) * time.Second
	wt.mu.Lock()
	defer wt.mu.Unlock()
	var best *WallEntry
	for _, wall := range wt.askWalls {
		if wall.Price <= abovePrice {
			continue
		}
		age := wall.Age()
		if age < minAge || age > maxAge {
			continue
		}
		if wall.AbsorptionPct() > 0 {
			continue
		}
		if best == nil || wall.Price < best.Price {
			best = wall
		}
	}
	return best
}

// avgLevelVolume вычисляет средний объём уровней стакана.
func avgLevelVolume(entries []orderbook.Entry) float64 {
	if len(entries) == 0 {
		return 0
	}
	var sum float64
	var n int
	for _, e := range entries {
		q, _ := strconv.ParseFloat(e.Qty, 64)
		if q > 0 {
			sum += q
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
