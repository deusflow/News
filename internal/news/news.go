package news

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/gemini"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/scraper"
	"github.com/deusflow/News/internal/translate"
)

// News - основная структура новости
type News struct {
	Title     string
	Content   string
	Link      string
	Published time.Time
	Category  string
	Score     int

	SourceName       string
	SourceLang       string
	SourceCategories []string

	Summary          string
	SummaryDanish    string
	SummaryUkrainian string
	TitleUkrainian   string

	ImageURL string
	ImageAlt string

	ImportanceDanish    string
	ImportanceUkrainian string

	Mood    string   // positive, negative, neutral
	Tags    []string // ["Політика", "Данія"]
	TLDR    string   // Одно предложение - суть новости
	FunFact string   // Цікавий факт про Данію (генерується AI)
}

type Options struct {
	Limit             int
	MaxAge            time.Duration
	PerSource         int
	MaxGeminiRequests int
	ScrapeMaxArticles int
	ScrapeConcurrency int
	PerCategory       int
	Keywords          *config.KeywordsConfig
	AIClient          *gemini.Client
	Metrics           *metrics.Metrics
}

func FilterAndTranslateWithOptions(ctx context.Context, items []*rss.FeedItem, opts Options) ([]News, error) {
	if opts.AIClient == nil {
		return nil, fmt.Errorf("gemini client not initialized")
	}

	var candidates []News
	seenLinks := make(map[string]bool)

	// 1. Фильтрация и Оценка
	for _, item := range items {
		pubDate := time.Now()
		if item.PublishedParsed != nil {
			pubDate = *item.PublishedParsed
		}
		if time.Since(pubDate) > opts.MaxAge {
			continue
		}
		if seenLinks[item.Link] {
			continue
		}

		score, cat := calculateNewsScore(item, opts.Keywords)
		if score < 10 {
			continue
		}

		n := News{
			Title:     strings.TrimSpace(item.Title),
			Content:   cleanContent(item.Description),
			Link:      item.Link,
			Published: pubDate,
			Category:  cat,
			Score:     score,
			ImageURL:  extractImageURL(item),
			ImageAlt:  item.Title,
		}

		if item.Source != nil {
			n.SourceName = item.Source.Name
			n.SourceLang = item.Source.Lang
			n.SourceCategories = item.Source.Categories
		}

		seenLinks[item.Link] = true
		candidates = append(candidates, n)
	}

	// 2. Сортировка
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Published.After(candidates[j].Published)
	})

	// 3. Лимит
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// 4. Скрейпинг и AI
	var result []News
	urls := make([]string, len(candidates))
	for i, n := range candidates {
		urls[i] = n.Link
	}

	fullArticles := scraper.ExtractArticlesInBackgroundWithLimits(ctx, urls, opts.ScrapeMaxArticles, opts.ScrapeConcurrency)
	geminiReqs := 0

	for _, n := range candidates {
		// Check context cancellation
		if ctx.Err() != nil {
			return result, ctx.Err()
		}

		// Подставляем полный текст
		if fa, ok := fullArticles[n.Link]; ok && len(fa.Content) > 200 {
			n.Content = fa.Content
		}

		// Rate limiter в gemini.go вже контролює швидкість запитів (40 сек між запитами)
		// Додаткова пауза тут не потрібна

		if opts.MaxGeminiRequests > 0 && geminiReqs >= opts.MaxGeminiRequests {
			// Лимит исчерпан - используем Groq для перевода
			log.Printf("⚠️ Gemini limit reached, using Groq fallback for: %s", n.Title)

			// Используем нормальный SummarizeText вместо простой обрезки
			if sum, err := translate.SummarizeText(n.Content, "da"); err == nil && sum != "" {
				n.SummaryDanish = sum
			} else {
				n.SummaryDanish = fallbackSummary(n.Content)
			}

			// Переводим через строгий fallback
			if ukSummary, err := translate.StrictTranslateText(n.SummaryDanish, "da", "uk"); err == nil && ukSummary != "" {
				n.SummaryUkrainian = ukSummary
				log.Printf("✅ Groq STRICT fallback translation ok for: %s", n.Title)
			} else {
				log.Printf("❌ Groq STRICT fallback failed for %s: %v", n.Title, err)
				n.SummaryUkrainian = "⚠️ Переклад тимчасово недоступний."
			}
			if ukTitle, err := translate.StrictTranslateText(n.Title, "da", "uk"); err == nil && ukTitle != "" {
				n.TitleUkrainian = ukTitle
			} else {
				n.TitleUkrainian = n.Title
			}
			n.Mood = "neutral"
			n.FunFact = GetRandomFact()
		} else {
			// AI
			aiResp, err := opts.AIClient.TranslateAndSummarizeNews(ctx, n.Title, n.Content)
			if err != nil {
				log.Printf("❌ Gemini failed for %s: %v", n.Title, err)

				// Используем нормальный SummarizeText вместо простой обрезки (попытка через другие модели)
				if sum, err := translate.SummarizeText(n.Content, "da"); err == nil && sum != "" {
					n.SummaryDanish = sum
				} else {
					n.SummaryDanish = fallbackSummary(n.Content)
				}

				// Переводим summary через строгий fallback
				if ukSummary, err := translate.StrictTranslateText(n.SummaryDanish, "da", "uk"); err == nil && ukSummary != "" {
					n.SummaryUkrainian = ukSummary
					log.Printf("✅ Groq STRICT fallback translation ok: %s", n.Title)
				} else {
					log.Printf("❌ Groq STRICT fallback failed for %s: %v", n.Title, err)
					n.SummaryUkrainian = "⚠️ Переклад тимчасово недоступний."
				}
				trTitle, _ := translate.StrictTranslateText(n.Title, "da", "uk")
				n.TitleUkrainian = trTitle
				n.Mood = "neutral"
				n.TLDR = ""
				n.FunFact = GetRandomFact() // Fallback на статический факт
			} else {
				n.SummaryDanish = aiResp.Danish
				n.SummaryUkrainian = aiResp.Ukrainian
				n.Mood = aiResp.Mood
				n.Tags = aiResp.Tags
				n.TLDR = aiResp.TLDR
				n.FunFact = aiResp.FunFact

				// Використовуємо TitleUkrainian з відповіді Gemini
				if aiResp.TitleUkrainian != "" {
					n.TitleUkrainian = aiResp.TitleUkrainian
				} else {
					// Fallback: перекладаємо через Gemini (TranslateText тепер спочатку Gemini)
					if ukTitle, err := translate.TranslateText(n.Title, "da", "uk"); err == nil && ukTitle != "" {
						n.TitleUkrainian = ukTitle
					} else {
						n.TitleUkrainian = n.Title
					}
				}
				if opts.Metrics != nil {
					opts.Metrics.IncrementSuccessfulTranslations()
				}
			}
			geminiReqs++
		}
		result = append(result, n)
	}

	// 5. Пересортировка:
	// 1) Сначала новости о беженцах (наивысший приоритет)
	// 2) Потом позитивные новости
	// 3) Остальные по настроению
	sort.SliceStable(result, func(i, j int) bool {
		// Категории беженцев всегда наверху
		iRefugee := isRefugeeCategory(result[i].Category)
		jRefugee := isRefugeeCategory(result[j].Category)
		if iRefugee != jRefugee {
			return iRefugee // refugee идут первыми
		}
		// Внутри остальных - по настроению
		return getMoodScore(result[i].Mood) > getMoodScore(result[j].Mood)
	})

	return result, nil
}

