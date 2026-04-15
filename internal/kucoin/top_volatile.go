package kucoin

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// TopVolatileProvider — источник топ волатильных символов KuCoin.
// Аналог binance.TopVolatileProvider, но использует KuCoin REST API.
// Поддерживает исключение символов, уже отслеживаемых другой биржей (WithExcludeFunc),
// чтобы списки Binance и KuCoin не пересекались.
type TopVolatileProvider struct {
	mu            sync.RWMutex
	tickers       []VolatileTicker
	pinnedSymbols []string
	excludeFunc   func() []string // динамический список символов для исключения (например, топ Binance)
	rest          *RestClient
	limit         int
	log           *zap.Logger
	onChanged     func(added, removed []string)
}

// NewTopVolatileProvider создаёт провайдер топ-волатильных символов KuCoin.
func NewTopVolatileProvider(rest *RestClient, limit int, log *zap.Logger) *TopVolatileProvider {
	return &TopVolatileProvider{
		rest:  rest,
		limit: limit,
		log:   log,
	}
}

// WithPinnedSymbols задаёт символы, которые всегда включаются в список и никогда не удаляются.
// Вызывать до Fetch().
func (p *TopVolatileProvider) WithPinnedSymbols(symbols []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pinnedSymbols = make([]string, len(symbols))
	copy(p.pinnedSymbols, symbols)
	p.log.Info("kucoin top volatile: pinned symbols set", zap.Strings("symbols", p.pinnedSymbols))
}

// WithExcludeFunc задаёт функцию, которая возвращает символы для исключения из списка.
// Используется для исключения символов, уже отслеживаемых Binance.
// Вызывать до Fetch().
func (p *TopVolatileProvider) WithExcludeFunc(fn func() []string) {
	p.excludeFunc = fn
}

// Fetch делает первичную загрузку. Вызывается синхронно до старта остальных компонентов.
func (p *TopVolatileProvider) Fetch(ctx context.Context) error {
	tickers, err := p.rest.GetTopVolatile(ctx, p.limit)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.tickers = p.buildListLocked(tickers)
	p.mu.Unlock()
	p.log.Info("kucoin top volatile: loaded",
		zap.Int("count", len(p.tickers)),
		zap.Strings("symbols", p.Symbols()),
	)
	return nil
}

// WithOnSymbolsChanged регистрирует хук, вызываемый при изменении состава топа.
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
				p.log.Warn("kucoin top volatile: refresh failed", zap.Error(err))
				continue
			}

			p.mu.Lock()
			old := p.tickers
			p.tickers = p.buildListLocked(tickers)
			newList := p.tickers
			p.mu.Unlock()

			added, removed := diffKucoinSymbols(old, newList)
			removed = p.filterPinned(removed)

			p.log.Info("kucoin top volatile: refreshed", zap.Int("count", len(p.tickers)))

			if len(added) > 0 || len(removed) > 0 {
				p.log.Info("kucoin top volatile: list changed",
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

// buildListLocked применяет пиннинг и исключения к сырому списку тикеров.
// Мьютекс должен быть захвачен вызывающей стороной.
func (p *TopVolatileProvider) buildListLocked(tickers []VolatileTicker) []VolatileTicker {
	// Строим множество исключённых символов
	excluded := make(map[string]struct{})
	if p.excludeFunc != nil {
		for _, s := range p.excludeFunc() {
			excluded[s] = struct{}{}
		}
	}

	// Фильтруем тикеры: убираем исключённые (кроме пиннутых)
	pinnedSet := make(map[string]struct{}, len(p.pinnedSymbols))
	for _, s := range p.pinnedSymbols {
		pinnedSet[s] = struct{}{}
	}

	filtered := make([]VolatileTicker, 0, len(tickers))
	for _, t := range tickers {
		_, isPinned := pinnedSet[t.Symbol]
		_, isExcluded := excluded[t.Symbol]
		if !isExcluded || isPinned {
			filtered = append(filtered, t)
		}
	}

	// Добавляем пиннутые символы, которых нет в списке
	existing := make(map[string]struct{}, len(filtered))
	for _, t := range filtered {
		existing[t.Symbol] = struct{}{}
	}
	result := make([]VolatileTicker, len(filtered))
	copy(result, filtered)
	for _, sym := range p.pinnedSymbols {
		if _, ok := existing[sym]; !ok {
			result = append(result, VolatileTicker{Symbol: sym})
		}
	}

	if len(excluded) > 0 {
		p.log.Debug("kucoin top volatile: excluded binance symbols",
			zap.Int("excluded_count", len(excluded)),
			zap.Int("result_count", len(result)),
		)
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

// diffKucoinSymbols вычисляет разницу между старым и новым списком тикеров.
func diffKucoinSymbols(old, new []VolatileTicker) (added, removed []string) {
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
