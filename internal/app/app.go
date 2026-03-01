package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/ai/gemini"
	"github.com/deusflow/News/internal/ai/groq"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/storage"
	"github.com/deusflow/News/internal/telegram"
	"github.com/deusflow/News/internal/website"
)

// Service interfaces for SRP
type NewsFetcher interface {
	Fetch(ctx context.Context) ([]*rss.FeedItem, error)
}

type NewsProcessor interface {
	Process(ctx context.Context, items []*rss.FeedItem) ([]news.News, error)
}

type NewsSender interface {
	SendSingle(ctx context.Context, newsList []news.News)
	SendMultiple(ctx context.Context, newsList []news.News, max int)
}

type HealthChecker interface {
	CheckHealth(ctx context.Context) map[string]string
}

// RSSFetcher implements NewsFetcher
type RSSFetcher struct {
	feeds []rss.FeedSource
}

func NewRSSFetcher(feeds []rss.FeedSource) *RSSFetcher {
	return &RSSFetcher{feeds: feeds}
}

func (f *RSSFetcher) Fetch(ctx context.Context) ([]*rss.FeedItem, error) {
	return rss.FetchAllFeeds(f.feeds)
}

// NewsFilterProcessor implements NewsProcessor
type NewsFilterProcessor struct {
	cfg      *config.Config
	aiMgr    *ai.Manager
	metrics  *metrics.Metrics
	keywords *config.KeywordsConfig
}

func NewNewsFilterProcessor(cfg *config.Config, aiMgr *ai.Manager, m *metrics.Metrics, keywords *config.KeywordsConfig) *NewsFilterProcessor {
	return &NewsFilterProcessor{
		cfg:      cfg,
		aiMgr:    aiMgr,
		metrics:  m,
		keywords: keywords,
	}
}

func (p *NewsFilterProcessor) Process(ctx context.Context, items []*rss.FeedItem) ([]news.News, error) {
	return news.FilterAndTranslateWithOptions(ctx, items, news.Options{
		Limit:             p.cfg.RSS.MaxNewsLimit,
		MaxAge:            p.cfg.RSS.NewsMaxAge,
		PerSource:         2,
		ScrapeMaxArticles: p.cfg.Scraper.MaxArticles,
		ScrapeConcurrency: p.cfg.Scraper.Concurrency,
		Keywords:          p.keywords,
		AI:                p.aiMgr,
		Metrics:           p.metrics,
	})
}

// TelegramNewsSender implements NewsSender
type TelegramNewsSender struct {
	cfg              *config.Config
	cacheAdapter     CacheAdapter
	metrics          *metrics.Metrics
	websiteGenerator *website.Generator
	supabaseClient   *storage.SupabaseClient
}

func NewTelegramNewsSender(cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics, websiteGen *website.Generator, supabase *storage.SupabaseClient) *TelegramNewsSender {
	return &TelegramNewsSender{
		cfg:              cfg,
		cacheAdapter:     cacheAdapter,
		metrics:          m,
		websiteGenerator: websiteGen,
		supabaseClient:   supabase,
	}
}

func (s *TelegramNewsSender) SendSingle(ctx context.Context, newsList []news.News) {
	sendSingleNews(ctx, newsList, s.cfg, s.cacheAdapter, s.metrics, s.websiteGenerator, s.supabaseClient)
}

func (s *TelegramNewsSender) SendMultiple(ctx context.Context, newsList []news.News, max int) {
	sendMultipleNews(ctx, newsList, s.cfg, s.cacheAdapter, max, s.metrics, s.websiteGenerator, s.supabaseClient)
}

type App struct {
	cfg              *config.Config
	metrics          *metrics.Metrics
	cacheAdapter     CacheAdapter
	aiManager        *ai.Manager
	fetcher          NewsFetcher
	processor        NewsProcessor
	sender           NewsSender
	keywords         *config.KeywordsConfig
	websiteGenerator *website.Generator
	supabaseClient   *storage.SupabaseClient
}

