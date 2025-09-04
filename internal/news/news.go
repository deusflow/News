package news

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"dknews/internal/gemini"
	"dknews/internal/metrics"
	"dknews/internal/rss"
	"dknews/internal/scraper"
)

// News represents a single news item enriched by Gemini summaries.
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

	Summary          string // Original language summary (or detected)
	SummaryDanish    string // Danish version of summary
	SummaryUkrainian string // Ukrainian version of summary
}

// Keywords for Ukraine news (high priority)
var ukraineKeywords = []string{
	"ukraine",
	"ukraina",
	"ukrainer",
	"hjælp ukraine",
	"flygtning",
	"krig",
	"støtte ukraine",
	"våben ukraine",
	"missiler ukraine",
	"sundhed",
	"flygtningekrise",
	"nato",
	"sanktion",
	"skole",
	"uddannelse",
	"undervisning",
	"børn",
	"lærer",
	"studie",
	"eksamen",
	"universitet",
	"folkeskole",
	"sprogskole",
}

// Keywords for important Denmark news
var denmarkKeywords = []string{
	"danmark",
	"regering",
	"politik",
	"økonomi",
	"minister",
	"valg",
	"eu",
	"samråd",
	"corona",
	"visum",
	"flygtning",
	"asyl",
	"opholdstilladelse",
	"nyheder",
	"verden",
	"samfund",
	"danske",
	"viborg",
	"københavn",
	"aarhus",
	"odense",
	"aalborg",
	"region",
	"kommune",
	"borgere",
	"beslutning",
	"lov",
	"nye",
	"stor",
	"krig",
	"krigs",
	"krigsvirkning",
	"Viborg",
	"8800 Viborg",
	"udlændinge",
	"indvandring",
	"integration",
	"arbejde",
	"bolig",
	"børn",
	"skole",
	"uddannelse",
	"undervisning",
	"lærer",
	"studie",
	"eksamen",
	"universitet",
	"folkeskole",
	"sprogskole",
	"friends",
	"venner",
	"familie",
}

// Extra boost keywords for refugee/visa related stories to increase priority
var refugeeBoostKeywords = []string{
	"refugee",
	"viborg",
	"flygtning",
	"refugee visa",
	"temporary protection",
	"asylum",
	"asylum support",
	"asylum application",
	"asylum application form",
	"asylum application form ukraine",
	"asylum application form denmark",
	"families",
	"family",
}

var visaBoostKeywords = []string{
	"visum",
	"visumforlængelse",
	"opholdstilladelse",
	"blive i EU",
}

// Words to exclude (not important topics)
var excludeKeywords = []string{
	"vejr",
	"musik",
	"film",
	"kendis",
	"fodboldresultat",
	"sportsresultat",
	"tv-program",
	"horoskop",
	"madopskrift",
}

// containsAny checks if string has any keyword (whole-word aware)
func containsAny(s string, keywords []string) bool {
	s = strings.ToLower(s)
	for _, kw := range keywords {
		pattern := `\b` + regexp.QuoteMeta(strings.ToLower(kw)) + `\b`
		matched, _ := regexp.MatchString(pattern, s)
		if matched {
			return true
		}
	}
	return false
}

// makeNewsKey generates a hash key from title and description for deduplication
func makeNewsKey(title, description string) string {
	h := sha1.New()
	h.Write([]byte(strings.ToLower(title + description)))
	return hex.EncodeToString(h.Sum(nil))
}

// makeSimilarityKey creates a more lenient key for detecting similar news
func makeSimilarityKey(title string) string {
	// Remove common words and normalize for similarity detection
	title = strings.ToLower(title)

	// Remove common Danish words that don't affect content
	commonWords := []string{"og", "er", "en", "det", "til", "af", "på", "med", "for", "som", "kan", "vil", "har", "skal", "alle", "den", "nye", "stor", "lille"}
	words := strings.Fields(title)
	var filtered []string

	for _, word := range words {
		word = strings.Trim(word, ".,!?:;\"'-")
		if len(word) > 2 {
			isCommon := false
			for _, common := range commonWords {
				if word == common {
					isCommon = true
					break
				}
			}
			if !isCommon {
				filtered = append(filtered, word)
			}
		}
	}

	// Take only first 5-6 meaningful words for similarity
	if len(filtered) > 6 {
		filtered = filtered[:6]
	}

	h := sha1.New()
	h.Write([]byte(strings.Join(filtered, " ")))
	return hex.EncodeToString(h.Sum(nil))
}

