package news

import (
	"context"
	"fmt"
	"strings"
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

// Summarizer — интерфейс для анализа новостей и листингов через LLM.
type Summarizer interface {
	Summarize(ctx context.Context, title, description string) (string, error)
	AnalyzeListing(ctx context.Context, title, description string) (string, error)
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
		{"bybit", rss.FetchBybitAnnouncements},
		{"okx", rss.FetchOKXAnnouncements},
		{"bitget", rss.FetchBitgetAnnouncements},
		{"coinbase", rss.FetchCoinbaseListings},
		{"mexc", rss.FetchMEXCListings},
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
					IsListing:   it.IsListing,
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

	// Строим маппинг guid→isListing до SaveBatch, чтобы не потерять флаг
	isListingByGUID := make(map[string]bool, len(allArticles))
	for i := range allArticles {
		if allArticles[i].IsListing {
			isListingByGUID[allArticles[i].GUID] = true
		}
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

	if s.summarizer == nil || s.notifier == nil {
		return
	}

	// Защита от первого запуска: источники без дат (coinbase, mexc) могут вернуть
	// тысячи "новых" записей сразу — сохраняем в БД, но в Telegram не шлём.
	const maxNewPerSource = 20
	newCountBySource := make(map[string]int)
	for i := range newArticles {
		newCountBySource[newArticles[i].Source]++
	}
	floodSources := make(map[string]bool)
	for src, cnt := range newCountBySource {
		if cnt > maxNewPerSource {
			floodSources[src] = true
			s.log.Info("news: initial sync detected, skipping announce",
				zap.String("source", src),
				zap.Int("new_count", cnt),
			)
		}
	}

	// source → блоки для листингов и сигналов новостей
	listingBlocks := make(map[string][]string)
	var listingOrder []string
	seenListing := make(map[string]bool)

	sourceSignals := make(map[string][]string)
	var sourceOrder []string
	seenSource := make(map[string]bool)

	for i := range newArticles {
		a := &newArticles[i]
		if floodSources[a.Source] {
			continue
		}
		a.IsListing = isListingByGUID[a.GUID]

		if a.IsListing {
			analysis, err := s.summarizer.AnalyzeListing(ctx, a.Title, a.Description)
			if err != nil {
				s.log.Warn("news: ollama listing analyze failed",
					zap.String("title", a.Title),
					zap.Error(err),
				)
				analysis = ""
			} else {
				analysis = strings.TrimSpace(analysis)
				if err := s.repo.UpdateSummary(ctx, a.GUID, analysis); err != nil {
					s.log.Warn("news: failed to save listing analysis", zap.String("guid", a.GUID), zap.Error(err))
				}
			}
			s.log.Info("news: new listing", zap.String("source", a.Source), zap.String("title", a.Title))
			block := formatListingBlock(a, analysis)
			if !seenListing[a.Source] {
				seenListing[a.Source] = true
				listingOrder = append(listingOrder, a.Source)
			}
			listingBlocks[a.Source] = append(listingBlocks[a.Source], block)
			continue
		}

		signal, err := s.summarizer.Summarize(ctx, a.Title, a.Description)
		if err != nil {
			s.log.Warn("news: ollama analyze failed",
				zap.String("title", a.Title),
				zap.Error(err),
			)
			continue
		}
		signal = strings.TrimSpace(signal)
		s.log.Info("news: ollama signal", zap.String("signal", signal), zap.String("title", a.Title))
		if err := s.repo.UpdateSummary(ctx, a.GUID, signal); err != nil {
			s.log.Warn("news: failed to save ollama signal", zap.String("guid", a.GUID), zap.Error(err))
		}
		if signal == "" || signal == "NONE" {
			continue
		}
		line := formatSignalMsg(signal, a.Link)
		if line == "" {
			continue
		}
		if !seenSource[a.Source] {
			seenSource[a.Source] = true
			sourceOrder = append(sourceOrder, a.Source)
		}
		sourceSignals[a.Source] = append(sourceSignals[a.Source], line)
	}

	for _, src := range listingOrder {
		exchange := strings.ToUpper(src[:1]) + src[1:]
		header := fmt.Sprintf("🚀 <b>Новые листинги — %s</b>", exchange)
		msg := header + "\n\n" + strings.Join(listingBlocks[src], "\n\n")
		go s.notifier.SendToThread(ctx, msg, s.newsThreadID)
	}

	for _, src := range sourceOrder {
		lines := sourceSignals[src]
		msg := fmt.Sprintf("📰 %s\n%s", src, strings.Join(lines, "\n"))
		go s.notifier.SendToThread(ctx, msg, s.newsThreadID)
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

// formatListingBlock формирует блок одного листинга внутри группового сообщения.
func formatListingBlock(a *Article, analysis string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📌 %s\n", a.Title))

	if a.PublishedAt != nil {
		sb.WriteString(fmt.Sprintf("📅 Объявлено: %s UTC\n", a.PublishedAt.Format("02.01.2006 15:04")))
	}

	if a.Description != "" {
		sb.WriteString(fmt.Sprintf("%s\n", a.Description))
	}

	if analysis != "" {
		sb.WriteString(fmt.Sprintf("🤖 %s\n", analysis))
	}

	sb.WriteString(fmt.Sprintf("🔗 <a href=%q>Подробнее</a>", a.Link))
	return sb.String()
}

// formatSignalMsg формирует TG-сообщение из сигнала вида "UP:BTC,ETH", "DOWN:SOL" или "UP:BTC|DOWN:ETH".
func formatSignalMsg(signal, link string) string {
	var lines []string
	for _, part := range strings.Split(signal, "|") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "UP:") {
			tickers := strings.ReplaceAll(strings.TrimPrefix(part, "UP:"), ",", " ")
			lines = append(lines, fmt.Sprintf("🟢 %s 📈", tickers))
		} else if strings.HasPrefix(part, "DOWN:") {
			tickers := strings.ReplaceAll(strings.TrimPrefix(part, "DOWN:"), ",", " ")
			lines = append(lines, fmt.Sprintf("🔴 %s 📉", tickers))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + fmt.Sprintf(" - <a href=%q>читать</a>", link)
}