func New(cfg *config.Config, m *metrics.Metrics) (*App, error) {
	logger.Init()
	logger.Info("Initializing Danish News Bot")

	// 1. Инициализация кэша
	var cacheAdapter CacheAdapter

	if cfg.Database.UsePostgres {
		pgCache, err := storage.NewPostgresCache(cfg.Database.URL, cfg.Database.TTL)
		if err != nil {
			return nil, fmt.Errorf("postgres error: %v", err)
		}

		// Очистка старых записей (важно для экономии места)
		if err := pgCache.Cleanup(); err != nil {
			logger.Warn("Failed to cleanup postgres cache", "error", err)
		} else {
			logger.Info("Postgres cache cleanup completed", "ttl_hours", cfg.Database.TTL)
		}

		cacheAdapter = &PostgresCacheAdapter{cache: pgCache}
		logger.Info("Using PostgreSQL cache")
	} else {
		// Мы используем FileCache из storage, но нам нужен адаптер для работы в app
		fileCache := storage.NewFileCache(cfg.Cache.FilePath, cfg.Cache.TTLHours)
		if err := fileCache.Load(); err != nil {
			logger.Warn("Failed to load cache", "error", err)
		}
		cacheAdapter = &FileCacheAdapter{cache: fileCache}
		logger.Info("Using File cache")
	}

	// 2. Инициализация AI (НОВАЯ ЛОГИКА)
	var aiProviders []ai.Provider

	for _, pName := range cfg.AI.Providers {
		switch strings.TrimSpace(strings.ToLower(pName)) {
		case "gemini":
			client, err := gemini.NewClient(cfg.AI.GeminiAPIKey, cfg.AI.GeminiModel)
			if err != nil {
				logger.Error("Failed to init Gemini", "error", err)
				continue // Не падаем, пробуем следующего
			}
			aiProviders = append(aiProviders, client)
			logger.Info("AI Provider added", "name", "gemini")

		case "groq":
			if cfg.AI.GroqAPIKey != "" {
				client := groq.NewClient(cfg.AI.GroqAPIKey)
				aiProviders = append(aiProviders, client)
				logger.Info("AI Provider added", "name", "groq")
			} else {
				logger.Warn("Groq API Key is missing, skipping")
			}
		default:
			logger.Warn("Unknown AI provider in config, skipping", "provider", pName)
		}
	}

	if len(aiProviders) == 0 {
		return nil, fmt.Errorf("no AI providers initialized (check config and keys)")
	}

	aiManager := ai.NewManager(m, aiProviders...)
	logger.Info("AI Manager initialized", "providers_count", len(aiProviders))

	// 3. Загрузка RSS фидов и ключевых слов
	feeds, err := rss.LoadFeeds(cfg.RSS.FeedsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("feeds error: %v", err)
	}

	keywords, err := config.LoadKeywords(cfg.RSS.KeywordsConfigPath)
	if err != nil {
		logger.Warn("Failed to load keywords config, using defaults or empty", "error", err)
	}

	// Initialize website generator
	websiteGen := website.NewGenerator(cfg.Website.ContentDir, cfg.Website.Enable)
	if cfg.Website.Enable {
		logger.Info("Website generation enabled", "content_dir", cfg.Website.ContentDir)
	}

	// Initialize Supabase client for website archive
	var supabaseClient *storage.SupabaseClient
	if cfg.Supabase.Enable {
		supabaseClient, err = storage.NewSupabaseClient(cfg.Supabase.URL, cfg.Supabase.ServiceKey)
		if err != nil {
			logger.Warn("Failed to initialize Supabase client", "error", err)
		} else {
			// Test connection
			if pingErr := supabaseClient.Ping(); pingErr != nil {
				logger.Warn("Supabase ping failed", "error", pingErr)
				supabaseClient = nil
			} else {
				logger.Info("Supabase client initialized for website archive")
				// Archive old news (older than 10 days)
				if archiveErr := supabaseClient.ArchiveOldNews(); archiveErr != nil {
					logger.Warn("Failed to archive old news", "error", archiveErr)
				}
			}
		}
	}

	app := &App{
		cfg:              cfg,
		metrics:          m,
		cacheAdapter:     cacheAdapter,
		aiManager:        aiManager,
		fetcher:          NewRSSFetcher(feeds),
		processor:        NewNewsFilterProcessor(cfg, aiManager, m, keywords),
		sender:           NewTelegramNewsSender(cfg, cacheAdapter, m, websiteGen, supabaseClient),
		keywords:         keywords,
		websiteGenerator: websiteGen,
		supabaseClient:   supabaseClient,
	}

	return app, nil
}

