package tinkoff

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	investgo "github.com/russianinvestments/invest-api-go-sdk/investgo"
	pb "github.com/russianinvestments/invest-api-go-sdk/proto"
	"go.uber.org/zap"
)

const batchSize = 500

// TopVolatileProvider — источник топ волатильных акций Т-Инвестиции.
// При старте получает полный список акций (классфильтр TQBR), рассчитывает
// % изменение цены за сессию и возвращает топ N наиболее волатильных.
// Пиннутые тикеры (ExtraSymbols) всегда включены в список.
type TopVolatileProvider struct {
	cfg   *Config
	limit int
	log   *zap.Logger

	mu             sync.RWMutex
	tickers        []VolatileTicker
	pinnedSymbols  []string         // тикеры из ExtraSymbols (до резолвинга)
	resolvedPinned []VolatileTicker // пиннутые с разрешёнными UID

	sdkClient *investgo.Client // создаётся при Fetch, реиспользуется в Run
	onChanged func(added, removed []string)
}

// NewTopVolatileProvider создаёт провайдер топ-волатильных акций.
func NewTopVolatileProvider(cfg *Config, limit int, log *zap.Logger) *TopVolatileProvider {
	return &TopVolatileProvider{
		cfg:   cfg,
		limit: limit,
		log:   log,
	}
}

// WithPinnedSymbols задаёт тикеры, которые всегда включаются в список и не удаляются.
// Вызывать до Fetch().
func (p *TopVolatileProvider) WithPinnedSymbols(symbols []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pinnedSymbols = make([]string, len(symbols))
	copy(p.pinnedSymbols, symbols)
	p.log.Info("tinkoff top volatile: pinned symbols set", zap.Strings("symbols", p.pinnedSymbols))
}

// WithOnSymbolsChanged регистрирует хук, вызываемый при изменении состава топа.
// added — новые тикеры, removed — выбывшие (пиннутые никогда не попадают в removed).
func (p *TopVolatileProvider) WithOnSymbolsChanged(fn func(added, removed []string)) {
	p.onChanged = fn
}

// Fetch делает первичную загрузку топа и резолвит пиннутые тикеры.
// Вызывается синхронно до запуска Run. ctx должен быть долгоживущим (main ctx).
func (p *TopVolatileProvider) Fetch(ctx context.Context) error {
	sdkClient, err := p.createSDKClient(ctx)
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.sdkClient = sdkClient
	p.mu.Unlock()

	return p.refresh(ctx)
}

// Run запускает периодическое обновление топа. Блокирует до ctx.Done().
// Fetch должен быть вызван до Run. Если после Fetch список пустой (ошибка при старте),
// Run пытается загрузить данные с backoff-ом (5s, 10s, 20s … до 60s) до успеха.
func (p *TopVolatileProvider) Run(ctx context.Context, interval time.Duration) {
	// Ретрай начальной загрузки если tickers пустой
	retryWait := 5 * time.Second
	for {
		p.mu.RLock()
		empty := len(p.tickers) == 0
		p.mu.RUnlock()
		if !empty {
			break
		}
		p.log.Info("tinkoff top volatile: retrying initial fetch", zap.Duration("wait", retryWait))
		select {
		case <-ctx.Done():
			p.stop()
			return
		case <-time.After(retryWait):
		}
		if err := p.refresh(ctx); err != nil {
			p.log.Warn("tinkoff top volatile: retry fetch failed", zap.Error(err))
		}
		if retryWait < 60*time.Second {
			retryWait *= 2
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.stop()
			return
		case <-ticker.C:
			if err := p.refresh(ctx); err != nil {
				p.log.Warn("tinkoff top volatile: refresh failed", zap.Error(err))
			}
		}
	}
}

// Symbols возвращает текущий список тикеров (копия).
func (p *TopVolatileProvider) Symbols() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]string, len(p.tickers))
	for i, t := range p.tickers {
		result[i] = t.Symbol
	}
	return result
}

// Tickers возвращает текущий полный список VolatileTicker (копия).
func (p *TopVolatileProvider) Tickers() []VolatileTicker {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]VolatileTicker, len(p.tickers))
	copy(result, p.tickers)
	return result
}