// isRefugeeCategory проверяет относится ли категория к беженцам
func isRefugeeCategory(cat string) bool {
	return cat == "refugee" || cat == "refugee_ukraine"
}

func getMoodScore(mood string) int {
	switch strings.ToLower(mood) {
	case "positive":
		return 10
	case "urgent":
		return 8 // Важные тоже высоко
	case "shocking":
		return 6
	case "neutral":
		return 4
	case "negative":
		return 1
	default:
		return 0
	}
}

// ====================================================================================
// НОВАЯ ЛОГИКА ФОРМАТИРОВАНИЯ (SMART LEAD)
// ====================================================================================

func GetMoodEmoji(mood string) string {
	switch strings.ToLower(mood) {
	case "positive":
		return "🟢"
	case "negative":
		return "🔴"
	case "shocking":
		return "⚡"
	case "urgent":
		return "🚨"
	default:
		return "🔵"
	}
}

func formatHeader(n News) string {
	moodEmoji := GetMoodEmoji(n.Mood)
	cat := strings.ToUpper(n.Category)

	// Специальные заголовки для категорий
	switch n.Category {
	case "refugee_ukraine":
		return "🇺🇦 <b>ВАЖЛИВО ДЛЯ УКРАЇНЦІВ</b>"
	case "refugee":
		return "📋 <b>ДЛЯ БІЖЕНЦІВ</b>"
	case "ukraine":
		cat = "UKRAINE"
	case "viborg":
		cat = "VIBORG"
	case "denmark":
		cat = "DANMARK"
	default:
		if cat == "" {
			cat = "NYHED"
		}
	}

	return fmt.Sprintf("%s <b>%s</b>", moodEmoji, cat)
}

