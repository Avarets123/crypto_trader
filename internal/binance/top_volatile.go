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
// При изменении списка вызывает хук onChanged(added, removed).
type TopVolatileProvider struct {
	mu        sync.RWMutex
	tickers   []VolatileTicker
	rest      *RestClient
	limit     int
	log       *zap.Logger
	onChanged func(added, removed []string)
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

// WithOnSymbolsChanged регистрирует хук, вызываемый при изменении состава топа.
// added — новые символы, removed — выбывшие.
func (p *TopVolatileProvider) WithOnSymbolsChanged(fn func(added, removed []string)) {
	p.onChanged = fn
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
			added, removed := diffSymbols(p.tickers, tickers)
			p.tickers = tickers
			p.mu.Unlock()

			p.log.Info("top volatile: refreshed", zap.Int("count", len(tickers)))

			if len(added) > 0 || len(removed) > 0 {
				p.log.Info("top volatile: list changed",
					zap.Strings("added", added),
					zap.Strings("removed", removed),
				)
				if p.onChanged != nil {
					p.onChanged(added, removed)
				}
			}
		}
	}
}

// diffSymbols вычисляет разницу между старым и новым списком тикеров.
func diffSymbols(old, new []VolatileTicker) (added, removed []string) {
	oldSet := make(map[string]struct{}, len(old))
	for _, t := range old {
		oldSet[t.Symbol] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(new))
	for _, t := range new {
		newSet[t.Symbol] = struct{}{}
	}
	for sym := range newSet {
		if _, ok := oldSet[sym]; !ok {
			added = append(added, sym)
		}
	}
	for sym := range oldSet {
		if _, ok := newSet[sym]; !ok {
			removed = append(removed, sym)
		}
	}
	return
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
