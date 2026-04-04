package comparator

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/ticker"
)

const eventsBuffer = 1024

type actionKind int8

const (
	actionOpen actionKind = iota
	actionClose
	actionUpdateMax
)

type spreadAction struct {
	kind       actionKind
	event      *SpreadEvent // указатель на SpreadEvent из activeSpreads
	closedAt   time.Time    // для actionClose
	durationMs int64        // для actionClose
	newMaxPct  float64      // для actionUpdateMax
}

// symbolPrices хранит цены и активные спреды для одного символа.
type symbolPrices struct {
	mu            sync.Mutex
	prices        map[string]float64     // exchange → price
	activeSpreads map[string]*SpreadEvent // pairKey → активный спред
}

// PriceComparator отслеживает цены символов по биржам и логирует межбиржевой спред.
type PriceComparator struct {
	mu            sync.RWMutex
	symbols       map[string]*symbolPrices
	threshold     float64
	log           *zap.Logger
	repo          *SpreadRepository
	ctx           context.Context
	events        chan spreadAction
	onSpreadOpen  func()
}

// WithOnSpreadOpen устанавливает хук, вызываемый при каждом новом обнаруженном спреде.
func (c *PriceComparator) WithOnSpreadOpen(fn func()) {
	c.onSpreadOpen = fn
}

// New создаёт PriceComparator с заданным порогом спреда в процентах.
// repo может быть nil — тогда события не сохраняются в БД.
func New(ctx context.Context, threshold float64, repo *SpreadRepository, log *zap.Logger) *PriceComparator {
	c := &PriceComparator{
		symbols:   make(map[string]*symbolPrices),
		threshold: threshold,
		log:       log,
		repo:      repo,
		ctx:       ctx,
	}
	if repo != nil {
		c.events = make(chan spreadAction, eventsBuffer)
		go c.processEvents()
	}
	return c
}

// processEvents читает из канала и пишет в PostgreSQL в отдельной горутине.
func (c *PriceComparator) processEvents() {
	handle := func(a spreadAction) {
		switch a.kind {
		case actionOpen:
			c.repo.OpenSpread(c.ctx, a.event)
		case actionClose:
			if a.event.ID == 0 {
				return // open был дропнут — нечего закрывать
			}
			c.repo.CloseSpread(c.ctx, a.event.ID, a.closedAt, a.durationMs)
		case actionUpdateMax:
			if a.event.ID == 0 {
				return
			}
			c.repo.UpdateMaxSpread(c.ctx, a.event.ID, a.newMaxPct)
		}
	}

	for {
		select {
		case a := <-c.events:
			handle(a)
		case <-c.ctx.Done():
			// дочитываем остаток канала перед выходом
			for {
				select {
				case a := <-c.events:
					handle(a)
				default:
					return
				}
			}
		}
	}
}

// enqueue отправляет действие в канал. Неблокирующий: при переполнении — дропает.
func (c *PriceComparator) enqueue(a spreadAction) {
	select {
	case c.events <- a:
	default:
		c.log.Warn("spread: events queue full, event dropped",
			zap.String("symbol", a.event.Symbol),
		)
	}
}

// getOrCreate возвращает существующий symbolPrices или создаёт новый.
func (c *PriceComparator) getOrCreate(symbol string) *symbolPrices {
	c.mu.RLock()
	sp, ok := c.symbols[symbol]
	c.mu.RUnlock()
	if ok {
		return sp
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if sp, ok = c.symbols[symbol]; ok {
		return sp
	}
	sp = &symbolPrices{
		prices:        make(map[string]float64),
		activeSpreads: make(map[string]*SpreadEvent),
	}
	c.symbols[symbol] = sp
	return sp
}

// Update обновляет цену тикера и проверяет спред по всем парам бирж.
func (c *PriceComparator) Update(t ticker.Ticker) {
	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || price <= 0 {
		return
	}

	sp := c.getOrCreate(t.Symbol)

	sp.mu.Lock()
	sp.prices[t.Exchange] = price
	snapshot := make(map[string]float64, len(sp.prices))
	for ex, p := range sp.prices {
		snapshot[ex] = p
	}
	sp.mu.Unlock()

	c.checkSpread(sp, t.Symbol, snapshot)
}

// checkSpread проверяет все пары бирж и управляет жизненным циклом спредов.
func (c *PriceComparator) checkSpread(sp *symbolPrices, symbol string, prices map[string]float64) {
	exchanges := make([]string, 0, len(prices))
	for ex := range prices {
		exchanges = append(exchanges, ex)
	}

	for i := 0; i < len(exchanges); i++ {
		for j := i + 1; j < len(exchanges); j++ {
			exA, exB := exchanges[i], exchanges[j]
			pA, pB := prices[exA], prices[exB]

			absSpread := math.Round(math.Abs(pA-pB)/pB*100*100) / 100
			key := pairKey(exA, exB)

			sp.mu.Lock()
			active, exists := sp.activeSpreads[key]

			if absSpread >= c.threshold {
				exHigh, exLow, pHigh, pLow := orderedPair(exA, exB, pA, pB)

				if !exists {
					event := &SpreadEvent{
						Symbol:       symbol,
						ExchangeHigh: exHigh,
						ExchangeLow:  exLow,
						OpenedAt:     time.Now(),
						MaxSpreadPct: absSpread,
						PriceHigh:    pHigh,
						PriceLow:     pLow,
					}
					sp.activeSpreads[key] = event
					sp.mu.Unlock()

					c.log.Warn("spread opened",
						zap.String("symbol", symbol),
						zap.String("exchange_high", exHigh),
						zap.String("exchange_low", exLow),
						zap.Float64("price_high", pHigh),
						zap.Float64("price_low", pLow),
						zap.Float64("spread_pct", absSpread),
					)
					if c.onSpreadOpen != nil {
						c.onSpreadOpen()
					}
					if c.events != nil {
						c.enqueue(spreadAction{kind: actionOpen, event: event})
					}
				} else if absSpread > active.MaxSpreadPct {
					active.MaxSpreadPct = absSpread
					sp.mu.Unlock()

					if c.events != nil {
						c.enqueue(spreadAction{kind: actionUpdateMax, event: active, newMaxPct: absSpread})
					}
				} else {
					sp.mu.Unlock()
				}
			} else if exists {
				delete(sp.activeSpreads, key)
				closedAt := time.Now()
				durationMs := closedAt.Sub(active.OpenedAt).Milliseconds()
				sp.mu.Unlock()

				c.log.Info("spread closed",
					zap.String("symbol", symbol),
					zap.String("pair", key),
					zap.Float64("max_spread_pct", active.MaxSpreadPct),
					zap.Int64("duration_ms", durationMs),
				)
				if c.events != nil {
					c.enqueue(spreadAction{kind: actionClose, event: active, closedAt: closedAt, durationMs: durationMs})
				}
			} else {
				sp.mu.Unlock()
			}
		}
	}
}

// pairKey возвращает детерминированный ключ для пары бирж (порядок не важен).
func pairKey(a, b string) string {
	if a < b {
		return a + "|" + b
	}
	return b + "|" + a
}

// orderedPair возвращает биржу/цену с бо́льшей ценой первой.
func orderedPair(exA, exB string, pA, pB float64) (exHigh, exLow string, pHigh, pLow float64) {
	if pA >= pB {
		return exA, exB, pA, pB
	}
	return exB, exA, pB, pA
}
