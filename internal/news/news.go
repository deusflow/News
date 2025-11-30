package news

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/gemini"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/scraper"
	"github.com/deusflow/News/internal/translate"
)

// News represents a single news item enriched by AI summaries with image support.
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

	// Image support
	ImageURL string
	ImageAlt string

	ImportanceDanish    string
	ImportanceUkrainian string

	Mood string   // positive, negative, neutral, etc.
	Tags []string // ["Політика", "Данія"]
}

// Global Gemini client
var aiClient *gemini.Client

func SetGeminiClient(c *gemini.Client) {
	aiClient = c
}

// ====================================================================================
// КЛЮЧЕВЫЕ СЛОВА ДЛЯ ФИЛЬТРАЦИИ
// ====================================================================================

// 1. Украина и беженцы (Самый высокий приоритет)
var refugeeBoostKeywords = []string{
	"ukrain", "flygtning", "asyl", "opholdstilladelse", "hjemsendelse",
	"integrat", "job", "sprog", "skole", "børn", "social", "ydelse",
	"bolig", "kommune", "lov", "regel", "grænse", "pas", "visum",
}

var ukraineWarKeywords = []string{
	"krig", "rusland", "putin", "zelensky", "våben", "kampvogn",
	"missil", "drone", "angreb", "forsvar", "nato", "eu", "sanktion",
	"donbas", "kyiv", "kharkiv", "odesa", "lviv", "front", "soldat",
}

// 2. Локальное (Виборг и окрестности)
var viborgKeywords = []string{
	"viborg", "midtjylland", "skive", "bjerringbro", "karup",
	"stoholm", "sunds", "kjellerup", "silkeborg",
}

// 3. Экономика, Зарплаты, Налоги (Важное для жизни)
var economyKeywords = []string{
	"løn", "lønforhøjelse", "overenskomst", // Зарплата
	"skat", "fradrag", "årsopgørelse", "skattekort", // Налоги
	"økonomi", "inflation", "pris", "priser", "elpris", "varmepris", // Цены
	"budget", "finanslov", "spare", // Бюджет
	"dagpenge", "kontanthjælp", "su", "børnepenge", // Пособия
	"arbejde", "job", "ansættelse", "mangel på arbejdskraft", // Работа
}

// 4. Стройка, Планы, Инфраструктура (Будущее)
var constructionKeywords = []string{
	"byggeri", "nybyg", "renovering", "byggeprojekt", // Стройка
	"lokalplan", "byplan", "høring", "fremtid", // Планы
	"vej", "trafik", "infrastruktur", "bro", "tunnel", "tog", "bane", // Дороги
	"bolig", "lejlighed", "ejendom", "husleje", // Жилье
	"hospital", "sygehus", "skole", "daginstitution", // Соц объекты
}

// 5. Досуг, Культура, События
var leisureKeywords = []string{
	"festival", "koncert", "musik", "kultur", // Культура
	"biograf", "teater", "museum", "udstilling", "kunst", // Искусство
	"oplevelse", "event", "arrangement", "marked", // События
	"ferie", "rejse", "turist", "weekend", // Отдых
	"restaurant", "cafe", "spise", "mad", // Еда
	"sport", "fodbold", "håndbold", "løb", // Спорт
}

