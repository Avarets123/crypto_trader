package news

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/osman/bot-traider/internal/news/rss"
	"github.com/osman/bot-traider/internal/ollama"
)

// TelegramNotifier — интерфейс для отправки уведомлений в Telegram-топик.
// Принимается интерфейсом, чтобы не создавать циклической зависимости с пакетом telegram.
type TelegramNotifier interface {
	SendToThread(ctx context.Context, text string, threadID int)
}

// Summarizer — интерфейс для перевода и суммаризации текста через LLM.
type Summarizer interface {
	Summarize(ctx context.Context, title, description string) (string, error)
}

type fetchFunc func(ctx context.Context) ([]rss.Item, error)

// Service периодически опрашивает RSS-ленты и сохраняет новые статьи.
type Service struct {
	repo             *Repository
	log              *zap.Logger
	fetchIntervalMin int
	notifier         TelegramNotifier
	newsThreadID     int
	summarizer       Summarizer
}


func New(ctx context.Context, pool *pgxpool.Pool, tgNotifier TelegramNotifier, newsEnabled bool, newsThreadID int, intervalMin int, log *zap.Logger) {
		if newsEnabled {
		newsRepo := NewRepository(pool, log.With(zap.String("component", "news")))
		newsSvc := NewService(newsRepo, log.With(zap.String("component", "news")), intervalMin)
		newsSvc.WithTelegramNotifier(tgNotifier, newsThreadID)
		ollamaClient := ollama.NewClient(ollama.LoadConfig())
		newsSvc.WithSummarizer(ollamaClient)
		log.Info("news: Ollama summarizer enabled",
			zap.String("url", ollama.LoadConfig().URL),
			zap.String("model", ollama.LoadConfig().Model),
		)
		go newsSvc.Start(ctx)
		log.Info("news: RSS parser enabled", zap.Int("news_thread_id", newsThreadID))
	} else {
		log.Info("news: RSS parser disabled (NEWS_ENABLED=false)")
	}
}

// NewService создаёт Service.
func NewService(repo *Repository, log *zap.Logger, fetchIntervalMin int) *Service {
	return &Service{
		repo:             repo,
		log:              log,
		fetchIntervalMin: fetchIntervalMin,
	}
}

// WithTelegramNotifier подключает Telegram-нотификатор для отправки новостей в топик.
func (s *Service) WithTelegramNotifier(n TelegramNotifier, threadID int) {
	s.notifier = n
	s.newsThreadID = threadID
}

// WithSummarizer подключает LLM-суммаризатор (Ollama) для перевода новостей на русский.
func (s *Service) WithSummarizer(sm Summarizer) {
	s.summarizer = sm
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
		{"theblock", rss.FetchTheBlock},
		{"binance", rss.FetchBinanceAnnouncements},
		{"kucoin", rss.FetchKuCoinAnnouncements},
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

	newArticles, err := s.repo.SaveBatch(ctx, allArticles)
	if err != nil {
		s.log.Warn("news: SaveBatch error", zap.Error(err))
		return
	}
	s.log.Info("news: saved new articles",
		zap.Int("saved", len(newArticles)),
		zap.Int("total_fetched", len(allArticles)),
	)

	// Суммаризируем только новые статьи, затем отправляем в Telegram
	for i := range newArticles {
		a := &newArticles[i]
		if s.summarizer != nil {
			summary, err := s.summarizer.Summarize(ctx, a.Title, a.Description)
			if err != nil {
				s.log.Warn("news: ollama summarize failed",
					zap.String("title", a.Title),
					zap.Error(err),
				)
			} else {
				a.Summary = summary
				if err := s.repo.UpdateSummary(ctx, a.GUID, summary); err != nil {
					s.log.Warn("news: failed to update summary",
						zap.String("guid", a.GUID),
						zap.Error(err),
					)
				}
			}
		}
		if s.notifier != nil {
			go s.notifier.SendToThread(ctx, formatNewsMsg(*a), s.newsThreadID)
		}
	}
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

// formatNewsMsg форматирует сообщение о новой статье для Telegram.
// Если есть Summary (русское резюме от Ollama) — используется он, иначе fallback на оригинальный Title.
func formatNewsMsg(a Article) string {
	pubDate := ""
	if a.PublishedAt != nil {
		pubDate = fmt.Sprintf("\n<i>%s</i>", a.PublishedAt.Format("02 Jan 2006 15:04"))
	}
	if a.Summary != "" {
		return fmt.Sprintf("📰 <b>%s</b> — <a href=%q>читать</a>%s\n\n%s",
			a.Source, a.Link, pubDate, a.Summary,
		)
	}
	return fmt.Sprintf("📰 <b>%s</b> — <a href=%q>%s</a>%s",
		a.Source, a.Link, a.Title, pubDate,
	)
}
