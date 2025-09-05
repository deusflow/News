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

// Географические / "украинские" термины (про саму Украину и украинцев)
var ukraineGeoKeywords = []string{
	"ukraine", "ukraina", "ukrainer", "ukrainsk", "ukrainere", "ukrainske",
	"ukrainske familier", "ukrainske i danmark", "ukrainere i danmark",
	"ukrainsk diaspora", "flygtninge fra ukraine",
}

var denmarkKeywords = []string{
	"danmark", "danske", "københavn", "aarhus", "aalborg", "viborg",
	"region", "kommune", "borgere", "lov", "politik", "økonomi",
	"visum", "opholdstilladelse", "asyl", "integration", "arbejde", "bolig",
	"udlændinge",
}

var conflictKeywords = []string{
	"krig", "krigen", "putin", "zelensky", "invasion", "bomb", "missil", "russisk", "war", "invasion",
}

// Технологии / инновации / стартапы / исследования
var techKeywords = []string{
	"teknologi", "innovation", "startup", "forskning", "research", "patent",
	"robot", "software", "hardware", "IT", "cloud", "cyber", "data",
	"machine learning", "deep learning", "artificial intelligence", "AI", "maskinlæring", "LLM",
}

// Исключительно AI-термины (чтобы точно поймать ИИ-новости)
var aiKeywords = []string{
	"ai", "artificial intelligence", "maskinlæring", "neuralt netværk", "large language model", "llm",
}

