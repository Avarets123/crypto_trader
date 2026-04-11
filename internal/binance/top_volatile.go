package binance

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// TopVolatileProvider — единственный источник топ волатильных символов.
// Получает список один раз при старте, затем обновляет по таймеру.
// Все компоненты используют Symbols() / Tickers() из кэша без REST-запросов.
type TopVolatileProvider struct {
	mu      sync.RWMutex
	tickers []VolatileTicker
	rest    *RestClient
	limit   int
	log     *zap.Logger
}

// NewTopVolatileProvider создаёт провайдер.
func NewTopVolatileProvider(rest *RestClient, limit int, log *zap.Logger) *TopVolatileProvider {
	return &TopVolatileProvider{
		rest:  rest,
		limit: limit,
		log:   log,
	}
}

// Fetch делает первичную загрузку. Вызывается синхронно до старта остальных компонентов.
func (p *TopVolatileProvider) Fetch(ctx context.Context) error {
	tickers, err := p.rest.GetTopVolatile(ctx, p.limit)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.tickers = tickers
	p.mu.Unlock()
	p.log.Info("top volatile: loaded", zap.Int("count", len(tickers)))
	return nil
}

// Run запускает периодическое обновление кэша. Блокирует до ctx.Done().
func (p *TopVolatileProvider) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickers, err := p.rest.GetTopVolatile(ctx, p.limit)
			if err != nil {
				p.log.Warn("top volatile: refresh failed", zap.Error(err))
				continue
			}
			p.mu.Lock()
			p.tickers = tickers
			p.mu.Unlock()
			p.log.Info("top volatile: refreshed", zap.Int("count", len(tickers)))
		}
	}
}

// Tickers возвращает текущий список тикеров (копия).
func (p *TopVolatileProvider) Tickers() []VolatileTicker {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]VolatileTicker, len(p.tickers))
	copy(result, p.tickers)
	return result
}

// Symbols возвращает текущий список символов (копия).
func (p *TopVolatileProvider) Symbols() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]string, len(p.tickers))
	for i, t := range p.tickers {
		result[i] = t.Symbol
	}
	return result
}

// FetchSymbols возвращает закэшированный список символов без REST-запроса.
// Совместим с сигнатурой fetchFn для SymbolWatcher.
func (p *TopVolatileProvider) FetchSymbols(ctx context.Context) ([]string, error) {
	return p.Symbols(), nil
}