// FilterAndTranslateWithOptions - Main pipeline
func FilterAndTranslateWithOptions(items []rss.Item, opts Options) ([]News, error) {
	var result []News
	processedHashes := make(map[string]bool)

	// Sort by date desc
	sort.Slice(items, func(i, j int) bool {
		return items[i].Published.After(items[j].Published)
	})

	for _, item := range items {
		// 1. Basic Filters
		if time.Since(item.Published) > opts.MaxAge {
			continue
		}

		// Dedup by Link
		if processedHashes[item.Link] {
			continue
		}

		// Dedup by Title similarity
		isDup := false
		for _, existing := range result {
			if isSimilarTitle(item.Title, existing.Title) {
				isDup = true
				break
			}
		}
		if isDup {
			metrics.Global.IncrementDuplicatesFiltered()
			continue
		}

		// 2. Context Scoring
		score, cat := calculateNewsScore(item)
		if score < 10 {
			// Skip low relevance
			continue
		}

		// 3. Create News Object
		n := News{
			Title:            strings.TrimSpace(item.Title),
			Content:          cleanContent(item.Description + " " + item.Content),
			Link:             item.Link,
			Published:        item.Published,
			Category:         cat,
			Score:            score,
			SourceName:       item.SourceName,
			SourceCategories: item.Categories,
			ImageURL:         item.ImageURL,
		}

		// Extract cleaner image if RSS image is missing or small
		if n.ImageURL == "" || strings.Contains(n.ImageURL, "pixel") {
			if extractedImg := scraper.ExtractImage(n.Link); extractedImg != "" {
				n.ImageURL = extractedImg
			}
		}

		// 4. AI Processing
		if aiClient != nil {
			fullText, err := scraper.ScrapeArticleText(n.Link)
			if err != nil {
				log.Printf("Scrape failed for %s: %v", n.Link, err)
				fullText = n.Content // fallback
			}

			if len(fullText) > 15000 {
				fullText = fullText[:15000]
			}

			analysis, err := aiClient.AnalyzeNews(fullText, n.Title)
			if err == nil {
				n.SummaryDanish = analysis.SummaryDanish
				n.SummaryUkrainian = analysis.SummaryUkrainian
				n.TitleUkrainian = analysis.TitleUkrainian
				n.Mood = analysis.Mood
				n.Tags = analysis.Tags

				if opts.EnableImportanceLine {
					n.ImportanceDanish = analysis.ImportanceDanish
					n.ImportanceUkrainian = analysis.ImportanceUkrainian
				}
				metrics.Global.IncrementSuccessfulTranslations()
			} else {
				log.Printf("AI Analysis failed: %v", err)
				trTitle, _ := translate.TranslateText(n.Title, "da", "uk")
				n.TitleUkrainian = trTitle
				n.SummaryDanish = n.Content
				trSum, _ := translate.TranslateText(n.Content, "da", "uk")
				n.SummaryUkrainian = trSum
				n.Mood = "neutral"
			}
		}

		processedHashes[n.Link] = true
		result = append(result, n)

		if len(result) >= opts.Limit {
			break
		}
	}

	metrics.Global.UpdateTotalProcessed(len(items))
	return result, nil
}

// Options for filtering
type Options struct {
	Limit                int
	MaxAge               time.Duration
	PerSource            int
	MaxGeminiRequests    int
	ScrapeMaxArticles    int
	ScrapeConcurrency    int
	EnableImportanceLine bool
}

// ====================================================================================
// НОВАЯ ЛОГИКА ФОРМАТИРОВАНИЯ (WIDER SMART LEAD)
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

// FormatNewsWithImage - ШИРОКИЙ режим (до 800 знаков)
func FormatNewsWithImage(n News, minSentences, maxSentences int) string {
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

// FormatCaptionForPhoto - Режим фото (Заполняем 1024 знака)
func FormatCaptionForPhoto(n News, maxLen int, sentencesPerLang int, minPerLang int) string {
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

func ShouldUsePhoto(n News, maxLen int, sentencesPerLang int, minPerLang int, minTotal int) bool {
	cap := FormatCaptionForPhoto(n, maxLen, sentencesPerLang, minPerLang)
	return utf8.RuneCountInString(cap) < maxLen && utf8.RuneCountInString(cap) > 100
}

// Вспомогательные функции

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
	lastDot := strings.LastIndex(cut, ".")
	if lastDot > limit/2 {
		return string(r[:lastDot+1])
	}

	lastSpace := strings.LastIndex(cut, " ")
	if lastSpace > limit/3 {
		return string(r[:lastSpace]) + "..."
	}

	return string(r[:limit-1]) + "..."
}

func limitText(s string, max int) string {
	return trimToNearestSentenceOrWord(s, max)
}

func cleanContent(s string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	s = re.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// calculateNewsScore - ОБНОВЛЕННАЯ ЛОГИКА ОЦЕНКИ
func calculateNewsScore(item rss.Item) (int, string) {
	text := strings.ToLower(item.Title + " " + item.Description)

	// 1. Ukraine (Highest)
	if containsAny(text, ukraineWarKeywords) || containsAny(text, refugeeBoostKeywords) {
		return 100, "ukraine"
	}

	// 2. Local Viborg
	if containsAny(text, viborgKeywords) {
		return 80, "viborg"
	}

	// 3. Economy (High Relevance) - Приравниваем к "denmark", но с высоким баллом
	if containsAny(text, economyKeywords) {
		return 70, "denmark"
	}

	// 4. Construction & Plans
	if containsAny(text, constructionKeywords) {
		return 65, "denmark"
	}

	// 5. Leisure & Culture
	if containsAny(text, leisureKeywords) {
		return 60, "denmark" // или "culture" если решишь добавить такую категорию в app.go
	}

	// 6. General Denmark
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

func isSimilarTitle(a, b string) bool {
	return strings.TrimSpace(strings.ToLower(a)) == strings.TrimSpace(strings.ToLower(b))
}