// refresh обновляет список топ-волатильных акций.
func (p *TopVolatileProvider) refresh(ctx context.Context) error {
	p.mu.RLock()
	sdkClient := p.sdkClient
	p.mu.RUnlock()

	if sdkClient == nil {
		return fmt.Errorf("sdk client not initialized, call Fetch first")
	}

	instClient := sdkClient.NewInstrumentsServiceClient()
	mdClient := sdkClient.NewMarketDataServiceClient()

	// Резолвим пиннутые тикеры при первом вызове (или если ещё не резолвились)
	p.mu.RLock()
	needResolve := len(p.pinnedSymbols) > 0 && len(p.resolvedPinned) == 0
	p.mu.RUnlock()
	if needResolve {
		if err := p.resolvePinned(ctx, instClient); err != nil {
			p.log.Warn("tinkoff top volatile: failed to resolve pinned symbols", zap.Error(err))
		}
	}

	top, err := p.fetchTopVolatile(ctx, instClient, mdClient)
	if err != nil {
		return fmt.Errorf("fetch top volatile: %w", err)
	}

	p.mu.Lock()
	old := p.tickers
	p.tickers = p.mergeWithPinnedLocked(top)
	newList := p.tickers
	p.mu.Unlock()

	added, removed := diffVolatileTickers(old, newList)
	// Пиннутые тикеры никогда не удаляются
	removed = p.filterPinned(removed)

	p.log.Info("tinkoff top volatile: refreshed",
		zap.Int("count", len(newList)),
		zap.Strings("symbols", symbolsOf(newList)),
	)

	if len(added) > 0 || len(removed) > 0 {
		p.log.Info("tinkoff top volatile: list changed",
			zap.Strings("added", added),
			zap.Strings("removed", removed),
		)
		if p.onChanged != nil {
			p.onChanged(added, removed)
		}
	}

	return nil
}

// fetchTopVolatile получает топ N наиболее волатильных акций из API.
func (p *TopVolatileProvider) fetchTopVolatile(
	ctx context.Context,
	instClient *investgo.InstrumentsServiceClient,
	mdClient *investgo.MarketDataServiceClient,
) ([]VolatileTicker, error) {
	// Получаем все акции со статусом "базовый" (торгуемые)
	sharesResp, err := instClient.Shares(pb.InstrumentStatus_INSTRUMENT_STATUS_BASE)
	if err != nil {
		return nil, fmt.Errorf("get shares: %w", err)
	}

	type shareInfo struct {
		uid      string
		ticker   string
		currency string
	}

	// Фильтруем по классу биржи и доступности API
	eligible := make([]shareInfo, 0, 300)
	for _, s := range sharesResp.GetInstruments() {
		if s.GetClassCode() != p.cfg.ExchangeFilter {
			continue
		}
		if !s.GetApiTradeAvailableFlag() || !s.GetBuyAvailableFlag() {
			continue
		}
		if strings.ToLower(s.GetCurrency()) != "rub" {
			continue
		}
		eligible = append(eligible, shareInfo{
			uid:      s.GetUid(),
			ticker:   s.GetTicker(),
			currency: s.GetCurrency(),
		})
	}

	if len(eligible) == 0 {
		return nil, fmt.Errorf("no eligible shares found for exchange filter %q", p.cfg.ExchangeFilter)
	}

	p.log.Debug("tinkoff top volatile: eligible shares",
		zap.Int("count", len(eligible)),
		zap.String("exchange", p.cfg.ExchangeFilter),
	)

	// Собираем UIDs для батч-запросов
	uids := make([]string, len(eligible))
	for i, s := range eligible {
		uids[i] = s.uid
	}

	// Получаем последние цены и цены закрытия прошлой сессии
	lastPrices, err := batchGetLastPrices(mdClient, uids)
	if err != nil {
		return nil, fmt.Errorf("get last prices: %w", err)
	}
	closePrices, err := batchGetClosePrices(mdClient, uids)
	if err != nil {
		return nil, fmt.Errorf("get close prices: %w", err)
	}

	// Рассчитываем % изменение и собираем результат
	result := make([]VolatileTicker, 0, len(eligible))
	for _, s := range eligible {
		last := lastPrices[s.uid]
		close := closePrices[s.uid]
		if last == 0 || close == 0 {
			continue
		}
		changePct := (last - close) / close * 100
		result = append(result, VolatileTicker{
			Symbol:         s.ticker,
			UID:            s.uid,
			Currency:       s.currency,
			PriceChangePct: changePct,
			LastPrice:      last,
		})
	}

	// Сортируем по абсолютному значению % изменения (наиболее волатильные первые)
	sort.Slice(result, func(i, j int) bool {
		return math.Abs(result[i].PriceChangePct) > math.Abs(result[j].PriceChangePct)
	})

	// Берём топ N
	if len(result) > p.limit {
		result = result[:p.limit]
	}

	return result, nil
}

// resolvePinned резолвит пиннутые тикеры в UID через FindInstrument.
func (p *TopVolatileProvider) resolvePinned(_ context.Context, instClient *investgo.InstrumentsServiceClient) error {
	p.mu.RLock()
	syms := make([]string, len(p.pinnedSymbols))
	copy(syms, p.pinnedSymbols)
	p.mu.RUnlock()

	resolved := make([]VolatileTicker, 0, len(syms))
	for _, sym := range syms {
		resp, err := instClient.FindInstrument(sym)
		if err != nil {
			p.log.Warn("tinkoff top volatile: find pinned instrument failed",
				zap.String("symbol", sym), zap.Error(err))
			continue
		}

		var best *pb.InstrumentShort
		for _, inst := range resp.GetInstruments() {
			if !strings.EqualFold(inst.GetTicker(), sym) {
				continue
			}
			if inst.GetInstrumentType() != "share" {
				continue
			}
			if best == nil {
				best = inst
			} else if inst.GetClassCode() == "TQBR" {
				best = inst
			}
		}
		// Fallback: любой с точным совпадением тикера
		if best == nil {
			for _, inst := range resp.GetInstruments() {
				if strings.EqualFold(inst.GetTicker(), sym) {
					best = inst
					break
				}
			}
		}
		if best == nil {
			p.log.Warn("tinkoff top volatile: pinned symbol not found", zap.String("symbol", sym))
			continue
		}

		resolved = append(resolved, VolatileTicker{
			Symbol:   best.GetTicker(),
			UID:      best.GetUid(),
			Currency: currencyByClassCode(best.GetClassCode()),
		})
		p.log.Info("tinkoff top volatile: pinned symbol resolved",
			zap.String("ticker", best.GetTicker()),
			zap.String("uid", best.GetUid()),
			zap.String("class_code", best.GetClassCode()),
		)
	}

	p.mu.Lock()
	p.resolvedPinned = resolved
	p.mu.Unlock()

	return nil
}

