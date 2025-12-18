package app

import (
	"context"
	"fmt"

	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/gemini"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/storage"
	"github.com/deusflow/News/internal/telegram"
)

type App struct {
	cfg          *config.Config
	metrics      *metrics.Metrics
	cacheAdapter CacheAdapter
	geminiClient *gemini.Client
	feeds        []rss.FeedSource
	keywords     *config.KeywordsConfig
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

	// 2. Инициализация Gemini
	gmClient, err := gemini.NewClient(cfg.GeminiAPIKey, m)
	if err != nil {
		return nil, fmt.Errorf("gemini error: %v", err)
	}

	// 3. Загрузка RSS фидов и ключевых слов
	feeds, err := rss.LoadFeeds(cfg.FeedsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("feeds error: %v", err)
	}

	keywords, err := config.LoadKeywords(cfg.KeywordsConfigPath)
	if err != nil {
		logger.Warn("Failed to load keywords config, using defaults or empty", "error", err)
	}

	return &App{
		cfg:          cfg,
		metrics:      m,
		cacheAdapter: cacheAdapter,
		geminiClient: gmClient,
		feeds:        feeds,
		keywords:     keywords,
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

	defer a.geminiClient.Close()

	// Save file cache on exit if used
	if fileAdapter, ok := a.cacheAdapter.(*FileCacheAdapter); ok {
		defer func() {
			if err := fileAdapter.cache.Save(); err != nil {
				logger.Error("Failed to save cache", "error", err)
			}
		}()
	}

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
	filtered, err := news.FilterAndTranslateWithOptions(items, news.Options{
		Limit:                a.cfg.MaxNewsLimit,
		MaxAge:               a.cfg.NewsMaxAge,
		PerSource:            2,
		MaxGeminiRequests:    a.cfg.MaxGeminiRequests,
		ScrapeMaxArticles:    a.cfg.ScrapeMaxArticles,
		ScrapeConcurrency:    a.cfg.ScrapeConcurrency,
		EnableImportanceLine: a.cfg.EnableImportanceLine,
		Keywords:             a.keywords,
		AIClient:             a.geminiClient,
		Metrics:              a.metrics,
	})
	if err != nil {
		logger.Error("Filter error", "err", err)
		return
	}

	// 7. Отправка в Telegram
	if a.cfg.BotMode == "single" {
		sendSingleNews(filtered, a.cfg, a.cacheAdapter, a.metrics)
	} else {
		sendMultipleNews(filtered, a.cfg, a.cacheAdapter, a.cfg.MaxNewsLimit, a.metrics)
	}

	// Метрики
	a.metrics.IncrementTelegramMessagesSent()
}

// CheckHealth performs health checks on components
func (a *App) CheckHealth(ctx context.Context) map[string]string {
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

	// Check Gemini (simple check if client is initialized)
	if a.geminiClient != nil {
		status["gemini"] = "initialized"
	} else {
		status["gemini"] = "error: not initialized"
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
func sendSingleNews(newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics) {
	for _, n := range newsList {
		hash := cacheAdapter.GenerateNewsHash(n.Title, n.Link)
		if cacheAdapter.IsAlreadySent(hash) {
			continue
		}

		// Решаем, использовать ли фото (если есть URL и текст влезает в лимит 1024)
		canPhoto := n.ImageURL != "" && news.ShouldUsePhoto(n, 1024, 0, 0, 0)
		var outText string
		var err error

		if canPhoto {
			outText = news.FormatCaptionForPhoto(n, 1024, 0, 0)
			err = telegram.SendPhoto(cfg.TelegramToken, cfg.TelegramChatID, n.ImageURL, outText)
		} else {
			outText = news.FormatNewsWithImage(n, 0, 0)
			_, err = telegram.SendMessageAllowPreview(cfg.TelegramToken, cfg.TelegramChatID, outText)
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
			_ = cacheAdapter.MarkAsSent(hash, n.Title, n.Link, n.Category, n.SourceName)
			m.IncrementTelegramMessagesSent()
		}

		// В режиме single шлем только одну и выходим
		break
	}
}

// sendMultipleNews отправляет список новостей
func sendMultipleNews(newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter, max int, m *metrics.Metrics) {
	sent := 0
	for _, n := range newsList {
		if sent >= max {
			break
		}

		hash := cacheAdapter.GenerateNewsHash(n.Title, n.Link)
		if cacheAdapter.IsAlreadySent(hash) {
			continue
		}

		canPhoto := n.ImageURL != "" && news.ShouldUsePhoto(n, 1024, 0, 0, 0)
		var outText string
		var err error

		if canPhoto {
			outText = news.FormatCaptionForPhoto(n, 1024, 0, 0)
			err = telegram.SendPhoto(cfg.TelegramToken, cfg.TelegramChatID, n.ImageURL, outText)
		} else {
			outText = news.FormatNewsWithImage(n, 0, 0)
			_, err = telegram.SendMessageAllowPreview(cfg.TelegramToken, cfg.TelegramChatID, outText)
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
			_ = cacheAdapter.MarkAsSent(hash, n.Title, n.Link, n.Category, n.SourceName)
			m.IncrementTelegramMessagesSent()
			sent++
		}
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

// formatSingleNews используется для создания дайджестов (текстовая версия)
// func formatSingleNews(n news.News, number int) string {
//	var b strings.Builder
//	emoji := news.GetMoodEmoji(n.Mood)
//
//	// Заголовок: Эмодзи + Номер + Ссылка
//	b.WriteString(fmt.Sprintf("%s <b>%d.</b> <a href=\"%s\">%s</a>\n", emoji, number, n.Link, n.Title))
//
//	// Украинское саммари
//	ukSum := n.SummaryUkrainian
//	if ukSum == "" {
//		ukSum = n.TitleUkrainian
//	}
//	if ukSum != "" {
//		b.WriteString(fmt.Sprintf("🇺🇦 %s\n", limitText(ukSum, 600)))
//	}
//
//	// Датское саммари
//	daSum := n.SummaryDanish
//	if daSum != "" {
//		b.WriteString(fmt.Sprintf("🇩🇰 %s\n", limitText(daSum, 600)))
//	}
//
//	b.WriteString("➖➖➖➖➖\n\n")
//	return b.String()
//}

// Вспомогательная функция для обрезки текста
// func limitText(s string, max int) string {
// 	r := []rune(s)
// 	if len(r) <= max {
// 		return s
// 	}
// 	cut := string(r[:max])
// 	// Пытаемся обрезать по точке
// 	if i := strings.LastIndex(cut, "."); i > max/2 {
// 		return string(r[:i+1])
// 	}
// 	// Или по пробелу
// 	if i := strings.LastIndex(cut, " "); i > 0 {
// 		return string(r[:i]) + "..."
// 	}
// 	return string(r[:max]) + "..."
// }
