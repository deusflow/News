package app

import (
	"fmt"
	"html"
	"log"
	"regexp"
	"sort"
	"strings"

	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/gemini"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/storage"
	"github.com/deusflow/News/internal/telegram"
	"github.com/deusflow/News/internal/translate"
)

// formatNewsMessage builds grouped message using AI summaries (Ukrainian priority, then Danish, then others)
func formatNewsMessage(newsList []news.News, max int) string {
	var b strings.Builder

	b.WriteString("🇩🇰 <b>Новини Данії</b> 🇺🇦\n")
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	count := 1

	// Priority: Ukraine in Denmark
	b.WriteString("🇺🇦 <b>УКРАЇНА В ДАНІЇ</b>\n\n")
	for _, n := range newsList {
		if count > max {
			break
		}
		if n.Category == "ukraine" {
			b.WriteString(formatSingleNews(n, count))
			count++
		}
	}

	// Then important Denmark
	if count <= max {
		b.WriteString("\n🇩🇰 <b>ВАЖЛИВІ НОВИНИ ДАНІЇ</b>\n\n")
		for _, n := range newsList {
			if count > max {
				break
			}
			if n.Category == "denmark" {
				b.WriteString(formatSingleNews(n, count))
				count++
			}
		}
	}

	// Then everything else to increase diversity
	if count <= max {
		b.WriteString("\n🌍 <b>ІНШІ ВАЖЛИВІ НОВИНИ</b>\n\n")
		for _, n := range newsList {
			if count > max {
				break
			}
			if n.Category != "ukraine" && n.Category != "denmark" {
				b.WriteString(formatSingleNews(n, count))
				count++
			}
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
	logger.Info("Configuration loaded successfully", "mode", cfg.BotMode, "max_news", cfg.MaxNewsLimit, "use_postgres", cfg.UsePostgres)

	// Initialize cache system (PostgreSQL or File-based)
	var cacheAdapter CacheAdapter

	if cfg.UsePostgres && cfg.DatabaseURL != "" {
		// Use PostgreSQL for production-grade duplicate prevention
		pgCache, err := storage.NewPostgresCache(cfg.DatabaseURL, cfg.DatabaseTTL)
		if err != nil {
			logger.Error("Failed to connect to PostgreSQL, falling back to file cache", "error", err)
			// Fallback to file cache
			fileCache := storage.NewFileCache(cfg.CacheFilePath, cfg.CacheTTLHours)
			if err := fileCache.Load(); err != nil {
				logger.Error("Failed to load file cache", "error", err)
			}
			cacheAdapter = &FileCacheAdapter{cache: fileCache}
		} else {
			logger.Info("PostgreSQL cache initialized successfully")
			// Cleanup old records
			if err := pgCache.Cleanup(); err != nil {
				logger.Warn("Failed to cleanup old records", "error", err)
			}
			cacheAdapter = &PostgresCacheAdapter{cache: pgCache}
			defer pgCache.Close()
		}
	} else {
		// Use file-based cache
		logger.Info("Using file-based cache")
		newsCache := storage.NewFileCache(cfg.CacheFilePath, cfg.CacheTTLHours)
		if err := newsCache.Load(); err != nil {
			logger.Error("Failed to load news cache", "error", err)
		} else {
			logger.Info("News cache loaded successfully", "items", newsCache.GetStats()["total_items"])
		}
		cacheAdapter = &FileCacheAdapter{cache: newsCache}
		defer func() {
			if fc, ok := cacheAdapter.(*FileCacheAdapter); ok {
				if err := fc.cache.Save(); err != nil {
					logger.Error("Failed to save news cache", "error", err)
				}
			}
		}()
	}

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

	// Filter and translate news with options from config
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
		sendSingleNews(filtered, cfg, cacheAdapter)
	} else {
		sendMultipleNews(filtered, cfg, cacheAdapter, cfg.MaxNewsLimit)
	}

	// Vocabulary post after sending if enabled
	if cfg.EnableVocabPost && len(filtered) > 0 {
		postVocabulary(filtered, cfg)
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
func sendSingleNews(newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter) {
	if len(newsList) == 0 {
		logger.Warn("No news to send")
		return
	}

	// Find first non-duplicate news (double check: hash and link)
	var selectedNews *news.News
	for i := range newsList {
		hash := cacheAdapter.GenerateNewsHash(newsList[i].Title, newsList[i].Link)

		// Double check: both hash and direct link
		if !cacheAdapter.IsAlreadySent(hash) && !cacheAdapter.IsLinkAlreadySent(newsList[i].Link) {
			selectedNews = &newsList[i]
			break
		}
		logger.Info("Skipping duplicate news", "title", newsList[i].Title, "hash", hash)
	}

	if selectedNews == nil {
		logger.Warn("All news items are duplicates, nothing to send")
		return
	}

	// Build caption/message according to policy
	var outText string
	usePhoto := false
	policy := strings.ToLower(strings.TrimSpace(cfg.PostingPolicy))
	if policy == "" {
		policy = "hybrid"
	}
	canPhoto := strings.TrimSpace(selectedNews.ImageURL) != "" && news.ShouldUsePhoto(*selectedNews, cfg.PhotoCaptionMaxRunes, cfg.PhotoSentencesPerLang, cfg.PhotoMinPerLangRunes, cfg.MinSummaryTotalRunes)
	if (policy == "photo-only" && canPhoto) || (policy == "hybrid" && canPhoto) {
		usePhoto = true
		outText = news.FormatCaptionForPhoto(*selectedNews, cfg.PhotoCaptionMaxRunes, cfg.PhotoSentencesPerLang, cfg.PhotoMinPerLangRunes)
	} else {
		// text-only or hybrid fallback
		outText = news.FormatNewsWithImage(*selectedNews, cfg.TextSentencesPerLangMin, cfg.TextSentencesPerLangMax)
	}
	logger.Info("Sending single news", "length", len(outText), "title", selectedNews.Title, "photo", usePhoto)

	var err error
	if usePhoto {
		err = telegram.SendPhoto(cfg.TelegramToken, cfg.TelegramChatID, selectedNews.ImageURL, outText)
	} else {
		// Allow preview so Telegram can show link thumbnail
		err = telegram.SendMessageAllowPreview(cfg.TelegramToken, cfg.TelegramChatID, outText)
	}
	if err != nil {
		logger.Error("Failed to send Telegram message", "error", err)
		log.Fatalf("Ошибка отправки в Telegram: %v", err)
	}

	// Mark as sent
	hash := cacheAdapter.GenerateNewsHash(selectedNews.Title, selectedNews.Link)
	if err := cacheAdapter.MarkAsSent(hash, selectedNews.Title, selectedNews.Link, selectedNews.Category, selectedNews.SourceName); err != nil {
		logger.Error("Failed to mark news as sent", "error", err)
	}

	metrics.Global.IncrementTelegramMessagesSent()
	logger.Info("Single news sent successfully", "title", selectedNews.Title, "hash", hash)
}

// sendMultipleNews отправляет кілька новин, кожну окремим повідомленням (з фото, если есть)
func sendMultipleNews(newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter, maxToSend int) {
	// Filter out duplicates with double check (hash + link)
	var uniqueNews []news.News
	for _, n := range newsList {
		hash := cacheAdapter.GenerateNewsHash(n.Title, n.Link)

		// Double protection: check both hash and link
		if !cacheAdapter.IsAlreadySent(hash) && !cacheAdapter.IsLinkAlreadySent(n.Link) {
			uniqueNews = append(uniqueNews, n)
		} else {
			logger.Info("Skipping duplicate news", "title", n.Title, "hash", hash)
			metrics.Global.IncrementDuplicatesFiltered()
		}
	}

	if len(uniqueNews) == 0 {
		logger.Warn("All news items are duplicates, nothing to send")
		return
	}

	if maxToSend <= 0 {
		maxToSend = 5
	}
	if maxToSend > len(uniqueNews) {
		maxToSend = len(uniqueNews)
	}

	policy := strings.ToLower(strings.TrimSpace(cfg.PostingPolicy))
	if policy == "" {
		policy = "hybrid"
	}

	// Send each item separately using the new format
	sentCount := 0
	for i := 0; i < maxToSend; i++ {
		n := uniqueNews[i]

		// Triple check before sending (paranoid mode to prevent duplicates)
		hash := cacheAdapter.GenerateNewsHash(n.Title, n.Link)
		if cacheAdapter.IsAlreadySent(hash) || cacheAdapter.IsLinkAlreadySent(n.Link) {
			logger.Warn("News became duplicate during sending, skipping", "title", n.Title)
			continue
		}

		var outText string
		usePhoto := false
		canPhoto := strings.TrimSpace(n.ImageURL) != "" && news.ShouldUsePhoto(n, cfg.PhotoCaptionMaxRunes, cfg.PhotoSentencesPerLang, cfg.PhotoMinPerLangRunes, cfg.MinSummaryTotalRunes)
		if (policy == "photo-only" && canPhoto) || (policy == "hybrid" && canPhoto) {
			usePhoto = true
			outText = news.FormatCaptionForPhoto(n, cfg.PhotoCaptionMaxRunes, cfg.PhotoSentencesPerLang, cfg.PhotoMinPerLangRunes)
		} else {
			outText = news.FormatNewsWithImage(n, cfg.TextSentencesPerLangMin, cfg.TextSentencesPerLangMax)
		}

		var err error
		if usePhoto {
			err = telegram.SendPhoto(cfg.TelegramToken, cfg.TelegramChatID, n.ImageURL, outText)
		} else {
			// Allow preview so Telegram can show link thumbnail
			err = telegram.SendMessageAllowPreview(cfg.TelegramToken, cfg.TelegramChatID, outText)
		}
		if err != nil {
			logger.Error("Failed to send Telegram message", "error", err, "title", n.Title)
			continue // Don't fail completely, try next news
		}

		// Mark as sent immediately after successful send
		if err := cacheAdapter.MarkAsSent(hash, n.Title, n.Link, n.Category, n.SourceName); err != nil {
			logger.Error("Failed to mark news as sent", "error", err, "title", n.Title)
		} else {
			logger.Info("News marked as sent", "title", n.Title, "hash", hash)
		}

		metrics.Global.IncrementTelegramMessagesSent()
		sentCount++
	}

	logger.Info("Multiple news sent successfully", "count", sentCount, "requested", maxToSend)
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

// postVocabulary builds and sends vocabulary list from Danish news summaries
func postVocabulary(list []news.News, cfg *config.Config) {
	logger.Info("Generating vocabulary post")

	// Collect Danish text corpus
	var corpus []string
	for _, n := range list {
		if n.SummaryDanish != "" {
			corpus = append(corpus, n.SummaryDanish)
		}
		corpus = append(corpus, n.Title)
	}

	if len(corpus) == 0 {
		logger.Warn("No Danish text for vocabulary extraction")
		return
	}

	full := strings.ToLower(strings.Join(corpus, " "))

	// Tokenize
	tokens := regexp.MustCompile(`[^a-zA-ZæøåÆØÅ]+`).Split(full, -1)
	freq := map[string]int{}

	// Danish stop words
	stop := map[string]struct{}{
		"danmark": {}, "viborg": {}, "der": {}, "og": {}, "for": {},
		"med": {}, "ikke": {}, "har": {}, "fra": {}, "til": {},
		"mens": {}, "efter": {}, "den": {}, "det": {}, "som": {},
		"man": {}, "jeg": {}, "hun": {}, "han": {}, "vores": {},
		"hans": {}, "de": {}, "at": {}, "en": {}, "et": {},
		"på": {}, "i": {}, "af": {}, "er": {}, "blev": {},
	}

	for _, t := range tokens {
		if len(t) < 5 {
			continue
		}
		if _, skip := stop[t]; skip {
			continue
		}
		freq[t]++
	}

	if len(freq) == 0 {
		logger.Warn("No vocabulary words found after filtering")
		return
	}

	// Select top N by frequency
	type pair struct {
		w string
		c int
	}
	var arr []pair
	for w, c := range freq {
		arr = append(arr, pair{w, c})
	}
	sort.Slice(arr, func(i, j int) bool {
		return arr[i].c > arr[j].c
	})

	limit := cfg.VocabWordsPerDay
	if limit <= 0 {
		limit = 5
	}
	if len(arr) < limit {
		limit = len(arr)
	}
	selected := arr[:limit]

	// Build message with translations
	var b strings.Builder
	b.WriteString("🧠 <b>Слова дня (Danish → Ukrainian)</b>\n\n")

	idx := 1
	for _, p := range selected {
		// Translate word
		uk, err := translate.TranslateText(p.w, "da", "uk")
		if err != nil || uk == "" {
			uk = p.w // fallback to original if translation fails
		}

		// Find example sentence from news containing this word
		example := ""
		for _, n := range list {
			if strings.Contains(strings.ToLower(n.SummaryDanish), p.w) {
				// Extract first sentence as example
				sentences := strings.Split(n.SummaryDanish, ".")
				for _, s := range sentences {
					s = strings.TrimSpace(s)
					if len(s) > 20 && strings.Contains(strings.ToLower(s), p.w) {
						example = s + "."
						break
					}
				}
				break
			}
		}

		if example == "" {
			example = "Brug: " + p.w + "…"
		}

		// Limit example length
		if len(example) > 200 {
			example = example[:197] + "..."
		}

		b.WriteString(fmt.Sprintf("%d. <b>%s</b> → %s\n<i>%s</i>\n\n", idx, p.w, uk, example))
		idx++
	}

	b.WriteString("📌 Допомагає вивчати датську через реальні новини.")

	// Send vocabulary post
	_, err := telegram.SendMessageWithButtons(cfg.TelegramToken, cfg.TelegramChatID, b.String(), nil, true, 0)
	if err != nil {
		logger.Error("Failed to send vocabulary post", "error", err)
	} else {
		logger.Info("Vocabulary post sent successfully", "words_count", len(selected))
	}
}