// removeTitleFromSummary removes the title if it appears at the beginning of the summary
// This prevents duplication like: "Title. Title. Rest of text..." or "Title: Title. Text..."
func removeTitleFromSummary(summary, title string) string {
	if summary == "" || title == "" {
		return summary
	}

	summary = strings.TrimSpace(summary)
	title = strings.TrimSpace(title)

	// Normalize title (remove trailing punctuation for comparison)
	normalizedTitle := strings.TrimRight(title, ".!?:;,-–—")
	normalizedTitle = strings.ToLower(normalizedTitle)

	// Also try shorter version (first 40 chars) for partial matches
	shortTitle := normalizedTitle
	if len(shortTitle) > 40 {
		shortTitle = shortTitle[:40]
	}

	summaryLower := strings.ToLower(summary)

	// Strategy 1: Summary starts with full title
	if strings.HasPrefix(summaryLower, normalizedTitle) {
		titleLen := len(normalizedTitle)
		if titleLen < len(summary) {
			rest := summary[titleLen:]
			rest = strings.TrimLeft(rest, ".!?:;,:-–— \n\t")
			if rest != "" {
				// Check if rest ALSO starts with title (double duplication)
				return removeTitleFromSummary(rest, title)
			}
		}
	}

	// Strategy 2: Summary starts with short title (partial match)
	if len(shortTitle) >= 20 && strings.HasPrefix(summaryLower, shortTitle) {
		// Find the end of the duplicated part (look for sentence end)
		for i := len(shortTitle); i < len(summary) && i < len(shortTitle)+50; i++ {
			if summary[i] == '.' || summary[i] == '!' || summary[i] == '?' {
				rest := strings.TrimLeft(summary[i+1:], " \n\t")
				if rest != "" {
					return removeTitleFromSummary(rest, title)
				}
				break
			}
		}
	}

	// Strategy 3: Look for "Title. Title." pattern anywhere in first 200 chars
	// This catches cases where AI writes "Title. Title. Rest..."
	if len(summary) > 50 {
		checkPart := summaryLower
		if len(checkPart) > 200 {
			checkPart = checkPart[:200]
		}

		// Find if title appears twice
		firstIdx := strings.Index(checkPart, shortTitle[:min(20, len(shortTitle))])
		if firstIdx >= 0 && firstIdx < 5 { // Title at the start
			secondIdx := strings.Index(checkPart[firstIdx+10:], shortTitle[:min(20, len(shortTitle))])
			if secondIdx >= 0 && secondIdx < 100 {
				// Found double title, cut from second occurrence
				cutPoint := firstIdx + 10 + secondIdx
				for i := cutPoint; i < len(summary) && i < cutPoint+50; i++ {
					if summary[i] == '.' || summary[i] == '!' || summary[i] == '?' {
						rest := strings.TrimLeft(summary[i+1:], " \n\t")
						if rest != "" {
							return rest
						}
						break
					}
				}
			}
		}
	}

	return summary
}

