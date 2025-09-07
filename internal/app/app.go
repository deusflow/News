package app

import (
	"dknews/internal/config"
	"dknews/internal/gemini"
	"dknews/internal/logger"
	"dknews/internal/metrics"
	"dknews/internal/news"
	"dknews/internal/rss"
	"dknews/internal/telegram"
	"fmt"
	"html"
	"log"
	"regexp"
	"strings"
)

// formatNewsMessage builds grouped message using AI summaries (Ukrainian priority, then Danish)
func formatNewsMessage(newsList []news.News, max int) string {
	var b strings.Builder

	// Group news by category
	ukraineNews := []news.News{}
	denmarkNews := []news.News{}
	for _, n := range newsList {
		if len(ukraineNews)+len(denmarkNews) >= max {
			break
		}

		if n.Category == "ukraine" {
			ukraineNews = append(ukraineNews, n)
		} else if n.Category == "denmark" {
			denmarkNews = append(denmarkNews, n)
		}
	}

	b.WriteString("🇩🇰 <b>Новини Данії</b> 🇺🇦\n")
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	count := 1

	// First Ukraine news (priority)
	if len(ukraineNews) > 0 {
		b.WriteString("🇺🇦 <b>УКРАЇНА В ДАНІЇ</b>\n\n")
		for _, n := range ukraineNews {
			if count > max {
				break
			}
			b.WriteString(formatSingleNews(n, count))
			count++
		}
	}

	// Then important Denmark news
	if len(denmarkNews) > 0 {
		if len(ukraineNews) > 0 {
			b.WriteString("\n🇩🇰 <b>ВАЖЛИВІ НОВИНИ ДАНІЇ</b>\n\n")
		}
		for _, n := range denmarkNews {
			if count > max {
				break
			}
			b.WriteString(formatSingleNews(n, count))
			count++
		}
	}

	b.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	b.WriteString("📱 Danish News Bot | Щодня о 8:00 UTC")

	return b.String()
}

// formatSingleNews now uses AI summaries instead of full translations
func formatSingleNews(n news.News, number int) string {
	var b strings.Builder

	// Set emoji by category
	emoji := "📰"
	if n.Category == "ukraine" {
		emoji = "🔥"
	}

	// Title with link
	b.WriteString(fmt.Sprintf("%s <b>%d.</b> <a href=\"%s\">%s</a>\n", emoji, number, n.Link, n.Title))

	// Ukrainian summary (primary)
	if n.SummaryUkrainian != "" {
		b.WriteString(fmt.Sprintf("🇺🇦 <i>%s</i>\n", limitText(n.SummaryUkrainian, 1500)))
	}

	// Danish summary (secondary)
	if n.SummaryDanish != "" {
		b.WriteString(fmt.Sprintf("🇩🇰 %s\n", limitText(n.SummaryDanish, 1500)))
	}

	b.WriteString("➖➖➖➖➖➖➖➖➖➖\n\n")

	return b.String()
}

func limitText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndex(cut, " "); i > 400 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "..."
}

// Run запускает основной процесс приложения с инициализацией Gemini
func Run() {
	// Initialize structured logging
	logger.Init()
	logger.Info("Starting Danish News Bot")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Error("Failed to load configuration", "error", err)
		log.Fatalf("Ошибка конфигурации: %v", err)
	}
	logger.Info("Configuration loaded successfully", "mode", cfg.BotMode, "max_news", cfg.MaxNewsLimit)

	// Initialize Gemini client
	gmClient, err := gemini.NewClient(cfg.GeminiAPIKey)
	if err != nil {
		logger.Error("Failed to initialize Gemini client", "error", err)
		log.Fatalf("Ошибка инициализации Gemini: %v", err)
	}
	defer gmClient.Close()
	news.SetGeminiClient(gmClient)
	logger.Info("Gemini client initialized successfully")

	// Load RSS feeds
	feeds, err := rss.LoadFeeds(cfg.FeedsConfigPath)
	if err != nil {
		logger.Error("Failed to load RSS feeds", "error", err)
		log.Fatalf("Ошибка загрузки списка RSS: %v", err)
	}
	logger.Info("RSS feeds loaded", "count", len(feeds))

	// Fetch news items
	items, err := rss.FetchAllFeeds(feeds)
	if err != nil {
		logger.Error("Failed to fetch RSS feeds", "error", err)
		log.Fatalf("Ошибка парсинга RSS: %v", err)
	}
	logger.Info("News items fetched", "total", len(items))

	// Filter and translate news
	filtered, err := news.FilterAndTranslate(items)
	if err != nil {
		logger.Error("Failed to filter and translate news", "error", err)
		log.Fatalf("Ошибка фильтрации/обработки: %v", err)
	}
	logger.Info("News filtered and translated", "relevant", len(filtered))

	// Show preview in console
	for i, n := range filtered {
		if i >= 2 {
			break
		}
		fmt.Println("---")
		fmt.Println(news.FormatNews(n))
	}

	if len(filtered) == 0 {
		logger.Warn("No relevant news found, skipping Telegram send")
		return
	}

	// Send to Telegram based on mode
	if cfg.BotMode == "single" {
		sendSingleNews(filtered, cfg.TelegramToken, cfg.TelegramChatID)
	} else {
		sendMultipleNews(filtered, cfg.TelegramToken, cfg.TelegramChatID)
	}

	// Log final metrics
	stats := metrics.Global.GetStats()
	logger.Info("Processing completed",
		"total_processed", stats["total_news_processed"],
		"successful_translations", stats["successful_translations"],
		"duplicates_filtered", stats["duplicates_filtered"],
		"processing_time_ms", stats["last_processing_time_ms"],
	)
}

