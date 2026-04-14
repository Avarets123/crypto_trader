package orderbook

import (
	"context"
	"sync"
	"time"

	"github.com/osman/bot-traider/internal/shared/utils"
	"go.uber.org/zap"
)

// Service управляет локальными стаканами: инициализирует и держит diff-стримы.
// Поддерживает динамическое добавление/удаление символов через OnSymbolsChanged.
type Service struct {
	fetcher *Fetcher
	store   *Store
	log     *zap.Logger
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	rootCtx context.Context
}

// NewService создаёт Service.
func NewService(fetcher *Fetcher, store *Store, log *zap.Logger) *Service {
	return &Service{
		fetcher: fetcher,
		store:   store,
		log:     log,
		cancels: make(map[string]context.CancelFunc),
	}
}

// snapshotDepth — глубина стакана для REST-снимка.
// 500 уровней (вес 25) вместо 5000 (вес 250) чтобы не превышать rate limit Binance.
const snapshotDepth = 1000

// initDelay — задержка между запусками горутин чтобы не бомбить REST одновременно.
const initDelay = 300 * time.Millisecond

// Init для каждого символа запускает полный diff-стрим:
// загружает REST-снимок → инициализирует LocalBook → держит WS актуальным.
// Горутины стартуют с задержкой initDelay чтобы не превысить rate limit Binance.
func (s *Service) Init(ctx context.Context, symbols []string) {
	defer utils.TimeTracker(s.log, "Init; orderbook service")()

	s.rootCtx = ctx
	for i, sym := range symbols {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(initDelay):
			}
		}
		s.startSymbol(sym)
	}
}

// OnSymbolsChanged реагирует на изменение топ-листа:
// запускает подписку для новых символов, останавливает для выбывших.
func (s *Service) OnSymbolsChanged(added, removed []string) {
	for _, sym := range removed {
		s.stopSymbol(sym)
	}
	for i, sym := range added {
		if i > 0 {
			select {
			case <-s.rootCtx.Done():
				return
			case <-time.After(initDelay):
			}
		}
		s.startSymbol(sym)
	}
}

// startSymbol запускает diff-стрим для символа с отдельным cancel-контекстом.
// Если символ уже подписан — ничего не делает.
func (s *Service) startSymbol(symbol string) {
	s.mu.Lock()
	if _, exists := s.cancels[symbol]; exists {
		s.mu.Unlock()
		s.log.Debug("orderbook: already subscribed", zap.String("symbol", symbol))
		return
	}
	ctx, cancel := context.WithCancel(s.rootCtx)
	s.cancels[symbol] = cancel
	s.mu.Unlock()

	s.log.Info("orderbook: subscribing", zap.String("symbol", symbol))
	go s.fetcher.SubscribeDiff(ctx, symbol, s.store)
}

// stopSymbol отменяет WS-горутину и удаляет LocalBook из Store.
func (s *Service) stopSymbol(symbol string) {
	s.mu.Lock()
	cancel, ok := s.cancels[symbol]
	if ok {
		delete(s.cancels, symbol)
	}
	s.mu.Unlock()

	if ok {
		cancel()
		s.store.Remove(symbol)
		s.log.Info("orderbook: unsubscribed and removed", zap.String("symbol", symbol))
	}
}

// GetBook возвращает актуальный снимок стакана из памяти.
// Возвращает false если символ ещё не инициализирован.
func (s *Service) GetBook(symbol string) (*OrderBook, bool) {
	return s.store.Snapshot(symbol, s.log)
}