// Медицинские / фармацевтические темы
var medicalKeywords = []string{
	"lægemidler", "medicin", "vaccine", "klinisk forsøg", "pharma", "biotek", "behandling", "treatment",
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

// Европа / европейский контекст (шире чем Дания)
var europeKeywords = []string{
	"europa", "eu", "european", "eu-lande", "europeisk",
}

// improved containsAny: distinguishes phrases and short words (avoids "ai" matching "said")
func containsAny(text string, keywords []string) bool {
	text = strings.ToLower(text)

	for _, k := range keywords {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}

		// If keyword is a phrase (contains space) -> substring match
		if strings.Contains(k, " ") {
			if strings.Contains(text, k) {
				return true
			}
			continue
		}

		// Short tokens (<=3) -> whole word match using word boundary regexp
		if len(k) <= 3 {
			// Use regexp.QuoteMeta to avoid accidental meta-chars in keyword
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(k) + `\b`)
			if re.MatchString(text) {
				return true
			}
			continue
		}

		// Otherwise, simple substring is fine
		if strings.Contains(text, k) {
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
// makeSimilarityKey - менее агрессивная версия.
// Логика:
// 1) Берём host из item.Link (если есть) — чтобы ключ был специфичен для источника.
// 2) Нормализуем заголовок: lowercase, убираем пунктуацию, убираем стоп-слова.
// 3) Оставляем первые N значимых слов (по умолчанию 6) — чтобы не склеивать слишком разные заголовки.
// 4) Добавляем временной срез (truncate по окну в hours, по умолчанию 6ч).
// Результат: host|topWords|windowUnix
func makeSimilarityKey(item *rss.FeedItem) string {
	// Параметры: можно менять
	const (
		windowHours = 6 // окно времени для дедупа (меньше -> меньше агрессивности)
		maxWords    = 6 // сколько значимых слов оставить
	)

	// Helper: получить host из ссылки
	getHost := func(link string) string {
		if link == "" {
			return "unknown"
		}
		u, err := url.Parse(link)
		if err != nil || u.Host == "" {
			// иногда в feed может быть относительный линк или пустой
			return "unknown"
		}
		return strings.ToLower(u.Host)
	}

	// Helper: нормализация текста — убрать пунктуацию, multiple spaces, lower
	normalize := func(s string) string {
		s = strings.ToLower(s)
		// удалить HTML-теги если вдруг
		reTags := regexp.MustCompile(`<[^>]*>`)
		s = reTags.ReplaceAllString(s, " ")

		// Оставить только буквы, цифры и пробелы (Unicode-aware)
		var b []rune
		b = make([]rune, 0, len(s))
		for _, r := range s {
			if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSpace(r) {
				b = append(b, r)
			} else {
				// заменяем на пробел, чтобы разделять слова
				b = append(b, ' ')
			}
		}
		out := strings.Join(strings.Fields(string(b)), " ")
		return out
	}

	// Небольшой набор стоп-слов — расширяй по необходимости (датский/английский)
	stopWords := map[string]bool{
		"a": true, "an": true, "the": true, "og": true, "i": true, "på": true,
		"til": true, "af": true, "med": true, "for": true, "er": true, "der": true,
		"om": true, "en": true, "et": true, "ikke": true,
	}

	// Собираем текст: title + short description
	text := strings.TrimSpace(item.Title + " " + item.Description)
	norm := normalize(text)
	words := strings.Fields(norm)

	// Оставляем только «значимые» слова
	significant := make([]string, 0, len(words))
	for _, w := range words {
		if len(significant) >= maxWords {
			break
		}
		if stopWords[w] {
			continue
		}
		// игнорируем слишком короткие слова (<=2)
		if len(w) <= 2 {
			continue
		}
		significant = append(significant, w)
	}
	// Если не осталось значимых слов — возьмём первые maxWords из оригинала (без стоп-словой фильтрации)
	if len(significant) == 0 && len(words) > 0 {
		for i := 0; i < len(words) && i < maxWords; i++ {
			significant = append(significant, words[i])
		}
	}

	// временной срез: используем PublishedParsed если есть, иначе текущий час
	var t time.Time
	if item.PublishedParsed != nil {
		t = *item.PublishedParsed
	} else if item.Published != "" {
		// попробуем распарсить Published (без гарантий) — безопасный fallback
		if parsed, err := time.Parse(time.RFC1123Z, item.Published); err == nil {
			t = parsed
		} else if parsed2, err2 := time.Parse(time.RFC1123, item.Published); err2 == nil {
			t = parsed2
		} else {
			t = time.Now()
		}
	} else {
		t = time.Now()
	}
	// Обрезаем время до начала окна (например, 6ч)
	windowStart := t.Truncate(time.Duration(windowHours) * time.Hour).Unix()

	host := getHost(item.Link)

	// Финальный ключ
	key := fmt.Sprintf("%s|%s|%d", host, strings.Join(significant, "_"), windowStart)
	return key
}

// calculateNewsScore - новая логика приоритезации
// calculateNewsScore - переработанная логика приоритезации
func calculateNewsScore(item *rss.FeedItem) (string, int) {
	text := strings.ToLower(item.Title + " " + item.Description)

	// Быстрая фильтрация
	if containsAny(text, excludeKeywords) {
		return "", 0
	}

	// Флаги
	hasDenmark := containsAny(text, denmarkKeywords)
	hasUkraineGeo := containsAny(text, ukraineGeoKeywords)
	hasEurope := containsAny(text, europeKeywords)
	hasTech := containsAny(text, techKeywords) || containsAny(text, aiKeywords)
	hasMedical := containsAny(text, medicalKeywords)
	hasConflict := containsAny(text, conflictKeywords)
	hasRefugeeBoost := containsAny(text, refugeeBoostKeywords)
	hasVisaBoost := containsAny(text, visaBoostKeywords)

	// Если это только "международное" упоминание войны/путин и НЕТ локального контекста — пропускаем.
	if hasConflict && !(hasDenmark || hasUkraineGeo || hasEurope) {
		return "", 0
	}

	// Переменные результата
	var category string
	score := 0

	// 1) Если это про технологии/ИИ/медицину — требуем гео-контекст
	if hasTech || hasMedical {
		if !(hasDenmark || hasUkraineGeo || hasEurope) {
			// технология/медицина без локальной привязки — не релевантно
			return "", 0
		}
		if hasMedical {
			category = "health"
		} else {
			category = "tech"
		}
		score = 80
		// AI-премия
		if containsAny(text, aiKeywords) {
			score += 10
		}
		// не возвращаем здесь — даём возможность добавить локальные бонусы ниже
	}

	// 2) Новости про украинцев / проблемы беженцев / визы — высокая приоритетность
	if hasUkraineGeo || hasRefugeeBoost || hasVisaBoost {
		// если категория ещё не установлена (не tech/health), установить "ukraine"
		if category == "" {
			category = "ukraine"
			score = 70
		} else {
			// если уже tech/health — усиливаем score для локального украинского контекста
			score += 5
		}
		// локальные бонусы
		if hasDenmark {
			score += 15
		}
		if hasEurope {
			score += 5
		}
		// Откат "войны" как главный фактор, если кроме неё нет соц/визы/интеграции
		if hasConflict && !(hasRefugeeBoost || hasVisaBoost || hasDenmark) {
			score -= 15
		}
		// Возвращаем, потому что это уже явный приоритетный блок
		return category, score
	}

	// 3) Общие датские/европейские новости
	if hasDenmark || hasEurope {
		// если категория не установлена — сделать denmark
		if category == "" {
			category = "denmark"
			score = 40
		}
		// маленький бонус за политику/экономику
		if containsAny(text, []string{"politik", "regering", "økonomi", "minister"}) {
			score += 15
		}
		// Технологические/медицинские вставки усиливают релевантность даже если категория "denmark"
		if hasTech && category != "tech" {
			score += 10
		}
		if hasMedical && category != "health" {
			score += 10
		}
		// бонусы для виз/беженцев
		if hasRefugeeBoost {
			score += 20
		}
		if hasVisaBoost {
			score += 25
		}
		// если в тексте также есть конфликтный контекст — немного снизим
		if hasConflict {
			score -= 5
		}
		// конец ветки
		return category, score
	}

	// 4) Если после всех проверок категория всё ещё пустая — не релевантно
	if category == "" {
		return "", 0
	}

	// 5) Если категория установлена (только tech/health путь оставался), применим общие бонусы

	// Гарантируем неотрицательный скор
	if score < 0 {
		score = 0
	}

	return category, score
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
		similarKey := makeSimilarityKey(item)
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

		sourceName, sourceLang := "", ""
		var sourceCategories []string
		if item.Source != nil {
			sourceName = item.Source.Name
			sourceLang = item.Source.Lang
			sourceCategories = item.Source.Categories
		}

		candidates = append(candidates, News{
			Title:            item.Title,
			Content:          item.Description, // Пока краткое описание, полный контент добавим после
			Link:             item.Link,
			Published:        published,
			Category:         category,
			Score:            score,
			SourceName:       sourceName,
			SourceLang:       sourceLang,
			SourceCategories: sourceCategories,
		})

		log.Printf("Добавлена новость [%s, score:%d, source:%s]: %s", category, score, sourceName, item.Title)
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