// min returns the smaller of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func formatSmartBlock(flag, title, importance, summary string) string {
	var sb strings.Builder
	t := strings.TrimSpace(title)
	// Добавляем точку для слияния
	if t != "" && !strings.HasSuffix(t, ".") && !strings.HasSuffix(t, "!") && !strings.HasSuffix(t, "?") {
		t += "."
	}
	sb.WriteString(fmt.Sprintf("%s <b>%s</b>", flag, t))

	if importance != "" {
		sb.WriteString(fmt.Sprintf(" 🔥 <i>%s</i>", strings.TrimSpace(importance)))
	}
	if summary != "" {
		// Remove title from summary if it's duplicated at the beginning
		cleanSummary := removeTitleFromSummary(summary, title)
		if cleanSummary != "" {
			sb.WriteString(" " + strings.TrimSpace(cleanSummary))
		}
	}
	return sb.String()
}

// FormatNewsWithImage - ШИРОКИЙ режим (800 знаков)
func FormatNewsWithImage(n News, _, _ int) string {
	daSum := limitText(n.SummaryDanish, 800)
	if daSum == "" {
		daSum = limitText(n.Content, 800)
	}

	ukSum := limitText(n.SummaryUkrainian, 800)
	if ukSum == "" {
		// Если украинского перевода нет - пробуем перевести датский summary СТРОГО (без переписывания)
		if daSum != "" {
			if translated, err := translate.StrictTranslateText(daSum, "da", "uk"); err == nil && translated != "" {
				ukSum = limitText(translated, 800)
				log.Printf("✅ FormatNewsWithImage: STRICT fallback translation ok")
			} else {
				log.Printf("⚠️ FormatNewsWithImage: Переклад недоступний для: %s", n.Title)
				ukSum = "⚠️ Переклад тимчасово недоступний. Див. оригінал вище."
			}
		} else {
			ukSum = "⚠️ Переклад недоступний."
		}
	}

	ukTitle := n.TitleUkrainian
	if ukTitle == "" {
		// Пробуем перевести заголовок (строго)
		if translated, err := translate.StrictTranslateText(n.Title, "da", "uk"); err == nil && translated != "" {
			ukTitle = translated
		} else {
			ukTitle = n.Title
		}
	}

	var b strings.Builder
	b.WriteString(formatHeader(n) + "\n")

	// TL;DR - обов'язковий короткий акцент (💬 + emoji)
	tldr := strings.TrimSpace(n.TLDR)
	if tldr != "" {
		// гарантируем, что TL;DR начинается с emoji/символа (хотя бы)
		b.WriteString("💬 <b>" + tldr + "</b>\n")
	}
	b.WriteString("\n")

	b.WriteString(formatSmartBlock("🇩🇰", n.Title, n.ImportanceDanish, daSum))
	b.WriteString("\n\n")
	b.WriteString(formatSmartBlock("🇺🇦", ukTitle, n.ImportanceUkrainian, ukSum))
	b.WriteString("\n\n")

	if n.Link != "" {
		b.WriteString("🔗 <a href=\"" + n.Link + "\">Læs mere / Читати оригінал</a>\n")
	}
	if len(n.Tags) > 0 {
		tags := make([]string, 0, len(n.Tags))
		for _, t := range n.Tags {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			tags = append(tags, "#"+strings.ReplaceAll(t, " ", "_"))
		}
		if len(tags) > 0 {
			b.WriteString("<i>" + strings.Join(tags, " ") + "</i>\n")
		}
	}

	// separator + fun fact (always about Denmark/Kingdom)
	fact := strings.TrimSpace(n.FunFact)
	if fact == "" {
		fact = GetRandomFact()
	}
	b.WriteString("\n━━━━━━━━━━━━━━━\n")
	b.WriteString("💡 <i>" + fact + "</i>")

	return b.String()
}

