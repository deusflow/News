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

type App struct {
	cfg              *config.Config
	metrics          *metrics.Metrics
	cacheAdapter     CacheAdapter
	aiManager        *ai.Manager
	feeds            []rss.FeedSource
	keywords         *config.KeywordsConfig
	websiteGenerator *website.Generator
	supabaseClient   *storage.SupabaseClient
}

func New(cfg *config.Config, m *metrics.Metrics) (*App, error) {
	logger.Init()
	logger.Info("Initializing Danish News Bot")

	// 1. Инициализация кэша
	var cacheAdapter CacheAdapter

	if cfg.UsePostgres {
		pgCache, err := storage.NewPostgresCache(cfg.DatabaseURL, cfg.DatabaseTTL)
		if err != nil {
			return nil, fmt.Errorf("postgres error: %v", err)
		}

		// Очистка старых записей (важно для экономии места)
		if err := pgCache.Cleanup(); err != nil {
			logger.Warn("Failed to cleanup postgres cache", "error", err)
		} else {
			logger.Info("Postgres cache cleanup completed", "ttl_hours", cfg.DatabaseTTL)
		}

		cacheAdapter = &PostgresCacheAdapter{cache: pgCache}
		logger.Info("Using PostgreSQL cache")
	} else {
		// Мы используем FileCache из storage, но нам нужен адаптер для работы в app
		fileCache := storage.NewFileCache(cfg.CacheFilePath, cfg.CacheTTLHours)
		if err := fileCache.Load(); err != nil {
			logger.Warn("Failed to load cache", "error", err)
		}
		cacheAdapter = &FileCacheAdapter{cache: fileCache}
		logger.Info("Using File cache")
	}

	// 2. Инициализация AI (НОВАЯ ЛОГИКА)
	var aiProviders []ai.Provider

	for _, pName := range cfg.AIProviders {
		switch strings.TrimSpace(strings.ToLower(pName)) {
		case "gemini":
			client, err := gemini.NewClient(cfg.GeminiAPIKey, cfg.GeminiModel)
			if err != nil {
				logger.Error("Failed to init Gemini", "error", err)
				continue // Не падаем, пробуем следующего
			}
			aiProviders = append(aiProviders, client)
			logger.Info("AI Provider added", "name", "gemini")

		case "groq":
			if cfg.GroqAPIKey != "" {
				client := groq.NewClient(cfg.GroqAPIKey)
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
	feeds, err := rss.LoadFeeds(cfg.FeedsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("feeds error: %v", err)
	}

	keywords, err := config.LoadKeywords(cfg.KeywordsConfigPath)
	if err != nil {
		logger.Warn("Failed to load keywords config, using defaults or empty", "error", err)
	}

	// Initialize website generator
	websiteGen := website.NewGenerator(cfg.WebsiteContentDir, cfg.EnableWebsite)
	if cfg.EnableWebsite {
		logger.Info("Website generation enabled", "content_dir", cfg.WebsiteContentDir)
	}

	// Initialize Supabase client for website archive
	var supabaseClient *storage.SupabaseClient
	if cfg.EnableSupabase {
		supabaseClient, err = storage.NewSupabaseClient(cfg.SupabaseURL, cfg.SupabaseServiceKey)
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

	return &App{
		cfg:              cfg,
		metrics:          m,
		cacheAdapter:     cacheAdapter,
		aiManager:        aiManager,
		feeds:            feeds,
		keywords:         keywords,
		websiteGenerator: websiteGen,
		supabaseClient:   supabaseClient,
	}, nil
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
		if fileAdapter, ok := a.cacheAdapter.(*FileCacheAdapter); ok {
			if err := fileAdapter.cache.Save(); err != nil {
				logger.Error("Failed to save cache", "error", err)
			}
		}
	}()

	// 4.1 Обработка очереди неудачных сообщений (DLQ)
	if pgAdapter, ok := a.cacheAdapter.(*PostgresCacheAdapter); ok {
		processFailedMessages(pgAdapter, a.cfg, a.metrics)
	}

	// 5. Скачивание новостей
	items, err := rss.FetchAllFeeds(a.feeds)
	if err != nil {
		logger.Error("Fetch error", "err", err)
		return
	}

	// Обновляем метрики
	for range items {
		a.metrics.IncrementNewsProcessed()
	}

	// 6. Фильтрация и перевод
	filtered, err := news.FilterAndTranslateWithOptions(ctx, items, news.Options{
		Limit:             a.cfg.MaxNewsLimit,
		MaxAge:            a.cfg.NewsMaxAge,
		PerSource:         2,
		ScrapeMaxArticles: a.cfg.ScrapeMaxArticles,
		ScrapeConcurrency: a.cfg.ScrapeConcurrency,
		Keywords:          a.keywords,
		AI:                a.aiManager,
		Metrics:           a.metrics,
	})
	if err != nil {
		logger.Error("Filter error", "err", err)
		return
	}

	// 7. Отправка в Telegram
	if a.cfg.BotMode == "single" {
		sendSingleNews(filtered, a.cfg, a.cacheAdapter, a.metrics, a.websiteGenerator, a.supabaseClient)
	} else {
		sendMultipleNews(filtered, a.cfg, a.cacheAdapter, a.cfg.MaxNewsLimit, a.metrics, a.websiteGenerator, a.supabaseClient)
	}

	// Метрики
	a.metrics.IncrementTelegramMessagesSent()
}

// CheckHealth performs health checks on components
func (a *App) CheckHealth(_ context.Context) map[string]string {
	status := make(map[string]string)
	status["app"] = "ok"

	// Check DB
	if pgAdapter, ok := a.cacheAdapter.(*PostgresCacheAdapter); ok {
		if err := pgAdapter.cache.Ping(); err != nil {
			status["db"] = fmt.Sprintf("error: %v", err)
		} else {
			status["db"] = "ok"
		}
	} else {
		status["db"] = "ok (file cache)"
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
	feeds, err := rss.LoadFeeds(a.cfg.FeedsConfigPath)
	if err != nil {
		return fmt.Errorf("failed to reload feeds: %v", err)
	}
	a.feeds = feeds

	// Reload keywords
	keywords, err := config.LoadKeywords(a.cfg.KeywordsConfigPath)
	if err != nil {
		logger.Warn("Failed to reload keywords, keeping old ones", "error", err)
	} else {
		a.keywords = keywords
	}

	logger.Info("Configuration reloaded successfully")
	return nil
}

// sendSingleNews отправляет одну новость (с фото или без)
func sendSingleNews(newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics, websiteGen *website.Generator, supabase *storage.SupabaseClient) {
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

		// Решаем, использовать ли фото (если есть URL и текст влезает в лимит 1024)
		canPhoto := n.ImageURL != "" && news.ShouldUsePhoto(n, 1024, 0, 0, 0)
		var outText string
		var err error

		// Prepare buttons if enabled (только ссылка на оригінал)
		var buttons [][]telegram.InlineButton
		if cfg.EnableInlineButtons && n.Link != "" {
			buttons = append(buttons, []telegram.InlineButton{
				{Text: "🔗 Читати оригінал / Læs mere", URL: n.Link},
			})
		}

		if canPhoto {
			outText = news.FormatCaptionForPhoto(n, 1024, 0, 0)
			if len(buttons) > 0 {
				err = telegram.SendPhotoWithButtons(cfg.TelegramToken, cfg.TelegramChatID, n.ImageURL, outText, buttons)
			} else {
				err = telegram.SendPhoto(cfg.TelegramToken, cfg.TelegramChatID, n.ImageURL, outText)
			}
		} else {
			outText = news.FormatNewsWithImage(n, 0, 0)
			if len(buttons) > 0 {
				_, err = telegram.SendMessageWithButtons(cfg.TelegramToken, cfg.TelegramChatID, outText, buttons, true, 0)
			} else {
				_, err = telegram.SendMessageAllowPreview(cfg.TelegramToken, cfg.TelegramChatID, outText)
			}
		}

		if err != nil {
			logger.Error("Failed to send telegram message", "title", n.Title, "error", err)
			// Save to DLQ if using Postgres
			if pgAdapter, ok := cacheAdapter.(*PostgresCacheAdapter); ok {
				if saveErr := pgAdapter.cache.SaveFailedNews(n.Title, n.Link, n.ImageURL, outText, err.Error()); saveErr != nil {
					logger.Error("Failed to save to DLQ", "error", saveErr)
				} else {
					logger.Info("Saved failed message to DLQ", "title", n.Title)
				}
			}
		} else {
			// Use MarkAsSentWithContent to store content hash for future duplicate detection
			_ = cacheAdapter.MarkAsSentWithContent(hash, n.Title, n.Link, n.Content, n.Category, n.SourceName)
			m.IncrementTelegramMessagesSent()

			// Save to Supabase ONLY after successful Telegram send (1:1 relationship)
			if supabase != nil {
				saveToSupabase(supabase, n)
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
func sendMultipleNews(newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter, max int, m *metrics.Metrics, websiteGen *website.Generator, supabase *storage.SupabaseClient) {
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

		// Решаем, использовать ли фото (если есть URL и текст влезает в лимит 1024)
		canPhoto := n.ImageURL != "" && news.ShouldUsePhoto(n, 1024, 0, 0, 0)
		var outText string
		var err error

		// Prepare buttons if enabled (только ссылка на оригінал)
		var buttons [][]telegram.InlineButton
		if cfg.EnableInlineButtons && n.Link != "" {
			buttons = append(buttons, []telegram.InlineButton{
				{Text: "🔗 Читати оригінал / Læs mere", URL: n.Link},
			})
		}

		if canPhoto {
			outText = news.FormatCaptionForPhoto(n, 1024, 0, 0)
			if len(buttons) > 0 {
				err = telegram.SendPhotoWithButtons(cfg.TelegramToken, cfg.TelegramChatID, n.ImageURL, outText, buttons)
			} else {
				err = telegram.SendPhoto(cfg.TelegramToken, cfg.TelegramChatID, n.ImageURL, outText)
			}
		} else {
			outText = news.FormatNewsWithImage(n, 0, 0)
			if len(buttons) > 0 {
				_, err = telegram.SendMessageWithButtons(cfg.TelegramToken, cfg.TelegramChatID, outText, buttons, true, 0)
			} else {
				_, err = telegram.SendMessageAllowPreview(cfg.TelegramToken, cfg.TelegramChatID, outText)
			}
		}

		if err != nil {
			logger.Error("Failed to send telegram message", "title", n.Title, "error", err)
			// Save to DLQ if using Postgres
			if pgAdapter, ok := cacheAdapter.(*PostgresCacheAdapter); ok {
				if saveErr := pgAdapter.cache.SaveFailedNews(n.Title, n.Link, n.ImageURL, outText, err.Error()); saveErr != nil {
					logger.Error("Failed to save to DLQ", "error", saveErr)
				} else {
					logger.Info("Saved failed message to DLQ", "title", n.Title)
				}
			}
		} else {
			// Use MarkAsSentWithContent to store content hash for future duplicate detection
			_ = cacheAdapter.MarkAsSentWithContent(hash, n.Title, n.Link, n.Content, n.Category, n.SourceName)
			m.IncrementTelegramMessagesSent()
			sent++

			// Save to Supabase ONLY after successful Telegram send (1:1 relationship)
			if supabase != nil {
				saveToSupabase(supabase, n)
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
func saveToSupabase(client *storage.SupabaseClient, n news.News) {
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

	if err := client.SaveNews(archive); err != nil {
		logger.Warn("Failed to save to Supabase", "title", n.Title, "error", err)
	} else {
		logger.Info("Saved to Supabase archive", "title", n.Title)
	}
}

func processFailedMessages(adapter *PostgresCacheAdapter, cfg *config.Config, m *metrics.Metrics) {
	items, err := adapter.cache.GetFailedNews(5) // Process max 5 failed items per run
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
			err = telegram.SendPhoto(cfg.TelegramToken, cfg.TelegramChatID, item.ImageURL, item.MessageText)
		} else {
			_, err = telegram.SendMessageAllowPreview(cfg.TelegramToken, cfg.TelegramChatID, item.MessageText)
		}

		if err != nil {
			logger.Error("Failed to resend DLQ item", "id", item.ID, "error", err)
			if updateErr := adapter.cache.IncrementFailedAttempts(item.ID, err.Error()); updateErr != nil {
				logger.Error("Failed to update DLQ attempts", "error", updateErr)
			}
		} else {
			logger.Info("Successfully resent DLQ item", "id", item.ID)
			if delErr := adapter.cache.DeleteFailedNews(item.ID); delErr != nil {
				logger.Error("Failed to delete DLQ item", "error", delErr)
			}
			// Also mark as sent in main table to avoid re-processing if it comes from RSS again
			hash := adapter.GenerateNewsHash(item.Title, item.Link)
			_ = adapter.MarkAsSent(hash, item.Title, item.Link, "DLQ", "DLQ")
			m.IncrementTelegramMessagesSent()
		}
	}
}
