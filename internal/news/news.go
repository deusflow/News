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

	Mood string   // positive, negative, neutral
	Tags []string // ["Політика", "Данія"]
}

type Options struct {
	Limit                int
	MaxAge               time.Duration
	PerSource            int
	MaxGeminiRequests    int
	ScrapeMaxArticles    int
	ScrapeConcurrency    int
	EnableImportanceLine bool
	PerCategory          int
	Keywords             *config.KeywordsConfig
	AIClient             *gemini.Client
	Metrics              *metrics.Metrics
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

		// === Пауза 60 секунд между запросами к Gemini ===
		// Gemini Free Tier: 20 запросов/минуту, но с запасом ставим 1 запрос в минуту
		// чтобы гарантированно избежать "Quota Exceeded"
		if geminiReqs > 0 { // Не ждем перед первой новостью
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-time.After(60 * time.Second):
			}
		}

		if opts.MaxGeminiRequests > 0 && geminiReqs >= opts.MaxGeminiRequests {
			// Лимит исчерпан
			n.SummaryDanish = fallbackSummary(n.Content)
			n.SummaryUkrainian = fallbackSummary(n.Content)
			n.Mood = "neutral"
		} else {
			// AI
			aiResp, err := opts.AIClient.TranslateAndSummarizeNews(ctx, n.Title, n.Content)
			if err != nil {
				log.Printf("Gemini failed for %s: %v", n.Title, err)
				n.SummaryDanish = fallbackSummary(n.Content)
				trTitle, _ := translate.TranslateText(n.Title, "da", "uk")
				n.TitleUkrainian = trTitle
				n.Mood = "neutral"
			} else {
				n.SummaryDanish = aiResp.Danish
				n.SummaryUkrainian = aiResp.Ukrainian
				n.Mood = aiResp.Mood
				n.Tags = aiResp.Tags

				if ukTitle, err := translate.TranslateText(n.Title, "da", "uk"); err == nil {
					n.TitleUkrainian = ukTitle
				} else {
					n.TitleUkrainian = n.Title
				}
				if opts.Metrics != nil {
					opts.Metrics.IncrementSuccessfulTranslations()
				}
			}
			geminiReqs++
		}
		result = append(result, n)
	}

	// 5. Пересортировка: Поднимаем позитивные новости наверх
	sort.SliceStable(result, func(i, j int) bool {
		return getMoodScore(result[i].Mood) > getMoodScore(result[j].Mood)
	})

	return result, nil
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
	if cat == "" {
		cat = "NYHED"
	}
	if cat == "UKRAINE" {
		cat = "UKRAINE"
	}
	return fmt.Sprintf("%s <b>%s</b>", moodEmoji, cat)
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
		sb.WriteString(" " + strings.TrimSpace(summary))
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
		ukSum = limitText(n.Content, 800)
	}

	ukTitle := n.TitleUkrainian
	if ukTitle == "" {
		ukTitle = n.Title
	}

	var b strings.Builder
	b.WriteString(formatHeader(n) + "\n\n")
	b.WriteString(formatSmartBlock("🇩🇰", n.Title, n.ImportanceDanish, daSum))
	b.WriteString("\n\n")
	b.WriteString(formatSmartBlock("🇺🇦", ukTitle, n.ImportanceUkrainian, ukSum))
	b.WriteString("\n\n")

	if n.Link != "" {
		b.WriteString("🔗 <a href=\"" + n.Link + "\">Læs mere / Читати оригінал</a>\n")
	}
	if len(n.Tags) > 0 {
		tags := make([]string, len(n.Tags))
		for i, t := range n.Tags {
			tags[i] = "#" + strings.ReplaceAll(t, " ", "_")
		}
		b.WriteString("<i>" + strings.Join(tags, " ") + "</i>")
	}
	return b.String()
}

// FormatCaptionForPhoto - Режим фото (1024 знака)
func FormatCaptionForPhoto(n News, maxLen int, _, _ int) string {
	if maxLen > 1024 {
		maxLen = 1024
	}

	ukTitle := n.TitleUkrainian
	if ukTitle == "" {
		ukTitle = n.Title
	}

	header := formatHeader(n) + "\n\n"
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

	// Считаем бюджет
	dummyDanish := formatSmartBlock("🇩🇰", n.Title, n.ImportanceDanish, "")
	dummyUkr := formatSmartBlock("🇺🇦", ukTitle, n.ImportanceUkrainian, "")

	skeletonLen := utf8.RuneCountInString(header) +
		utf8.RuneCountInString(dummyDanish) +
		utf8.RuneCountInString(dummyUkr) +
		utf8.RuneCountInString(footer) + 4

	availableForContent := maxLen - skeletonLen
	if availableForContent < 50 {
		return trimToRuneCount(header+dummyDanish+"\n\n"+dummyUkr+footer, maxLen)
	}

	budgetPerLang := availableForContent / 2
	dCut := trimToNearestSentenceOrWord(n.SummaryDanish, budgetPerLang)
	uCut := trimToNearestSentenceOrWord(n.SummaryUkrainian, budgetPerLang)

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString(formatSmartBlock("🇩🇰", n.Title, n.ImportanceDanish, dCut))
	sb.WriteString("\n\n")
	sb.WriteString(formatSmartBlock("🇺🇦", ukTitle, n.ImportanceUkrainian, uCut))
	sb.WriteString(footer)

	result := sb.String()
	if utf8.RuneCountInString(result) > maxLen {
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

	if containsAny(text, kw.UkraineWar) || containsAny(text, kw.RefugeeBoost) {
		return 100, "ukraine"
	}
	if containsAny(text, kw.Viborg) {
		return 80, "viborg"
	}
	if containsAny(text, kw.Economy) {
		return 70, "denmark"
	}
	if containsAny(text, kw.Construction) {
		return 65, "denmark"
	}
	if containsAny(text, kw.Leisure) {
		return 60, "denmark"
	}
	if containsAny(text, kw.Exclude) {
		return 0, ""
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

func trimToRuneCount(s string, limit int) string {
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit])
	}
	return s
}

func trimToNearestSentenceOrWord(s string, limit int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	cut := string(r[:limit])
	if idx := strings.LastIndex(cut, "."); idx > limit/2 {
		return string(r[:idx+1])
	}
	if idx := strings.LastIndex(cut, " "); idx > limit/3 {
		return string(r[:idx]) + "..."
	}
	return string(r[:limit-1]) + "..."
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
	if len(content) > 300 {
		return content[:300] + "..."
	}
	return content
}

func extractImageURL(item *rss.FeedItem) string {
	if item.Enclosures != nil {
		for _, e := range item.Enclosures {
			if e != nil && strings.HasPrefix(e.Type, "image/") {
				return e.URL
			}
		}
	}
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
	return ""
}