// FormatCaptionForPhoto - Режим фото (1024 знака)
func FormatCaptionForPhoto(n News, maxLen int, _, _ int) string {
	if maxLen > 1024 {
		maxLen = 1024
	}

	ukTitle := n.TitleUkrainian
	if ukTitle == "" {
		// Пробуем перевести заголовок строго
		if translated, err := translate.StrictTranslateText(n.Title, "da", "uk"); err == nil && translated != "" {
			ukTitle = translated
		} else {
			ukTitle = n.Title
		}
	}

	// Проверяем есть ли украинский summary, если нет - переводим строго
	ukSummary := n.SummaryUkrainian
	if ukSummary == "" && n.SummaryDanish != "" {
		if translated, err := translate.StrictTranslateText(n.SummaryDanish, "da", "uk"); err == nil && translated != "" {
			ukSummary = translated
			log.Printf("✅ FormatCaptionForPhoto: STRICT fallback translation ok")
		}
	}
	// Сохраняем перевод обратно в структуру для использования ниже
	if ukSummary != "" {
		n.SummaryUkrainian = ukSummary
	}

	// Clean summaries from title duplication (AI should not include title, but just in case)
	cleanDanishSummary := removeTitleFromSummary(n.SummaryDanish, n.Title)
	cleanUkrainianSummary := removeTitleFromSummary(n.SummaryUkrainian, ukTitle)

	header := formatHeader(n) + "\n"

	// TL;DR для швидкого читання
	tldrBlock := ""
	if n.TLDR != "" {
		tldrBlock = "💬 <b>" + n.TLDR + "</b>\n"
	}
	header += tldrBlock + "\n"

	footer := ""

	if len(n.Tags) > 0 {
		tags := make([]string, 0, len(n.Tags))
		for _, t := range n.Tags {
			tags = append(tags, "#"+strings.ReplaceAll(t, " ", "_"))
		}
		tagStr := "\n" + strings.Join(tags, " ")
		if len(tagStr) < 150 {
			footer = tagStr
		}
	}

	// Prepare FunFact
	fact := strings.TrimSpace(n.FunFact)
	if fact == "" {
		fact = GetRandomFact()
	}
	funFactSection := "\n━━━━━━━━━━━━━━━\n💡 <i>" + fact + "</i>"

	// Build the message - AI already outputs correctly sized content
	// Trimming is just a safety fallback
	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString(formatSmartBlock("🇩🇰", n.Title, n.ImportanceDanish, cleanDanishSummary))
	sb.WriteString("\n\n")
	sb.WriteString(formatSmartBlock("🇺🇦", ukTitle, n.ImportanceUkrainian, cleanUkrainianSummary))
	sb.WriteString(footer)
	sb.WriteString(funFactSection)

	result := sb.String()

	// Safety fallback: trim if AI exceeded limits (shouldn't happen often)
	if utf8.RuneCountInString(result) > maxLen {
		log.Printf("⚠️ FormatCaptionForPhoto: content exceeded %d chars, trimming", maxLen)
		return trimToRuneCount(result, maxLen-1) + "…"
	}
	return result
}

func ShouldUsePhoto(n News, maxLen int, sentencesPerLang int, minPerLang int, _ int) bool {
	caption := FormatCaptionForPhoto(n, maxLen, sentencesPerLang, minPerLang)
	return utf8.RuneCountInString(caption) < maxLen && utf8.RuneCountInString(caption) > 100
}

// ====================================================================================
// ВСПОМОГАТЕЛЬНЫЕ ФУНКЦИИ
// ====================================================================================

