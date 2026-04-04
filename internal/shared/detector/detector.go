package detector

import (
	"context"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/ticker"
)

const eventsBuffer = 1024

// pricePoint — одна точка в скользящем окне.
type pricePoint struct {
	price float64
	ts    time.Time
}

// symbolState хранит скользящее окно цен для одного символа на одной бирже.
type symbolState struct {
	mu     sync.Mutex
	prices []pricePoint
}

// Detector обнаруживает резкие ценовые движения (pump и flash crash).
type Detector struct {
	mu                sync.RWMutex
	symbols           map[string]*symbolState // ключ: "symbol|exchange"
	pumpThreshold     float64
	crashThreshold    float64
	windowSec         int
	log               *zap.Logger
	repo              *DetectorRepository
	ctx               context.Context
	events            chan *DetectorEvent
	onPump            func()
	onCrash           func()
}

// WithOnPump устанавливает хук, вызываемый при каждом обнаруженном pump.
func (d *Detector) WithOnPump(fn func()) {
	d.onPump = fn
}

// WithOnCrash устанавливает хук, вызываемый при каждом обнаруженном flash crash.
func (d *Detector) WithOnCrash(fn func()) {
	d.onCrash = fn
}

// New создаёт Detector с заданными параметрами.
// repo может быть nil — тогда события не сохраняются в БД.
func New(ctx context.Context, cfg Config, repo *DetectorRepository, log *zap.Logger) *Detector {
	d := &Detector{
		symbols:        make(map[string]*symbolState),
		pumpThreshold:  cfg.PumpThresholdPct,
		crashThreshold: cfg.CrashThresholdPct,
		windowSec:      cfg.WindowSec,
		log:            log,
		repo:           repo,
		ctx:            ctx,
	}
	if repo != nil {
		d.events = make(chan *DetectorEvent, eventsBuffer)
		go d.processEvents()
	}
	return d
}

// processEvents читает из канала и пишет в PostgreSQL в отдельной горутине.
func (d *Detector) processEvents() {
	handle := func(e *DetectorEvent) {
		d.repo.Save(d.ctx, e)
	}
	for {
		select {
		case e := <-d.events:
			handle(e)
		case <-d.ctx.Done():
			for {
				select {
				case e := <-d.events:
					handle(e)
				default:
					return
				}
			}
		}
	}
}

// enqueue отправляет событие в канал. Неблокирующий: при переполнении — дропает.
func (d *Detector) enqueue(e *DetectorEvent) {
	select {
	case d.events <- e:
	default:
		d.log.Warn("detector: events queue full, event dropped",
			zap.String("symbol", e.Symbol),
			zap.String("exchange", e.Exchange),
		)
	}
}

// getOrCreate возвращает существующий symbolState или создаёт новый.
func (d *Detector) getOrCreate(key string) *symbolState {
	d.mu.RLock()
	s, ok := d.symbols[key]
	d.mu.RUnlock()
	if ok {
		return s
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok = d.symbols[key]; ok {
		return s
	}
	s = &symbolState{}
	d.symbols[key] = s
	return s
}

// Update обновляет скользящее окно и проверяет пороги.
func (d *Detector) Update(t ticker.Ticker) {
	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || price <= 0 {
		return
	}

	key := t.Symbol + "|" + t.Exchange
	s := d.getOrCreate(key)

	now := time.Now()
	cutoff := now.Add(-time.Duration(d.windowSec) * time.Second)

	s.mu.Lock()
	// добавляем новую точку
	s.prices = append(s.prices, pricePoint{price: price, ts: now})
	// удаляем устаревшие точки из начала
	i := 0
	for i < len(s.prices) && s.prices[i].ts.Before(cutoff) {
		i++
	}
	s.prices = s.prices[i:]

	if len(s.prices) < 2 {
		s.mu.Unlock()
		return
	}

	priceBefore := s.prices[0].price
	s.mu.Unlock()

	changePct := (price - priceBefore) / priceBefore * 100

	if changePct >= d.pumpThreshold {
		e := &DetectorEvent{
			Type:        "pump",
			Symbol:      t.Symbol,
			Exchange:    t.Exchange,
			DetectedAt:  now,
			WindowSec:   d.windowSec,
			PriceBefore: priceBefore,
			PriceNow:    price,
			ChangePct:   changePct,
		}
		d.log.Warn("pump detected",
			zap.String("symbol", t.Symbol),
			zap.String("exchange", t.Exchange),
			zap.Float64("change_pct", changePct),
			zap.Float64("price_before", priceBefore),
			zap.Float64("price_now", price),
			zap.Int("window_sec", d.windowSec),
		)
		if d.onPump != nil {
			d.onPump()
		}
		if d.events != nil {
			d.enqueue(e)
		}
	} else if changePct <= -d.crashThreshold {
		e := &DetectorEvent{
			Type:        "crash",
			Symbol:      t.Symbol,
			Exchange:    t.Exchange,
			DetectedAt:  now,
			WindowSec:   d.windowSec,
			PriceBefore: priceBefore,
			PriceNow:    price,
			ChangePct:   changePct,
		}
		d.log.Warn("flash crash detected",
			zap.String("symbol", t.Symbol),
			zap.String("exchange", t.Exchange),
			zap.Float64("change_pct", changePct),
			zap.Float64("price_before", priceBefore),
			zap.Float64("price_now", price),
			zap.Int("window_sec", d.windowSec),
		)
		if d.onCrash != nil {
			d.onCrash()
		}
		if d.events != nil {
			d.enqueue(e)
		}
	}
}
