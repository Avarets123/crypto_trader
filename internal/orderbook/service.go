package orderbook

import (
	"context"

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

// Init для каждого символа запускает полный diff-стрим:
// загружает REST-снимок (5000 уровней) → инициализирует LocalBook → держит WS актуальным.
func (s *Service) Init(ctx context.Context, symbols []string) {
	for _, sym := range symbols {
		go s.fetcher.SubscribeDiff(ctx, sym, s.store)
	}
}

// GetBook возвращает актуальный снимок стакана из памяти.
// Возвращает false если символ ещё не инициализирован.
func (s *Service) GetBook(symbol string) (*OrderBook, bool) {
	return s.store.Snapshot(symbol)
}