// Run запускает приложение
func (a *App) Run(ctx context.Context) {
	logger.Info("Starting Danish News Bot Run")

	// Check for cancellation immediately
	if ctx.Err() != nil {
		logger.Info("Context cancelled before start")
		return
	}

	defer a.aiManager.Close()

	defer func() {
		switch adapter := a.cacheAdapter.(type) {
		case *FileCacheAdapter:
			if err := adapter.cache.Save(); err != nil {
				logger.Error("Failed to save file cache", "error", err)
			}
		case *PostgresCacheAdapter:
			// Явно закрываем connection pool — критично для Neon free tier
			// (ограниченное число одновременных подключений)
			if err := adapter.cache.Close(); err != nil {
				logger.Error("Failed to close postgres connection", "error", err)
			}
		}
	}()

	// 4.1 Обработка очереди неудачных сообщений (DLQ)
	// ИСПРАВЛЕНО: передаем интерфейс, а не конкретный тип
	processFailedMessages(a.cacheAdapter, a.cfg, a.metrics)

	// 5. Скачивание новостей
	items, err := a.fetcher.Fetch(ctx)
	if err != nil {
		logger.Error("Fetch error", "err", err)
		return
	}

	// Обновляем метрики
	for range items {
		a.metrics.IncrementNewsProcessed()
	}

	// 6. Фильтрация и перевод
	filtered, err := a.processor.Process(ctx, items)
	if err != nil {
		logger.Error("Filter error", "err", err)
		return
	}

	// 7. Отправка в Telegram
	if a.cfg.Telegram.BotMode == "single" {
		a.sender.SendSingle(ctx, filtered)
	} else {
		a.sender.SendMultiple(ctx, filtered, a.cfg.RSS.MaxNewsLimit)
	}

	// Метрики
	a.metrics.IncrementTelegramMessagesSent()
}

// CheckHealth performs health checks on components
func (a *App) CheckHealth(_ context.Context) map[string]string {
	status := make(map[string]string)
	status["app"] = "ok"

	// ИСПРАВЛЕНО: Убрал ошибочный код.
	// Так как у нас общий интерфейс, мы просто проверяем наличие адаптера.
	if a.cacheAdapter != nil {
		status["db"] = "connected"
	} else {
		status["db"] = "error"
	}

	// Check AI manager
	if a.aiManager != nil {
		status["ai"] = "ready"
	} else {
		status["ai"] = "not_initialized"
	}

	return status
}

// ReloadConfig reloads configuration (Hot Reload)
func (a *App) ReloadConfig() error {
	logger.Info("Reloading configuration...")

	// Reload feeds
	feeds, err := rss.LoadFeeds(a.cfg.RSS.FeedsConfigPath)
	if err != nil {
		return fmt.Errorf("failed to reload feeds: %v", err)
	}
	if rssFetcher, ok := a.fetcher.(*RSSFetcher); ok {
		rssFetcher.feeds = feeds
	}

	// Reload keywords
	keywords, err := config.LoadKeywords(a.cfg.RSS.KeywordsConfigPath)
	if err != nil {
		logger.Warn("Failed to reload keywords, keeping old ones", "error", err)
	} else {
		a.keywords = keywords
		if filterProcessor, ok := a.processor.(*NewsFilterProcessor); ok {
			filterProcessor.keywords = keywords
		}
	}

	logger.Info("Configuration reloaded successfully")
	return nil
}

