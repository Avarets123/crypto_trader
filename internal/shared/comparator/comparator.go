package comparator

import (
	"math"
	"strconv"
	"sync"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/ticker"
)

// symbolPrices хранит цены по биржам для одного символа со своим мутексом.
type symbolPrices struct {
	mu     sync.RWMutex
	prices map[string]float64 // exchange → price
}

// PriceComparator отслеживает цены символов по биржам и логирует межбиржевой спред.
type PriceComparator struct {
	mu        sync.RWMutex
	symbols   map[string]*symbolPrices // symbol → *symbolPrices
	threshold float64
	log       *zap.Logger
}

// New создаёт PriceComparator с заданным порогом спреда в процентах.
func New(threshold float64, log *zap.Logger) *PriceComparator {
	return &PriceComparator{
		symbols:   make(map[string]*symbolPrices),
		threshold: threshold,
		log:       log,
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
	// двойная проверка после захвата write-lock
	if sp, ok = c.symbols[symbol]; ok {
		return sp
	}
	sp = &symbolPrices{prices: make(map[string]float64)}
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

	c.checkSpread(t.Symbol, snapshot)
}

// checkSpread проверяет все пары бирж для символа и логирует спред >= порога.
func (c *PriceComparator) checkSpread(symbol string, prices map[string]float64) {
	exchanges := make([]string, 0, len(prices))
	for ex := range prices {
		exchanges = append(exchanges, ex)
	}

	// итерируем по уникальным парам (i, j), i < j — без дублей
	for i := 0; i < len(exchanges); i++ {
		for j := i + 1; j < len(exchanges); j++ {
			exA, exB := exchanges[i], exchanges[j]
			pA, pB := prices[exA], prices[exB]

			spread := (pA - pB) / pB * 100
			absSpread := math.Abs(spread)

			if absSpread < c.threshold {
				continue
			}

			exHigh, exLow := exA, exB
			pHigh, pLow := pA, pB
			if pB > pA {
				exHigh, exLow = exB, exA
				pHigh, pLow = pB, pA
			}

			c.log.Warn("price spread detected",
				zap.String("symbol", symbol),
				zap.String("exchange_high", exHigh),
				zap.String("exchange_low", exLow),
				zap.Float64("price_high", pHigh),
				zap.Float64("price_low", pLow),
				zap.Float64("spread_pct", math.Round(absSpread*100)/100),
			)
		}
	}
}