// calculateNewsScore gets news importance
func calculateNewsScore(item *rss.FeedItem) (string, int) {
	text := strings.ToLower(item.Title + " " + item.Description)

	// Exclude not important topics
	if containsAny(text, excludeKeywords) {
		return "", 0
	}

	// Ukraine news - highest priority
	if containsAny(text, ukraineKeywords) {
		score := 100
		// Extra points for important words
		if strings.Contains(text, "skole") || strings.Contains(text, "børn") || strings.Contains(text, "uddannelse") {
			score += 20
		}
		if strings.Contains(text, "hjælp") || strings.Contains(text, "help") {
			score += 15
		}
		return "ukraine", score
	}

	// Important Denmark news - ОЧЕНЬ мягкие критерии для тест
	if containsAny(text, denmarkKeywords) {
		score := 30
		// Extra points for politics and economy
		if containsAny(text, []string{"regering", "friends", "minister", "skole", "uddannelse", "undervisning", "børn", "lærer", "studie", "eksamen", "universitet", "folkeskole"}) {
			score += 20
		}
		if containsAny(text, []string{"politik", "økonomi", "money", "penge"}) {
			score += 15
		}

		// Boost for refugee/visa related stories (they're important for the audience)
		if containsAny(text, refugeeBoostKeywords) {
			score += 20
		}
		if containsAny(text, visaBoostKeywords) {
			score += 15
		}

		return "denmark", score
	}

	// ВРЕМЕННО: Захватываем ВСЕ новости для тестирования системы перевода
	// Если в заголовке или описании есть хоть какие-то слова - пропускаем
	if len(strings.Fields(text)) > 3 {
		return "general", 20 // Минимальный балл для общих новостей
	}

	return "", 0
}

// Gemini client injection
var aiClient *gemini.Client

// SetGeminiClient sets the Gemini client for translation and summarization
func SetGeminiClient(c *gemini.Client) {
	aiClient = c
}

