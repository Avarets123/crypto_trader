package exchange_orders

import (
	"context"
	"sync"

	"go.uber.org/zap"
)

// Service запускает WS-подписки на торговые сделки для списка символов.
// Поддерживает динамическое добавление/удаление символов через OnSymbolsChanged.
type Service struct {
	fetcher *Fetcher
	log     *zap.Logger
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	rootCtx context.Context
}

// NewService создаёт Service.
func NewService(fetcher *Fetcher, log *zap.Logger) *Service {
	return &Service{
		fetcher: fetcher,
		log:     log,
		cancels: make(map[string]context.CancelFunc),
	}
}

// Start запускает горутину-подписку для каждого символа.
func (s *Service) Start(ctx context.Context, symbols []string) {
	s.rootCtx = ctx
	for _, sym := range symbols {
		s.startSymbol(sym)
	}
	s.log.Info("exchange_orders: started trade subscriptions", zap.Int("symbols", len(symbols)), zap.Strings("list", symbols))
}

// OnSymbolsChanged реагирует на изменение топ-листа:
// запускает подписку для новых символов, останавливает для выбывших.
func (s *Service) OnSymbolsChanged(added, removed []string) {
	for _, sym := range removed {
		s.stopSymbol(sym)
	}
	for _, sym := range added {
		s.startSymbol(sym)
	}
}

// startSymbol запускает WS-подписку для символа с отдельным cancel-контекстом.
// Если символ уже подписан — ничего не делает.
func (s *Service) startSymbol(symbol string) {
	s.mu.Lock()
	if _, exists := s.cancels[symbol]; exists {
		s.mu.Unlock()
		s.log.Debug("exchange_orders: already subscribed", zap.String("symbol", symbol))
		return
	}
	ctx, cancel := context.WithCancel(s.rootCtx)
	s.cancels[symbol] = cancel
	s.mu.Unlock()

	s.log.Info("exchange_orders: subscribing", zap.String("symbol", symbol))
	go s.fetcher.Subscribe(ctx, symbol)
}

// stopSymbol отменяет WS-горутину для символа.
func (s *Service) stopSymbol(symbol string) {
	s.mu.Lock()
	cancel, ok := s.cancels[symbol]
	if ok {
		delete(s.cancels, symbol)
	}
	s.mu.Unlock()

	if ok {
		cancel()
		s.log.Info("exchange_orders: unsubscribed", zap.String("symbol", symbol))
	}
}