// sendSingleNews отправляет одну новость (с фото или без)
func sendSingleNews(ctx context.Context, newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics, websiteGen *website.Generator, supabase *storage.SupabaseClient) {
	for _, n := range newsList {
		hash := cacheAdapter.GenerateNewsHash(n.Title, n.Link)
		if cacheAdapter.IsAlreadySent(hash) {
			continue
		}

		// Check for content duplicate (same news from different sources)
		if isDuplicate, existingTitle := cacheAdapter.IsContentDuplicate(n.Content); isDuplicate {
			logger.Info("⏭️ Skipping duplicate news (same content found)",
				"new_title", n.Title,
				"existing_title", existingTitle)
			m.IncrementDuplicatesFiltered()
			continue
		}

		// ShouldUsePhoto returns false if: no ImageURL OR caption won't fit in 1024 chars.
		// In the latter case we fall back to text mode (4096 limit) so the news is always shown complete.
		canPhoto := news.ShouldUsePhoto(n, cfg.Posting.PhotoTextLimit)
		if n.ImageURL != "" && !canPhoto {
			logger.Info("📝 Photo skipped — content too long for caption, using text mode", "title", n.Title)
		}
		var outText string
		var err error

		var buttons [][]telegram.InlineButton
		if cfg.Feature.EnableInlineButtons && n.Link != "" {
			buttons = append(buttons, []telegram.InlineButton{
				{Text: "🔗 Читати оригінал / Læs mere", URL: n.Link},
			})
		}

		if canPhoto {
			outText = news.FormatCaptionForPhoto(n, cfg.Posting.PhotoTextLimit)
			if len(buttons) > 0 {
				err = telegram.SendPhotoWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, n.ImageURL, outText, buttons)
			} else {
				err = telegram.SendPhoto(cfg.Telegram.Token, cfg.Telegram.ChatID, n.ImageURL, outText)
			}
		} else {
			outText = news.FormatNewsWithImage(n)
			if len(buttons) > 0 {
				_, err = telegram.SendMessageWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, outText, buttons, true, 0)
			} else {
				_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, outText)
			}
		}

		if err != nil {
			logger.Error("Failed to send telegram message", "title", n.Title, "error", err)

			// ИСПРАВЛЕНО: Используем метод интерфейса напрямую, без проверки типа
			if saveErr := cacheAdapter.SaveFailedNews(n.Title, n.Link, n.ImageURL, outText, err.Error()); saveErr != nil {
				logger.Error("Failed to save to DLQ", "error", saveErr)
			} else {
				// Если это FileCache, он просто вернет nil, и мы попадем сюда, но это нормально
				logger.Info("News processing failed (DLQ check passed)", "title", n.Title)
			}

		} else {
			// Use MarkAsSentWithContent to store content hash for future duplicate detection
			_ = cacheAdapter.MarkAsSentWithContent(hash, n.Title, n.Link, n.Content, n.Category, n.SourceName)
			m.IncrementTelegramMessagesSent()

			// Save to Supabase ONLY after successful Telegram send (1:1 relationship)
			if supabase != nil {
				saveToSupabase(ctx, supabase, n)
			}

			// Generate website post (SYNC - must complete before workflow exits)
			if websiteGen != nil && websiteGen.IsEnabled() {
				generateWebsitePost(websiteGen, n)
			}
		}

		// В режиме single шлем только одну и выходим
		break
	}
}

// sendMultipleNews отправляет список новостей
func sendMultipleNews(ctx context.Context, newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter, max int, m *metrics.Metrics, websiteGen *website.Generator, supabase *storage.SupabaseClient) {
	sent := 0
	for _, n := range newsList {
		if sent >= max {
			break
		}

		hash := cacheAdapter.GenerateNewsHash(n.Title, n.Link)
		if cacheAdapter.IsAlreadySent(hash) {
			continue
		}

		// Check for content duplicate (same news from different sources)
		if isDuplicate, existingTitle := cacheAdapter.IsContentDuplicate(n.Content); isDuplicate {
			logger.Info("⏭️ Skipping duplicate news (same content found)",
				"new_title", n.Title,
				"existing_title", existingTitle)
			m.IncrementDuplicatesFiltered()
			continue
		}

		canPhoto := news.ShouldUsePhoto(n, cfg.Posting.PhotoTextLimit)
		if n.ImageURL != "" && !canPhoto {
			logger.Info("📝 Photo skipped — content too long for caption, using text mode", "title", n.Title)
		}
		var outText string
		var err error

		var buttons [][]telegram.InlineButton
		if cfg.Feature.EnableInlineButtons && n.Link != "" {
			buttons = append(buttons, []telegram.InlineButton{
				{Text: "🔗 Читати оригінал / Læs mere", URL: n.Link},
			})
		}

		if canPhoto {
			outText = news.FormatCaptionForPhoto(n, cfg.Posting.PhotoTextLimit)
			if len(buttons) > 0 {
				err = telegram.SendPhotoWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, n.ImageURL, outText, buttons)
			} else {
				err = telegram.SendPhoto(cfg.Telegram.Token, cfg.Telegram.ChatID, n.ImageURL, outText)
			}
		} else {
			outText = news.FormatNewsWithImage(n)
			if len(buttons) > 0 {
				_, err = telegram.SendMessageWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, outText, buttons, true, 0)
			} else {
				_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, outText)
			}
		}

		if err != nil {
			logger.Error("Failed to send telegram message", "title", n.Title, "error", err)

			// ИСПРАВЛЕНО: Используем метод интерфейса напрямую, без проверки типа
			if saveErr := cacheAdapter.SaveFailedNews(n.Title, n.Link, n.ImageURL, outText, err.Error()); saveErr != nil {
				logger.Error("Failed to save to DLQ", "error", saveErr)
			} else {
				logger.Info("News processing failed (DLQ check passed)", "title", n.Title)
			}

		} else {
			// Use MarkAsSentWithContent to store content hash for future duplicate detection
			_ = cacheAdapter.MarkAsSentWithContent(hash, n.Title, n.Link, n.Content, n.Category, n.SourceName)
			m.IncrementTelegramMessagesSent()
			sent++

			// Save to Supabase ONLY after successful Telegram send (1:1 relationship)
			if supabase != nil {
				saveToSupabase(ctx, supabase, n)
			}

			// Generate website post (SYNC - must complete before workflow exits)
			if websiteGen != nil && websiteGen.IsEnabled() {
				generateWebsitePost(websiteGen, n)
			}
		}
	}
}

