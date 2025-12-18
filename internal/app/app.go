package app

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/gemini"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/storage"
	"github.com/deusflow/News/internal/telegram"
)

// Run запускает приложение
func Run(ctx context.Context, cfg *config.Config, m *metrics.Metrics) {
	logger.Init()
	logger.Info("Starting Danish News Bot")

	// Check for cancellation immediately
	if ctx.Err() != nil {
		logger.Info("Context cancelled before start")
		return
	}

	// 1. Загрузка конфигурации - теперь передается извне
	// cfg, err := config.Load()
	// if err != nil {
	// 	log.Fatalf("Config error: %v", err)
	// }

	// 2. Инициализация кэша
	var cacheAdapter CacheAdapter

	if cfg.UsePostgres {
		pgCache, err := storage.NewPostgresCache(cfg.DatabaseURL, cfg.DatabaseTTL)
		if err != nil {
			log.Fatalf("Postgres error: %v", err)
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
		// Важно: сохраняем кэш при выходе
		defer func() {
			if err := fileCache.Save(); err != nil {
				logger.Error("Failed to save cache", "error", err)
			}
		}()
		cacheAdapter = &FileCacheAdapter{cache: fileCache}
		logger.Info("Using File cache")
	}

	// 3. Инициализация Gemini
	gmClient, err := gemini.NewClient(cfg.GeminiAPIKey, m)
	if err != nil {
		log.Fatalf("Gemini error: %v", err)
	}
	defer gmClient.Close()

	// 4. Загрузка RSS фидов и ключевых слов
	feeds, err := rss.LoadFeeds(cfg.FeedsConfigPath)
	if err != nil {
		log.Fatalf("Feeds error: %v", err)
	}

	keywords, err := config.LoadKeywords(cfg.KeywordsConfigPath)
	if err != nil {
		logger.Warn("Failed to load keywords config, using defaults or empty", "error", err)
		// Можно создать дефолтный конфиг, если нужно, но пока оставим как есть (будет ошибка в news если nil)
		// Но мы сделали проверку на nil в news.go
	}

	// 5. Скачивание новостей
	// rss.FetchAllFeeds возвращает ([]*FeedItem, error)
	items, err := rss.FetchAllFeeds(feeds)
	if err != nil {
		logger.Error("Fetch error", "err", err)
		return
	}

	// Обновляем метрики
	for range items {
		m.IncrementNewsProcessed()
	}

	// 6. Фильтрация и перевод
	// news.FilterAndTranslateWithOptions возвращает ([]News, error)
	filtered, err := news.FilterAndTranslateWithOptions(items, news.Options{
		Limit:                cfg.MaxNewsLimit,
		MaxAge:               cfg.NewsMaxAge,
		PerSource:            2,
		MaxGeminiRequests:    cfg.MaxGeminiRequests,
		ScrapeMaxArticles:    cfg.ScrapeMaxArticles,
		ScrapeConcurrency:    cfg.ScrapeConcurrency,
		EnableImportanceLine: cfg.EnableImportanceLine,
		Keywords:             keywords,
		AIClient:             gmClient,
		Metrics:              m,
	})
	if err != nil {
		logger.Error("Filter error", "err", err)
		return
	}

	// 7. Отправка в Telegram
	if cfg.BotMode == "single" {
		sendSingleNews(filtered, cfg, cacheAdapter, m)
	} else {
		sendMultipleNews(filtered, cfg, cacheAdapter, cfg.MaxNewsLimit, m)
	}

	// Метрики
	m.IncrementTelegramMessagesSent()
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
func limitText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := string(r[:max])
	// Пытаемся обрезать по точке
	if i := strings.LastIndex(cut, "."); i > max/2 {
		return string(r[:i+1])
	}
	// Или по пробелу
	if i := strings.LastIndex(cut, " "); i > 0 {
		return string(r[:i]) + "..."
	}
	return string(r[:max]) + "..."
}