func calculateNewsScore(item *rss.FeedItem, kw *config.KeywordsConfig) (int, string) {
	text := strings.ToLower(item.Title + " " + item.Description)

	if kw == nil {
		// Fallback if no keywords provided (should not happen)
		return 40, "denmark"
	}

	// Виключаємо непотрібні новини
	if containsAny(text, kw.Exclude) {
		return 0, ""
	}

	// ========================================
	// НАЙВИЩИЙ ПРІОРИТЕТ: Українці в Данії (віза SL1, робота, інтеграція)
	// ========================================
	if containsAny(text, kw.UkrainiansInDenmark) {
		return 250, "refugee_ukraine" // Максимальний пріоритет!
	}

	// Новини про біженців з ПРЯМИМ зв'язком з Україною
	if isRelevantForRefugees(text) {
		if isSpecificForUkrainians(text) {
			return 200, "refugee_ukraine"
		}
		return 150, "refugee"
	}

	// ========================================
	// ВИСОКИЙ ПРІОРИТЕТ: Життя в Данії (діти, сім'я, освіта)
	// ========================================
	if containsAny(text, kw.FamilyLife) {
		return 140, "denmark" // Високий пріоритет для сімейних новин
	}

	// ========================================
	// ВИСОКИЙ ПРІОРИТЕТ: Позитивні новини про Данію
	// ========================================
	if containsAny(text, kw.PositiveDenmark) {
		return 120, "denmark" // Культура, спорт, свята
	}

	// Віборг та регіон (для локальних новин)
	if containsAny(text, kw.Viborg) {
		return 100, "viborg"
	}

	// Економіка та робота
	if containsAny(text, kw.Economy) {
		return 90, "denmark"
	}

	// Будівництво та інфраструктура
	if containsAny(text, kw.Construction) {
		return 80, "denmark"
	}

	// Дозвілля (нижчий пріоритет, ніж раніше)
	if containsAny(text, kw.Leisure) {
		return 70, "denmark"
	}

	// ========================================
	// ЗНИЖЕНИЙ ПРІОРИТЕТ: Війна в Україні (загальні новини)
	// ========================================
	// Якщо новина тільки про війну, але НЕ про українців в Данії - нижчий пріоритет
	if containsAny(text, kw.UkraineWar) {
		return 60, "ukraine" // Знижено з 100 до 60
	}

	return 40, "denmark"
}

func containsAny(text string, keywords []string) bool {
	for _, k := range keywords {
		if strings.Contains(text, k) {
			return true
		}
	}
	return false
}

// isRelevantForRefugees - СТРОГАЯ проверка релевантности для беженцев
// Новость должна содержать ПРЯМЫЕ указатели на беженцев/мигрантов в Дании
// или комбинацию контекстных слов (тема + Дания)
func isRelevantForRefugees(text string) bool {
	// Прямые индикаторы - одного слова достаточно
	directKeywords := []string{
		"flygtning", "asyl", "opholdstilladelse", "hjemsendelse",
		"udlænding", "udlændinge", "indvandrer",
		"midlertidig beskyttelse", "særlov", "ukrainerloven",
		"familiesammenføring", "asylcenter", "flygtningecenter",
		"integrationsydelse", "hjemrejseydelse", "selvforsørgelses",
		"statsborgerskab", "opholdslov",
	}

	for _, k := range directKeywords {
		if strings.Contains(text, k) {
			return true
		}
	}

	// Контекстные слова требуют связи с Данией или беженцами
	contextKeywords := []string{
		"ydelse", "kontanthjælp", "boligstøtte", "boligsikring",
		"opholdskort", "visum", "cpr", "nemid", "mitid",
	}

	danishContext := []string{
		"danmark", "dansk", "kommune", "styrelse", "ministeriet",
	}

	refugeeContext := []string{
		"flygtning", "asyl", "udlænding", "migrant",
	}

	for _, k := range contextKeywords {
		if strings.Contains(text, k) {
			// Проверяем есть ли датский контекст ИЛИ контекст беженцев
			if containsAny(text, danishContext) || containsAny(text, refugeeContext) {
				return true
			}
		}
	}

	return false
}

// isSpecificForUkrainians - проверяет что новость СПЕЦИФИЧНО для українців
// Только закони, статусы и пособия именно для украинских беженцев
func isSpecificForUkrainians(text string) bool {
	// Прямые указатели на украинский закон/статус
	ukrainianSpecificKeywords := []string{
		"ukrainerloven",        // закон об украинцах
		"ukraine-loven",        // альтернативное написание
		"ukrainske flygtninge", // украинские беженцы
		"ukrainere i danmark",  // украинцы в Дании
		"ukrainsk flygtning",   // украинский беженец
	}

	for _, k := range ukrainianSpecificKeywords {
		if strings.Contains(text, k) {
			return true
		}
	}

	// Комбинация: специфичные термины + упоминание Украины
	ukraineIndicators := []string{"ukrain", "україн"}
	refugeeStatusTerms := []string{
		"midlertidig beskyttelse", // временная защита
		"særlov",                  // специальный закон
		"opholdstilladelse",       // разрешение на проживание
		"forlængelse",             // продление
		"permanent ophold",        // постоянное проживание
		"ydelse",                  // пособие
		"integrationsydelse",      // пособие по интеграции
	}

	hasUkraine := containsAny(text, ukraineIndicators)
	hasRefugeeStatus := containsAny(text, refugeeStatusTerms)

	return hasUkraine && hasRefugeeStatus
}

