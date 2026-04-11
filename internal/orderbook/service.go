package orderbook

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Service управляет локальными стаканами: инициализирует и держит diff-стримы.
type Service struct {
	fetcher *Fetcher
	store   *Store
	log     *zap.Logger
}

// NewService создаёт Service.
func NewService(fetcher *Fetcher, store *Store, log *zap.Logger) *Service {
	return &Service{fetcher: fetcher, store: store, log: log}
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
	for i, sym := range symbols {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(initDelay):
			}
		}
		go s.fetcher.SubscribeDiff(ctx, sym, s.store)
	}
}

// GetBook возвращает актуальный снимок стакана из памяти.
// Возвращает false если символ ещё не инициализирован.
func (s *Service) GetBook(symbol string) (*OrderBook, bool) {
	return s.store.Snapshot(symbol)
}