// sendSingleNews отправляет одну новость
func sendSingleNews(newsList []news.News, token, chatID string) {
	if len(newsList) == 0 {
		logger.Warn("No news to send")
		return
	}

	selectedNews := newsList[0]
	msg := formatSingleNewsMessage(selectedNews, 1)

	logger.Info("Sending single news", "length", len(msg), "title", selectedNews.Title)

	err := telegram.SendMessage(token, chatID, msg)
	if err != nil {
		logger.Error("Failed to send Telegram message", "error", err)
		log.Fatalf("Ошибка отправки в Telegram: %v", err)
	}

	metrics.Global.IncrementTelegramMessagesSent()
	logger.Info("Single news sent successfully", "title", selectedNews.Title)
}

// sendMultipleNews отправляет несколько новостей одним сообщением
func sendMultipleNews(newsList []news.News, token, chatID string) {
	msg := formatNewsMessage(newsList, 2)

	// Check Telegram message length limit
	if len(msg) > 4000 {
		logger.Warn("Message too long, reducing to 1 news", "original_length", len(msg))
		msg = formatNewsMessage(newsList, 1)
	}

	logger.Info("Sending multiple news", "length", len(msg))

	err := telegram.SendMessage(token, chatID, msg)
	if err != nil {
		logger.Error("Failed to send Telegram message", "error", err)
		log.Fatalf("Ошибка отправки в Telegram: %v", err)
	}

	metrics.Global.IncrementTelegramMessagesSent()
	logger.Info("Multiple news sent successfully")
}

// formatSingleNewsMessage адаптирован для саммари
func formatSingleNewsMessage(n news.News, number int) string {
	var b strings.Builder

	// Красивый заголовок
	b.WriteString("🇩🇰 <b>Danish News</b> 🇺🇦\n")
	b.WriteString("━━━━━━━━━━━━━━━\n\n")

	// Определяем категорию и эмодзи
	emoji := "📰"
	categoryText := "🇩🇰 <b>НОВИНИ ДАНІЇ - Стисло!</b>"

	if n.Category == "ukraine" {
		emoji = "🔥"
		categoryText = "🇺🇦 <b>УКРАЇНА В ДАНІЇ</b>"
	}

	b.WriteString(categoryText + "\n\n")

	// Заголовок новости с ссылкой
	b.WriteString(fmt.Sprintf("%s <a href=\"%s\">%s</a>\n\n", emoji, n.Link, n.Title))

	if n.SummaryUkrainian != "" {
		b.WriteString("🇺🇦 <i>" + limitText(n.SummaryUkrainian, 1000) + "</i>\n\n")
	}
	if n.SummaryDanish != "" {
		b.WriteString("🇩🇰 " + limitText(n.SummaryDanish, 1000) + "\n\n")
	}

	b.WriteString("━━━━━━━━━━━━━━━\n")
	b.WriteString("📱 <i>Danish News Bot - DeusFlow</i>")

	return b.String()
}

// cleanAndLimitContent kept for original snippet extraction
func cleanAndLimitContent(content string, isOriginal bool) string {
	// Strip HTML tags first
	content = stripHTML(content)
	// Decode HTML entities
	content = html.UnescapeString(content)
	// Collapse whitespace
	content = strings.ReplaceAll(content, "\r", "")
	content = strings.ReplaceAll(content, "\t", " ")
	content = strings.ReplaceAll(content, "\n", " ")
	content = strings.Join(strings.Fields(content), " ")

	// Trim
	content = strings.TrimSpace(content)

	// Split into sentences
	sentences := strings.Split(content, ".")
	var cleanSentences []string
	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		if len(sentence) < 15 {
			continue
		}
		if isOriginal && isIrrelevantSentence(sentence) {
			continue
		}
		cleanSentences = append(cleanSentences, sentence)
		if len(cleanSentences) >= 3 {
			break
		}
	}
	result := strings.Join(cleanSentences, ". ")
	if result != "" && !strings.HasSuffix(result, ".") {
		result += "."
	}
	if len(result) > 500 {
		result = result[:500] + "..."
	}
	return result
}

var htmlTagRe = regexp.MustCompile(`<[^>]+>`) // simple tag stripper

func stripHTML(s string) string {
	if s == "" {
		return s
	}
	return htmlTagRe.ReplaceAllString(s, "")
}

// isIrrelevantSentence reused for filtering original content noise
func isIrrelevantSentence(sentence string) bool {
	lower := strings.ToLower(sentence)

	// Фразы, указывающие на другие статьи или нерелевантный контент
	irrelevant := []string{"læs også", "se også", "følg med på", "dr nyheder har"}
	for _, ph := range irrelevant {
		if strings.Contains(lower, ph) {
			return true
		}
	}

	return false
}
