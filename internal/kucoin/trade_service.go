package kucoin

import (
	"context"
	"sync"

	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"go.uber.org/zap"
)

// TradeService управляет батчевыми WS-подписками на сделки KuCoin (/market/match).
// Все символы одного чанка (до MaxSymbolsPerConn) обслуживаются одним WS-соединением,
// что снижает количество одновременных соединений.
type TradeService struct {
	fetcher *TradeFetcher
	log     *zap.Logger

	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      *sync.WaitGroup
	rootCtx context.Context
	symbols []string
}

// NewTradeService создаёт TradeService.
func NewTradeService(rest *RestClient, log *zap.Logger) *TradeService {
	return &TradeService{
		fetcher: NewTradeFetcher(rest, log),
		log:     log,
		wg:      &sync.WaitGroup{},
	}
}

// WithOnTrade устанавливает realtime-хук, вызываемый при каждой входящей сделке.
// Должен быть вызван до Start().
func (s *TradeService) WithOnTrade(fn func(exchange_orders.ExchangeOrder)) {
	s.fetcher.WithOnTrade(fn)
}

// WithOnSave устанавливает хук батч-сохранения сделок в БД.
// Должен быть вызван до Start().
func (s *TradeService) WithOnSave(fn func(ctx context.Context, orders []exchange_orders.ExchangeOrder) error) {
	s.fetcher.WithOnSave(fn)
}

// Start запускает подписки для начального списка символов.
func (s *TradeService) Start(ctx context.Context, symbols []string) {
	s.rootCtx = ctx
	s.restart(symbols)
}

// OnSymbolsChanged реагирует на изменение топ-листа: пересчитывает список и перезапускает соединения.
func (s *TradeService) OnSymbolsChanged(added, removed []string) {
	s.mu.Lock()
	current := make(map[string]struct{}, len(s.symbols))
	for _, sym := range s.symbols {
		current[sym] = struct{}{}
	}
	for _, sym := range removed {
		delete(current, sym)
	}
	for _, sym := range added {
		current[sym] = struct{}{}
	}
	newSymbols := make([]string, 0, len(current))
	for sym := range current {
		newSymbols = append(newSymbols, sym)
	}
	s.mu.Unlock()

	s.restart(newSymbols)
}

// restart останавливает текущие соединения и запускает новые батчи.
func (s *TradeService) restart(symbols []string) {
	s.mu.Lock()
	oldCancel := s.cancel
	oldWg := s.wg
	s.mu.Unlock()

	// Останавливаем предыдущие соединения
	if oldCancel != nil {
		oldCancel()
		oldWg.Wait()
	}

	s.mu.Lock()
	s.symbols = symbols
	if len(symbols) == 0 {
		s.cancel = nil
		s.wg = &sync.WaitGroup{}
		s.mu.Unlock()
		s.log.Info("kucoin trade: no symbols, connections stopped")
		return
	}
	ctx, cancel := context.WithCancel(s.rootCtx)
	wg := &sync.WaitGroup{}
	s.cancel = cancel
	s.wg = wg
	s.mu.Unlock()

	chunks := ChunkSymbols(symbols, MaxSymbolsPerConn)
	s.log.Info("kucoin trade: starting connections",
		zap.Int("symbols", len(symbols)),
		zap.Int("connections", len(chunks)),
	)

	for i, chunk := range chunks {
		wg.Add(1)
		go func(id int, syms []string) {
			defer wg.Done()
			s.fetcher.Subscribe(ctx, id, syms)
		}(i, chunk)
	}
}
