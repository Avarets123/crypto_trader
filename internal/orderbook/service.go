package orderbook

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Service инициализирует стаканы через REST и запускает WS-подписки.
type Service struct {
	fetcher *Fetcher
	repo    *Repository
	log     *zap.Logger
}

// NewService создаёт Service.
func NewService(fetcher *Fetcher, repo *Repository, log *zap.Logger) *Service {
	return &Service{fetcher: fetcher, repo: repo, log: log}
}

// staleThreshold — если стакан в Redis старше этого времени, считаем его устаревшим и перезапрашиваем.
const staleThreshold = 5 * time.Minute

// Init для каждого символа:
// 1. Проверяет Redis — если стакан свежий, использует его без обращения к Binance REST.
// 2. Если данных нет или они устарели — загружает REST-снимок и сохраняет в Redis.
// 3. В любом случае запускает WS-подписку, которая непрерывно обновляет Redis.
func (s *Service) Init(ctx context.Context, symbols []string, depth int) {
	for _, sym := range symbols {
		existing, err := s.repo.Get(ctx, sym)
		if err == nil && time.Since(existing.UpdatedAt) < staleThreshold {
			s.log.Info("orderbook: using cached data from redis",
				zap.String("symbol", sym),
				zap.Time("updated_at", existing.UpdatedAt),
			)
		} else {
			// Данных нет или устарели — запрашиваем REST
			ob, err := s.fetcher.FetchSnapshot(ctx, sym, depth)
			if err != nil {
				s.log.Error("orderbook: fetch snapshot failed",
					zap.String("symbol", sym),
					zap.Error(err),
				)
			} else if err := s.repo.Save(ctx, *ob); err != nil {
				s.log.Error("orderbook: save snapshot failed",
					zap.String("symbol", sym),
					zap.Error(err),
				)
			} else {
				s.log.Info("orderbook: snapshot fetched and saved",
					zap.String("symbol", sym),
					zap.Int("bids", len(ob.Bids)),
					zap.Int("asks", len(ob.Asks)),
				)
			}
		}

		// WS-подписка всегда запускается — держит Redis актуальным
		go s.fetcher.Subscribe(ctx, sym)
	}
}
