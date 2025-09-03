package news

import (
	"dknews/internal/gemini"
	"dknews/internal/rss"
	"dknews/internal/scraper"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
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
	"ukraine", "ukraina", "ukrainer", "українці", "украин",
	"hjælp ukraine", "help ukraine", "допомога україна",
	"flygtning", "refugee", "біженці", "біженець",
	"krig", "war", "støtte ukraine", "support ukraine", "підтримк україни",
	"våben ukraine", "weapon ukraine", "зброя україни",
	"missiler ukraine", "missiles ukraine", "ракети україні", "sundhed", "health", "здоров'я",
	"flygtninge krise", "refugee crisis", "криза біженців",
	"nato", "нато",
	"sanction", "санкці",
}

// Keywords for important Denmark news
var denmarkKeywords = []string{
	"danmark", "danish", "данія", "данська",
	"regering", "government", "правительств", "уряд",
	"politik", "politics", "политик", "політика",
	"økonomi", "economy", "экономик", "економіка",
	"minister", "министр", "міністр",
	"valg", "election", "выборы", "вибори",
	"eu", "europe", "европа", "європа",
	"samråd", "consultation", "консультація", "консультації",
	"corona", "covid", "visa", "візи",

	// визовые и беженские темы — базовые ключевые слова включены, дополнительные б��сты ниже
	"refugee", "беженцы", "біженці", "asylum", "убежище", "притулок",
	"residence permit", "вид на жительство", "посвідка на проживання",

	// Добавляем более общие ключевые слова для тестирования
	"nyheder", "news", "новости", "новини",
	"verden", "world", "світ", "мир",
	"samfund", "society", "суспільство", "общество",

	// Еще более общие слова для захвата большего колва новостей
	"danske", "danish", "viborg", "датське",
	"københav", "copenhagen", "копенгаген", "копенгага",
	"aarhus", "odense", "aalborg",
	"region", "kommune", "borgere", "citizens",
	"beslutning", "decision", "рішення", "решение",
	"lov", "law", "закон", "право",
	"nye", "new", "новий", "новый",
	"stor", "large", "великий", "большой",
}

// Extra boost keywords for refugee/visa related stories to increase priority
var refugeeBoostKeywords = []string{
	"refugee", "viborg",
	"flygtning", "refugee visa", "temporary protection", "тимчасовий захист",
}

var visaBoostKeywords = []string{
	"visa", "visa extension", "продление визы", "продовження візи",
	"residence permit", "вид на жительство", "залишитися в єс", "stay in eu",
}

// Words to exclude (not important topics)
var excludeKeywords = []string{
	"vejr", "weather", "погода",
	"musik", "music", "музыка", "музик",
	"film", "movie", "фільм", "кіно",
	"celebrity", "знаменит",
	"fodbold result", "football result", "результат футбол",
	"sport result", "спортив результат", "результат спорт",
	"tv program", "телепрогр", "телепрограма",
	"horoskop", "гороскоп",
	"madopskrift", "recipe", "рецепт",
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

// NewsFilter contains filtering configuration
type NewsFilter struct {
	Categories            []string
	MinScore              int
	MaxAge                time.Duration
	ExcludeKeywords       []string
	RequiredKeywords      []string
	EnableContentScraping bool
}

// DefaultFilter returns default filtering configuration
func DefaultFilter() *NewsFilter {
	return &NewsFilter{
		Categories:            []string{"ukraine", "denmark", "visas", "integration"},
		MinScore:              20,
		MaxAge:                24 * time.Hour,
		ExcludeKeywords:       excludeKeywords,
		EnableContentScraping: true,
	}
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
		if strings.Contains(text, "våben") || strings.Contains(text, "weapon") || strings.Contains(text, "missiler") {
			score += 20
		}
		if strings.Contains(text, "hjælp") || strings.Contains(text, "help") {
			score += 15
		}
		return "ukraine", score
	}

	// Important Denmark news - ОЧЕНЬ мягкие кри��ерии для тест
	if containsAny(text, denmarkKeywords) {
		score := 30
		// Extra points for politics and economy
		if containsAny(text, []string{"regering", "minister", "minister"}) {
			score += 20
		}
		if containsAny(text, []string{"politik", "økonomi"}) {
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

// Enhanced scoring with more precise keyword matching
func calculateNewsScoreEnhanced(item *rss.FeedItem, filter *NewsFilter) (string, int) {
	text := strings.ToLower(item.Title + " " + item.Description)

	// Check exclude keywords first
	if containsAny(text, filter.ExcludeKeywords) {
		return "", 0
	}

	// Ukraine news - highest priority with more nuanced scoring
	if containsAny(text, ukraineKeywords) {
		score := 100

		// Critical keywords boost
		criticalWords := []string{"våben", "weapon", "missiler", "missiles", "angreb", "attack"}
		if containsAny(text, criticalWords) {
			score += 30
		}

		// Support/aid keywords
		supportWords := []string{"hjælp", "help", "støtte", "support", "bistand", "aid"}
		if containsAny(text, supportWords) {
			score += 20
		}

		// Integration keywords for Ukrainian refugees
		integrationWords := []string{"integration", "arbejde", "work", "bolig", "housing", "børn", "children"}
		if containsAny(text, integrationWords) {
			score += 15
		}

		return "ukraine", score
	}

	// Denmark news with better categorization
	if containsAny(text, denmarkKeywords) {
		score := 30

		// Government/Politics boost
		politicsWords := []string{"regering", "government", "minister", "folketinget", "parliament"}
		if containsAny(text, politicsWords) {
			score += 25
		}

		// Immigration/Integration boost
		immigrationWords := []string{"udlændinge", "foreigners", "indvandring", "immigration", "integration"}
		if containsAny(text, immigrationWords) {
			score += 20
		}

		// Visa/Legal matters boost
		legalWords := []string{"visa", "opholdstilladelse", "residence", "statsborgerskab", "citizenship"}
		if containsAny(text, legalWords) {
			score += 18
		}

		return "denmark", score
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
	if aiClient == nil {
		return nil, fmt.Errorf("gemini client not initialized; call news.SetGeminiClient")
	}

	seen := map[string]struct{}{}
	var candidates []News

	log.Printf("Начинаем фильтрацию из %d новостей", len(items))

	for _, item := range items {
		// Ограничиваем обработку только свежими новостями (последние 24 часа)
		if item.PublishedParsed != nil && time.Since(*item.PublishedParsed) > 24*time.Hour {
			continue
		}

		// Дедупликация по ссылке
		if _, dup := seen[item.Link]; dup {
			continue
		}
		seen[item.Link] = struct{}{}

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

	max := 8
	if len(candidates) < max {
		max = len(candidates)
	}
	urls := make([]string, max)
	for i := 0; i < max; i++ {
		urls[i] = candidates[i].Link
	}

	log.Printf("Извлекаем полный контент %d статей...", max)
	fullArticles := scraper.ExtractArticlesInBackground(urls)

	res := make([]News, 0, max)
	for i := 0; i < max; i++ {
		n := candidates[i]
		if fa, ok := fullArticles[n.Link]; ok && len(fa.Content) > 200 {
			n.Content = fa.Content
			log.Printf("✅ Полный контент (%d) для: %s", len(n.Content), n.Title)
		} else {
			log.Printf("⚠️ Краткое описание для: %s", n.Title)
		}

		log.Printf("Gemini summary %d/%d: %s", i+1, max, n.Title)
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
