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
// Пиннутые символы (WithPinnedSymbols) всегда включены в список и никогда не удаляются.
type TopVolatileProvider struct {
	mu            sync.RWMutex
	tickers       []VolatileTicker
	pinnedSymbols []string // символы, всегда присутствующие в списке (из EXTRA_SYMBOLS)
	rest          *RestClient
	limit         int
	log           *zap.Logger
	onChanged     func(added, removed []string)
}

// NewTopVolatileProvider создаёт провайдер.
func NewTopVolatileProvider(rest *RestClient, limit int, log *zap.Logger) *TopVolatileProvider {
	return &TopVolatileProvider{
		rest:  rest,
		limit: limit,
		log:   log,
	}
}

// WithPinnedSymbols задаёт список символов, которые всегда включаются в Symbols() / Tickers()
// и никогда не попадают в removed при OnSymbolsChanged. Вызывать до Fetch().
func (p *TopVolatileProvider) WithPinnedSymbols(symbols []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pinnedSymbols = make([]string, len(symbols))
	copy(p.pinnedSymbols, symbols)
	p.log.Info("top volatile: pinned symbols set", zap.Strings("symbols", p.pinnedSymbols))
}

// Fetch делает первичную загрузку. Вызывается синхронно до старта остальных компонентов.
func (p *TopVolatileProvider) Fetch(ctx context.Context) error {
	tickers, err := p.rest.GetTopVolatile(ctx, p.limit)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.tickers = p.mergeWithPinnedLocked(tickers)
	p.mu.Unlock()
	p.log.Info("top volatile: loaded", zap.Int("count", len(p.tickers)), zap.Strings("symbols", p.Symbols()))
	return nil
}

// WithOnSymbolsChanged регистрирует хук, вызываемый при изменении состава топа.
// added — новые символы, removed — выбывшие (пиннутые никогда не попадают в removed).
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
			old := p.tickers
			p.tickers = p.mergeWithPinnedLocked(tickers)
			new := p.tickers
			p.mu.Unlock()

			added, removed := diffSymbols(old, new)
			// Пиннутые символы никогда не удаляются — фильтруем из removed
			removed = p.filterPinned(removed)

			p.log.Info("top volatile: refreshed", zap.Int("count", len(p.tickers)))

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

// mergeWithPinnedLocked добавляет пиннутые символы в список тикеров без дублей.
// Мьютекс должен быть уже захвачен вызывающей стороной.
func (p *TopVolatileProvider) mergeWithPinnedLocked(tickers []VolatileTicker) []VolatileTicker {
	if len(p.pinnedSymbols) == 0 {
		return tickers
	}
	existing := make(map[string]struct{}, len(tickers))
	for _, t := range tickers {
		existing[t.Symbol] = struct{}{}
	}
	result := make([]VolatileTicker, len(tickers))
	copy(result, tickers)
	for _, sym := range p.pinnedSymbols {
		if _, ok := existing[sym]; !ok {
			result = append(result, VolatileTicker{Symbol: sym})
		}
	}
	return result
}

// filterPinned убирает пиннутые символы из списка removed.
func (p *TopVolatileProvider) filterPinned(removed []string) []string {
	if len(p.pinnedSymbols) == 0 {
		return removed
	}
	pinnedSet := make(map[string]struct{}, len(p.pinnedSymbols))
	for _, s := range p.pinnedSymbols {
		pinnedSet[s] = struct{}{}
	}
	filtered := removed[:0]
	for _, s := range removed {
		if _, ok := pinnedSet[s]; !ok {
			filtered = append(filtered, s)
		}
	}
	return filtered
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