// FilterAndTranslate now: filter + scrape + Gemini summarize + multi-language summary.
func FilterAndTranslate(items []*rss.FeedItem) ([]News, error) {
	startTime := time.Now()
	defer func() {
		metrics.Global.RecordProcessingTime(time.Since(startTime))
		metrics.Global.SetLastRun()
	}()

	if aiClient == nil {
		return nil, fmt.Errorf("gemini client not initialized; call news.SetGeminiClient")
	}
	log.Println("[Gemini] Starting filter + scrape + summarize pipeline (TranslateAndSummarizeNews)")

	seenLinks := map[string]struct{}{}
	seenContent := map[string]struct{}{}
	seenSimilar := map[string]struct{}{} // Новый уровень дедупликации по схожести
	var candidates []News

	log.Printf("Начинаем фильтрацию из %d новостей", len(items))

	for _, item := range items {
		metrics.Global.IncrementNewsProcessed()

		// Ограничиваем обработку только свежими новостями (последние 24 часа)
		if item.PublishedParsed != nil && time.Since(*item.PublishedParsed) > 24*time.Hour {
			continue
		}

		// Дедупликация по ссылке
		if _, dup := seenLinks[item.Link]; dup {
			log.Printf("🔗 Дубликат по ссылке: %s", item.Title)
			metrics.Global.IncrementDuplicatesFiltered()
			continue
		}
		seenLinks[item.Link] = struct{}{}

		// Дедупликация по содержанию (заголовок + описание)
		key := makeNewsKey(item.Title, item.Description)
		if _, dup := seenContent[key]; dup {
			log.Printf("📄 Дубликат по содержанию: %s", item.Title)
			metrics.Global.IncrementDuplicatesFiltered()
			continue
		}
		seenContent[key] = struct{}{}

		// Дедупликация по схожести заголовков (более мягкая)
		similarKey := makeSimilarityKey(item.Title)
		if _, dup := seenSimilar[similarKey]; dup {
			log.Printf("🔄 Похожая новость (пропускаем): %s", item.Title)
			metrics.Global.IncrementDuplicatesFiltered()
			continue
		}
		seenSimilar[similarKey] = struct{}{}

		// Вычисляем категорию и важност��
		category, score := calculateNewsScore(item)
		if score == 0 {
			continue // Пропускаем неважные новости
		}

		published := time.Now()
		if item.PublishedParsed != nil {
			published = *item.PublishedParsed
		}

		candidates = append(candidates, News{
			Title:            item.Title,
			Content:          item.Description, // Пока краткое описание, полный контент добавим после
			Link:             item.Link,
			Published:        published,
			Category:         category,
			Score:            score,
			SourceName:       item.Source.Name,
			SourceLang:       item.Source.Lang,
			SourceCategories: item.Source.Categories,
		})

		log.Printf("Добавлена новость [%s, score:%d, source:%s]: %s", category, score, item.Source.Name, item.Title)
	}

	// sort
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score // По убыванию важности
		}
		return candidates[i].Published.After(candidates[j].Published) // По времени (новые первыми)
	})

	newsLimit := 8
	if len(candidates) < newsLimit {
		newsLimit = len(candidates)
	}
	urls := make([]string, newsLimit)
	for i := 0; i < newsLimit; i++ {
		urls[i] = candidates[i].Link
	}

	log.Printf("Извлекаем полный контент %d статей...", newsLimit)
	fullArticles := scraper.ExtractArticlesInBackground(urls)

	res := make([]News, 0, newsLimit)
	for i := 0; i < newsLimit; i++ {
		n := candidates[i]
		if fa, ok := fullArticles[n.Link]; ok && len(fa.Content) > 200 {
			n.Content = fa.Content
			log.Printf("✅ Полный контент (%d) для: %s", len(n.Content), n.Title)
		} else {
			log.Printf("⚠️ Краткое описание для: %s", n.Title)
		}

		log.Printf("Gemini summary %d/%d: %s", i+1, newsLimit, n.Title)
		aiResp, err := aiClient.TranslateAndSummarizeNews(n.Title, n.Content)
		if err != nil {
			log.Printf("❌ Gemini error: %v", err)
			n.Summary = fallbackSummary(n.Content)
			n.SummaryDanish = "(Ingen AI)"
			n.SummaryUkrainian = "(Немає AI)"
		} else {
			n.Summary = aiResp.Summary
			n.SummaryDanish = aiResp.Danish
			n.SummaryUkrainian = aiResp.Ukrainian
		}
		res = append(res, n)
	}

	log.Printf("Обработано %d новостей с саммаризацией", len(res))
	return res, nil
}

func fallbackSummary(content string) string {
	c := strings.TrimSpace(content)
	if c == "" {
		return "(Нет контента)"
	}
	sentences := strings.Split(c, ".")
	var picked []string
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if len(s) < 25 {
			continue
		}
		picked = append(picked, s)
		if len(picked) >= 2 {
			break
		}
	}
	if len(picked) == 0 {
		if len(c) > 160 {
			return c[:160] + "..."
		}
		return c
	}
	return strings.Join(picked, ". ") + "."
}

// FormatNews produces concise formatted output with summaries.
func FormatNews(n News) string {
	var b strings.Builder
	b.WriteString("🇩🇰 *" + n.Title + "*\n")
	if n.SummaryUkrainian != "" {
		b.WriteString("🇺🇦 " + n.SummaryUkrainian + "\n")
	}
	if n.SummaryDanish != "" {
		b.WriteString("🇩🇰 " + n.SummaryDanish + "\n")
	}
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━")
	return b.String()
}
