package news

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/news/rss"
)

type fetchFunc func(ctx context.Context) ([]rss.Item, error)

// Service периодически опрашивает RSS-ленты и сохраняет новые статьи.
type Service struct {
	repo             *Repository
	log              *zap.Logger
	fetchIntervalMin int
}

// NewService создаёт Service.
func NewService(repo *Repository, log *zap.Logger, fetchIntervalMin int) *Service {
	return &Service{
		repo:             repo,
		log:              log,
		fetchIntervalMin: fetchIntervalMin,
	}
}

// FetchAndSave опрашивает все RSS-ленты параллельно и сохраняет новые статьи.
func (s *Service) FetchAndSave(ctx context.Context) {
	sources := []struct {
		name string
		fn   fetchFunc
	}{
		{"coindesk", rss.FetchCoinDesk},
		{"cointelegraph", rss.FetchCoinTelegraph},
		{"decrypt", rss.FetchDecrypt},
	}

	var (
		mu          sync.Mutex
		allArticles []Article
	)

	var wg sync.WaitGroup
	for _, src := range sources {
		src := src
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := src.fn(ctx)
			if err != nil {
				s.log.Warn("news: failed to fetch RSS",
					zap.String("source", src.name),
					zap.Error(err),
				)
				return
			}
			s.log.Info("news: fetched articles",
				zap.String("source", src.name),
				zap.Int("count", len(items)),
			)
			articles := make([]Article, 0, len(items))
			for _, it := range items {
				articles = append(articles, Article{
					Source:      it.Source,
					GUID:        it.GUID,
					Title:       it.Title,
					Link:        it.Link,
					Description: it.Summary,
					Summary:     it.Summary,
					PublishedAt: it.PublishedAt,
				})
			}
			mu.Lock()
			allArticles = append(allArticles, articles...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(allArticles) == 0 {
		return
	}

	saved, err := s.repo.SaveBatch(ctx, allArticles)
	if err != nil {
		s.log.Warn("news: SaveBatch error", zap.Error(err))
		return
	}
	s.log.Info("news: saved new articles",
		zap.Int64("saved", saved),
		zap.Int("total_fetched", len(allArticles)),
	)
}

// Start запускает scheduler: первый вызов сразу, затем каждые fetchIntervalMin минут.
func (s *Service) Start(ctx context.Context) {
	interval := time.Duration(s.fetchIntervalMin) * time.Minute
	s.log.Info("news: scheduler started",
		zap.Duration("interval", interval),
	)

	s.FetchAndSave(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.log.Info("news: scheduler stopped")
			return
		case <-ticker.C:
			s.FetchAndSave(ctx)
		}
	}
}