// generateWebsitePost converts news.News to website.NewsPost and generates the post with timeout
func generateWebsitePost(gen *website.Generator, n news.News) {
	post := website.NewsPost{
		Title:            n.Title,
		TitleUkrainian:   n.TitleUkrainian,
		Content:          n.Content,
		SummaryUkrainian: n.SummaryUkrainian,
		SummaryDanish:    n.SummaryDanish,
		Link:             n.Link,
		ImageURL:         n.ImageURL,
		SourceName:       n.SourceName,
		Category:         n.Category,
		Tags:             n.Tags,
		Mood:             n.Mood,
		TLDR:             n.TLDR,
		FunFact:          n.FunFact,
		PublishedAt:      n.Published,
	}

	// Use timeout to prevent hanging goroutines (10 seconds should be plenty for file write)
	ctx := context.Background()
	if err := gen.GeneratePostWithTimeout(ctx, post, 10*time.Second); err != nil {
		logger.Warn("Failed to generate website post", "title", n.Title, "error", err)
	} else {
		logger.Info("Generated website post", "title", n.Title)
	}
}

// saveToSupabase saves news to Supabase for website archive
func saveToSupabase(ctx context.Context, client *storage.SupabaseClient, n news.News) {
	archive := storage.NewsArchive{
		Slug:             storage.GenerateSlugWithDate(n.Title, n.Published),
		Title:            n.Title,
		TitleUkrainian:   n.TitleUkrainian,
		SummaryUkrainian: n.SummaryUkrainian,
		SummaryDanish:    n.SummaryDanish,
		TLDR:             n.TLDR,
		FunFact:          n.FunFact,
		ImageURL:         n.ImageURL,
		SourceURL:        n.Link,
		SourceName:       n.SourceName,
		Category:         n.Category,
		Tags:             n.Tags,
		Mood:             n.Mood,
		PublishedAt:      n.Published,
	}

	if err := client.SaveNews(ctx, archive); err != nil {
		logger.Warn("Failed to save to Supabase", "title", n.Title, "error", err)
	} else {
		logger.Info("Saved to Supabase archive", "title", n.Title)
	}
}

// processFailedMessages принимает теперь любой CacheAdapter (Interface), а не конкретный тип
func processFailedMessages(adapter CacheAdapter, cfg *config.Config, m *metrics.Metrics) {
	// Мы просим адаптер: "Дай мне список ошибок".
	// Нам плевать, откуда он их возьмет.
	items, err := adapter.GetFailedNews(5)
	if err != nil {
		logger.Error("Failed to get DLQ items", "error", err)
		return
	}

	if len(items) > 0 {
		logger.Info("Processing failed messages", "count", len(items))
	}

	for _, item := range items {
		var err error
		if item.ImageURL != "" {
			err = telegram.SendPhoto(cfg.Telegram.Token, cfg.Telegram.ChatID, item.ImageURL, item.MessageText)
		} else {
			_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, item.MessageText)
		}

		if err != nil {
			logger.Error("Failed to resend DLQ item", "id", item.ID, "error", err)
			// Вызываем метод интерфейса
			if updateErr := adapter.IncrementFailedAttempts(item.ID, err.Error()); updateErr != nil {
				logger.Error("Failed to update DLQ attempts", "error", updateErr)
			}
		} else {
			logger.Info("Successfully resent DLQ item", "id", item.ID)
			// Вызываем метод интерфейса
			if delErr := adapter.DeleteFailedNews(item.ID); delErr != nil {
				logger.Error("Failed to delete DLQ item", "error", delErr)
			}

			// Также отмечаем как отправленное в основной таблице
			hash := adapter.GenerateNewsHash(item.Title, item.Link)
			_ = adapter.MarkAsSent(hash, item.Title, item.Link, "DLQ", "DLQ")
			m.IncrementTelegramMessagesSent()
		}
	}
}