func trimToRuneCount(s string, limit int) string {
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit])
	}
	return s
}

func trimToNearestSentenceOrWord(s string, limit int) string {
	s = strings.TrimSpace(s)
	if s == "" || limit <= 0 {
		return ""
	}

	// Convert to runes for proper Unicode handling (æ, ø, å, emojis)
	runes := []rune(s)
	runeLen := len(runes)

	// If text fits within limit, return as-is
	if runeLen <= limit {
		return s
	}

	// Safety check: ensure limit doesn't exceed rune length
	if limit > runeLen {
		limit = runeLen
	}

	// Create a cut version (in runes, not bytes)
	cutRunes := runes[:limit]
	cut := string(cutRunes)

	// Try to find last sentence boundary (.) in the cut portion
	if idx := strings.LastIndex(cut, ". "); idx > limit/2 {
		// Convert byte index to rune index
		runeIdx := utf8.RuneCountInString(cut[:idx])
		if runeIdx > 0 && runeIdx < limit {
			return string(runes[:runeIdx+1]) // Include the dot
		}
	}

	// Try to find last period at end
	if idx := strings.LastIndex(cut, "."); idx > limit/2 {
		runeIdx := utf8.RuneCountInString(cut[:idx])
		if runeIdx > 0 && runeIdx < limit {
			return string(runes[:runeIdx+1])
		}
	}

	// Try to find last space for word boundary
	if idx := strings.LastIndex(cut, " "); idx > limit/3 {
		runeIdx := utf8.RuneCountInString(cut[:idx])
		if runeIdx > 0 && runeIdx < limit {
			return string(runes[:runeIdx]) + "..."
		}
	}

	// Fallback: just cut at limit (safely)
	safeLimit := limit - 1
	if safeLimit < 1 {
		safeLimit = 1
	}
	if safeLimit > runeLen {
		safeLimit = runeLen
	}
	return string(runes[:safeLimit]) + "..."
}

func limitText(s string, max int) string {
	return trimToNearestSentenceOrWord(s, max)
}

func cleanContent(s string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	s = re.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func fallbackSummary(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}

	runes := []rune(content)
	if len(runes) > 300 {
		// Try to cut at sentence boundary
		cut := string(runes[:300])
		if idx := strings.LastIndex(cut, ". "); idx > 150 {
			return string(runes[:utf8.RuneCountInString(cut[:idx+1])])
		}
		// Try to cut at word boundary
		if idx := strings.LastIndex(cut, " "); idx > 200 {
			return string(runes[:utf8.RuneCountInString(cut[:idx])]) + "..."
		}
		return string(runes[:299]) + "..."
	}
	return content
}

func extractImageURL(item *rss.FeedItem) string {
	// 1. Try RSS enclosures first
	if item.Enclosures != nil {
		for _, e := range item.Enclosures {
			if e != nil && strings.HasPrefix(e.Type, "image/") {
				return e.URL
			}
		}
	}

	// 2. Try to find <img> in description
	if strings.Contains(item.Description, "<img") {
		start := strings.Index(item.Description, "src=\"")
		if start != -1 {
			start += 5
			end := strings.Index(item.Description[start:], "\"")
			if end != -1 {
				return item.Description[start : start+end]
			}
		}
	}

	// 3. Fallback: try to extract og:image from the article page
	if item.Link != "" {
		if imgURL, err := scraper.ExtractImageURL(item.Link); err == nil && imgURL != "" {
			return imgURL
		}
	}

	return ""
}