// mergeWithPinnedLocked добавляет пиннутые тикеры без дублей.
// Мьютекс должен быть уже захвачен.
func (p *TopVolatileProvider) mergeWithPinnedLocked(tickers []VolatileTicker) []VolatileTicker {
	if len(p.resolvedPinned) == 0 {
		return tickers
	}
	existing := make(map[string]struct{}, len(tickers))
	for _, t := range tickers {
		existing[t.Symbol] = struct{}{}
	}
	result := make([]VolatileTicker, len(tickers))
	copy(result, tickers)
	for _, pt := range p.resolvedPinned {
		if _, ok := existing[pt.Symbol]; !ok {
			result = append(result, pt)
		}
	}
	return result
}

// filterPinned убирает пиннутые тикеры из списка removed.
func (p *TopVolatileProvider) filterPinned(removed []string) []string {
	p.mu.RLock()
	pinned := p.resolvedPinned
	p.mu.RUnlock()

	if len(pinned) == 0 {
		return removed
	}
	pinnedSet := make(map[string]struct{}, len(pinned))
	for _, t := range pinned {
		pinnedSet[t.Symbol] = struct{}{}
	}
	filtered := removed[:0]
	for _, s := range removed {
		if _, ok := pinnedSet[s]; !ok {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// createSDKClient создаёт SDK-клиент для Т-Инвестиции.
// Всегда использует production-endpoint: InstrumentsService и MarketDataService
// возвращают одинаковые данные для sandbox и prod; sandbox-endpoint не поддерживает
// большие ответы GetShares (unexpected EOF).
func (p *TopVolatileProvider) createSDKClient(ctx context.Context) (*investgo.Client, error) {
	sdkCfg := investgo.Config{
		Token:    p.cfg.Token,
		EndPoint: "invest-public-api.tinkoff.ru:443",
		AppName:  "bot-traider",
	}
	return investgo.NewClient(ctx, sdkCfg, &zapLogger{p.log})
}

// stop закрывает SDK-клиент.
func (p *TopVolatileProvider) stop() {
	p.mu.RLock()
	sdkClient := p.sdkClient
	p.mu.RUnlock()
	if sdkClient != nil {
		sdkClient.Stop() //nolint:errcheck
	}
}

// batchGetLastPrices получает последние цены для списка UID батчами.
func batchGetLastPrices(mdClient *investgo.MarketDataServiceClient, uids []string) (map[string]float64, error) {
	result := make(map[string]float64, len(uids))
	for i := 0; i < len(uids); i += batchSize {
		end := i + batchSize
		if end > len(uids) {
			end = len(uids)
		}
		resp, err := mdClient.GetLastPrices(uids[i:end])
		if err != nil {
			return nil, err
		}
		for _, lp := range resp.GetLastPrices() {
			uid := lp.GetInstrumentUid()
			if uid == "" {
				uid = lp.GetFigi()
			}
			result[uid] = quotationToFloat(lp.GetPrice())
		}
	}
	return result, nil
}

// batchGetClosePrices получает цены закрытия прошлой сессии для списка UID батчами.
func batchGetClosePrices(mdClient *investgo.MarketDataServiceClient, uids []string) (map[string]float64, error) {
	result := make(map[string]float64, len(uids))
	for i := 0; i < len(uids); i += batchSize {
		end := i + batchSize
		if end > len(uids) {
			end = len(uids)
		}
		resp, err := mdClient.GetClosePrices(uids[i:end])
		if err != nil {
			return nil, err
		}
		for _, cp := range resp.GetClosePrices() {
			uid := cp.GetInstrumentUid()
			result[uid] = quotationToFloat(cp.GetPrice())
		}
	}
	return result, nil
}

// diffVolatileTickers вычисляет разницу между старым и новым списком.
func diffVolatileTickers(old, newList []VolatileTicker) (added, removed []string) {
	oldSet := make(map[string]struct{}, len(old))
	for _, t := range old {
		oldSet[t.Symbol] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(newList))
	for _, t := range newList {
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

// symbolsOf возвращает срез тикеров из списка VolatileTicker.
func symbolsOf(tickers []VolatileTicker) []string {
	result := make([]string, len(tickers))
	for i, t := range tickers {
		result[i] = t.Symbol
	}
	return result
}
