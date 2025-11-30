package app

import (
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
func Run() {
	logger.Init()
	logger.Info("Starting Danish News Bot")

	// 1. Загрузка конфигурации
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	// 2. Инициализация кэша
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

	// Инициализируем адаптер (он должен быть определен в cache_adapter.go в этом же пакете)
	cacheAdapter := &FileCacheAdapter{cache: fileCache}

	// 3. Инициализация Gemini
	gmClient, err := gemini.NewClient(cfg.GeminiAPIKey)
	if err != nil {
		log.Fatalf("Gemini error: %v", err)
	}
	defer gmClient.Close()
	news.SetGeminiClient(gmClient)

	// 4. Загрузка RSS фидов
	feeds, err := rss.LoadFeeds(cfg.FeedsConfigPath)
	if err != nil {
		log.Fatalf("Feeds error: %v", err)
	}

	// 5. Скачивание новостей
	// rss.FetchAllFeeds возвращает ([]*FeedItem, error)
	items, err := rss.FetchAllFeeds(feeds)
	if err != nil {
		logger.Error("Fetch error", "err", err)
		return
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
	})
	if err != nil {
		logger.Error("Filter error", "err", err)
		return
	}

	// 7. Отправка в Telegram
	if cfg.BotMode == "single" {
		sendSingleNews(filtered, cfg, cacheAdapter)
	} else {
		sendMultipleNews(filtered, cfg, cacheAdapter, cfg.MaxNewsLimit)
	}

	// Метрики (убрал вызов UpdateTotalProcessed, так как он вызывал ошибку)
	metrics.Global.IncrementTelegramMessagesSent()
}

// sendSingleNews отправляет одну новость (с фото или без)
func sendSingleNews(newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter) {
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
		} else {
			_ = cacheAdapter.MarkAsSent(hash, n.Title, n.Link, n.Category, n.SourceName)
			metrics.Global.IncrementTelegramMessagesSent()
		}

		// В режиме single шлем только одну и выходим
		break
	}
}

// sendMultipleNews отправляет список новостей
func sendMultipleNews(newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter, max int) {
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
		} else {
			_ = cacheAdapter.MarkAsSent(hash, n.Title, n.Link, n.Category, n.SourceName)
			metrics.Global.IncrementTelegramMessagesSent()
			sent++
		}
	}
}

// formatSingleNews используется для создания дайджестов (текстовая версия)
func formatSingleNews(n news.News, number int) string {
	var b strings.Builder
	emoji := news.GetMoodEmoji(n.Mood)

	// Заголовок: Эмодзи + Номер + Ссылка
	b.WriteString(fmt.Sprintf("%s <b>%d.</b> <a href=\"%s\">%s</a>\n", emoji, number, n.Link, n.Title))

	// Украинское саммари
	ukSum := n.SummaryUkrainian
	if ukSum == "" {
		ukSum = n.TitleUkrainian
	}
	if ukSum != "" {
		b.WriteString(fmt.Sprintf("🇺🇦 %s\n", limitText(ukSum, 600)))
	}

	// Датское саммари
	daSum := n.SummaryDanish
	if daSum != "" {
		b.WriteString(fmt.Sprintf("🇩🇰 %s\n", limitText(daSum, 600)))
	}

	b.WriteString("➖➖➖➖➖\n\n")
	return b.String()
}

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
