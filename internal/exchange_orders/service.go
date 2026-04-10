package exchange_orders

import (
	"context"

	"go.uber.org/zap"
)

// Service запускает WS-подписки на торговые сделки для списка символов.
type Service struct {
	fetcher *Fetcher
	log     *zap.Logger
}

// NewService создаёт Service.
func NewService(fetcher *Fetcher, log *zap.Logger) *Service {
	return &Service{fetcher: fetcher, log: log}
}

// Start запускает горутину-подписку для каждого символа.
func (s *Service) Start(ctx context.Context, symbols []string) {
	for _, sym := range symbols {
		go s.fetcher.Subscribe(ctx, sym)
	}
	s.log.Info("exchange_orders: started trade subscriptions", zap.Int("symbols", len(symbols)), zap.Strings("list", symbols))
}
