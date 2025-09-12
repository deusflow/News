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

// Тематики для подростков и родителей
var youthKeywords = []string{
	"ungdom", "teenager", "unge", "skole", "gymnasium", "uddannelse", "studerende",
	"fritid", "sport", "gaming", "esport", "social media", "mobil", "app",
	"musik", "festival", "koncert", "streaming", "youtube", "tiktok", "instagram",
	"snapchat", "discord", "twitch", "netflix", "spotify", "podcast",
	"mode", "influencer", "blogger", "vlogger", "content creator",
	"mental sundhed", "stress", "angst", "selvværd", "mobning", "cybermobning",
	"kæreste", "venskab", "dating", "ungdomskultur", "trend", "viral",
	"uddannelsesvalg", "studievejledning", "efterskole", "gap year",
	"job", "praktikplads", "sommerjob", "ungdomsarbejde", "cv",
}

var parentKeywords = []string{
	"forældre", "børn", "familie", "dagpleje", "børnehave", "skole", "mor", "far",
	"graviditet", "fødsel", "baby", "småbørn", "teenager", "opdragelse", "familieøkonomi",
	"børnepenge", "orlov", "barsel", "familieydelse", "SFO", "fritidsordning",
	"mødregruppe", "fædregruppe", "forældremøde", "forældreinddragelse",
	"børns udvikling", "motorik", "sprog", "læsning", "matematik",
	"allergi", "astma", "vaccination", "sundhedspleje", "børnelæge",
	"skilsmisse", "samvær", "børnebidrag", "forældremyndighed",
	"digital opdragelse", "skærmtid", "online sikkerhed", "cybersikkerhed",
	"bullying", "mobning", "skolevægring", "særlige behov", "inklusion",
	"familieaktiviteter", "ferie", "børnevenlig", "legeplads", "zoo", "museum",
	"boligsøgning", "børnevenlig bolig", "sikkerhed hjemme", "babyproofing",
}

var culturalKeywords = []string{
	"kultur", "museum", "teater", "opera", "kunst", "udstilling", "galleri",
	"litteratur", "bog", "forfatter", "bibliotek", "kulturel", "traditions",
	"folkefest", "festival", "kulturnat", "kunstmuseum", "kulturhus",
	"dansk kultur", "historie", "arv", "traditioner", "kulturformidling",
	"scene", "skuespil", "ballet", "koncert", "klassisk musik", "jazz",
	"film", "documentary", "kortfilm", "filminstruktør", "dansk film",
	"design", "arkitektur", "møbler", "dansk design", "designmuseum",
}

var sportsKeywords = []string{
	"sport", "fodbold", "håndbold", "cykling", "svømning", "atletik", "fitness",
	"idræt", "konkurrence", "mesterskab", "olympiske", "VM", "EM",
	"badminton", "tennis", "basketball", "volleyball", "gymnastik",
	"løb", "marathon", "triathlon", "styrketræning", "crossfit",
	"børnesport", "ungdomsidræt", "idrætsforening", "klub", "hold",
	"sundhed", "motion", "aktiv", "træning", "coaching", "instruktør",
	"parasport", "handicapidræt", "inklusion i sport", "tilgængelighed",
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
// 2) Нормализуем заголовок: lowercase, убиираем пунктуацию, убираем стоп-слова.
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
	hasTech := containsAny(text, techKeywords)
	hasMedical := containsAny(text, medicalKeywords)
	hasConflict := containsAny(text, conflictKeywords)
	hasRefugeeBoost := containsAny(text, refugeeBoostKeywords)
	hasVisaBoost := containsAny(text, visaBoostKeywords)
	hasYouth := containsAny(text, youthKeywords)
	hasParent := containsAny(text, parentKeywords)
	hasCultural := containsAny(text, culturalKeywords)
	hasSports := containsAny(text, sportsKeywords)

	ctxLocal := hasDenmark || hasUkraineGeo || hasEurope

	// Если это только "международное" упоминание войны/Путин без локального контекста — пропускаем
	if hasConflict && !ctxLocal {
		return "", 0
	}

	// Переменные результата
	var category string
	score := 0

	// 1) Новости про украинцев / проблемы беженцев / визы — высокая приоритетность
	if hasUkraineGeo || hasRefugeeBoost || hasVisaBoost {
		category = "ukraine"
		score = 70
		if hasDenmark {
			score += 15
		}
		if hasEurope {
			score += 5
		}
		if hasConflict && !(hasRefugeeBoost || hasVisaBoost || hasDenmark) {
			score -= 15
		}
		if hasTech {
			score += 10
		}
		if hasMedical {
			score += 10
		}
		return category, score
	}

	// 2) Технологии/медицина — требуем гео-контекст
	if hasTech || hasMedical {
		if !ctxLocal {
			return "", 0
		}
		if hasMedical {
			category = "health"
		} else {
			category = "tech"
		}
		score = 80
		if containsAny(text, aiKeywords) {
			score += 10
		}
		if hasDenmark {
			score += 10
		}
		if hasEurope {
			score += 5
		}
		return category, score
	}

	// 3) Семья/родители (до общего датского блока, чтобы не было unreachable бонусов)
	if hasParent && ctxLocal {
		category = "family"
		score = 55
		if hasDenmark {
			score += 10
		}
		return category, score
	}

	// 4) Молодежные темы
	if hasYouth && ctxLocal {
		category = "youth"
		score = 50
		if hasDenmark {
			score += 8
		}
		return category, score
	}

	// 5) Культура
	if hasCultural && ctxLocal {
		category = "culture"
		score = 35
		if hasDenmark {
			score += 10
		}
		return category, score
	}

	// 6) Спорт
	if hasSports && ctxLocal {
		category = "sports"
		score = 30
		if hasDenmark {
			score += 8
		}
		return category, score
	}

	// 7) Общие датские новости
	if hasDenmark {
		category = "denmark"
		score = 40
		if containsAny(text, []string{"politik", "regering", "økonomi", "minister"}) {
			score += 15
		}
		return category, score
	}

	// 8) Общие европейские новости (без датского контекста)
	if hasEurope {
		category = "europe"
		score = 25
		return category, score
	}

	// 9) Чисто конфликтные новости (минимальный приоритет)
	if hasConflict {
		category = "conflict"
		score = 15
		return category, score
	}

	// 10) Общие категории
	if containsAny(text, []string{"økonomi", "business", "marked", "aktier", "bank"}) {
		category = "economy"
		score = 20
	} else if containsAny(text, []string{"miljø", "klima", "climate", "environment", "grøn"}) {
		category = "environment"
		score = 25
	} else if containsAny(text, []string{"uddannelse", "education", "universitet"}) {
		category = "education"
		score = 22
	} else if containsAny(text, []string{"europa", "european", "eu"}) {
		category = "general"
		score = 10
	}

	if category == "" || score == 0 {
		return "", 0
	}

	return category, score
}

// Gemini client injection
var aiClient *gemini.Client

// SetGeminiClient sets the Gemini client for translation and summarization
func SetGeminiClient(c *gemini.Client) {
	aiClient = c
}

// FilterAndTranslate: фильтр + скрапинг + саммаризация Gemini + мультиязычные саммари.
func FilterAndTranslate(items []*rss.FeedItem) ([]News, error) {
	return FilterAndTranslateWithOptions(items, Options{})
}

// Options controls filtering and selection behavior.
type Options struct {
	Limit             int           // how many items to return
	MaxAge            time.Duration // discard items older than this
	PerSource         int           // cap per source in final list
	PerCategory       int           // cap per category in final list
	MaxGeminiRequests int           // maximum Gemini requests allowed (0 = unlimited)
}

// FilterAndTranslateWithOptions performs filtering and summarization using provided options.
func FilterAndTranslateWithOptions(items []*rss.FeedItem, opts Options) ([]News, error) {
	startTime := time.Now()
	defer func() {
		metrics.Global.RecordProcessingTime(time.Since(startTime))
		metrics.Global.SetLastRun()
	}()

	if aiClient == nil {
		return nil, fmt.Errorf("gemini client not initialized; call news.SetGeminiClient")
	}
	log.Println("[Gemini] Starting filter + scrape + summarize pipeline (WithOptions)")

	// defaults
	if opts.Limit <= 0 {
		opts.Limit = 8
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = 24 * time.Hour
	}
	if opts.PerSource <= 0 {
		opts.PerSource = 2
	}
	if opts.PerCategory <= 0 {
		opts.PerCategory = 2
	}

	seenLinks := map[string]struct{}{}
	seenContent := map[string]struct{}{}
	seenSimilar := map[string]struct{}{}
	var seenTitles []string
	var candidates []News

	log.Printf("Начинаем фильтрацию из %d новостей (maxAge=%s)", len(items), opts.MaxAge)

	for _, item := range items {
		metrics.Global.IncrementNewsProcessed()

		// Ограничиваем обработку по возрасту
		if item.PublishedParsed != nil && time.Since(*item.PublishedParsed) > opts.MaxAge {
			continue
		}

		// Улучшенная дедупликация по нормализованной ссылке
		normalizedLink := normalizeURL(item.Link)
		if _, dup := seenLinks[normalizedLink]; dup {
			metrics.Global.IncrementDuplicatesFiltered()
			continue
		}
		seenLinks[normalizedLink] = struct{}{}

		// Дедупликация по содержанию (заголовок + описание)
		key := makeNewsKey(item.Title, item.Description)
		if _, dup := seenContent[key]; dup {
			metrics.Global.IncrementDuplicatesFiltered()
			continue
		}
		seenContent[key] = struct{}{}

		// Дедупликация по схожести заголовков (более мягкая)
		similarKey := makeSimilarityKey(item)
		if _, dup := seenSimilar[similarKey]; dup {
			metrics.Global.IncrementDuplicatesFiltered()
			continue
		}
		seenSimilar[similarKey] = struct{}{}

		// Дополнительная проверка схожести заголовков с уже добавленными
		skipSimilar := false
		for _, existingTitle := range seenTitles {
			if isSimilarTitle(item.Title, existingTitle) {
				metrics.Global.IncrementDuplicatesFiltered()
				skipSimilar = true
				break
			}
		}
		if skipSimilar {
			continue
		}

		// Категория и скор
		category, score := calculateNewsScore(item)
		if score == 0 {
			continue
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
			Content:          item.Description,
			Link:             item.Link,
			Published:        published,
			Category:         category,
			Score:            score,
			SourceName:       sourceName,
			SourceLang:       sourceLang,
			SourceCategories: sourceCategories,
		})

		seenTitles = append(seenTitles, item.Title)
	}

	// Сортировка: скор, затем новизна
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Published.After(candidates[j].Published)
	})

	if len(candidates) == 0 {
		return nil, nil
	}

	// Применяем разнообразие: берём больше пула, чем финальный лимит, чтобы улучшить покрытие
	pool := opts.Limit * 4
	if pool > len(candidates) {
		pool = len(candidates)
	}
	diverseCandidates := selectDiverse(candidates[:pool], opts.Limit, opts.PerSource, opts.PerCategory)

	newsLimit := opts.Limit
	if len(diverseCandidates) < newsLimit {
		newsLimit = len(diverseCandidates)
	}

	urls := make([]string, newsLimit)
	for i := 0; i < newsLimit; i++ {
		urls[i] = diverseCandidates[i].Link
	}

	log.Printf("Извлекаем полный контент %d статей...", newsLimit)
	fullArticles := scraper.ExtractArticlesInBackground(urls)

	res := make([]News, 0, newsLimit)
	geminiRequests := 0
	for i := 0; i < newsLimit; i++ {
		n := diverseCandidates[i]
		if fa, ok := fullArticles[n.Link]; ok && len(fa.Content) > 200 {
			n.Content = fa.Content
		}
		if opts.MaxGeminiRequests > 0 && geminiRequests >= opts.MaxGeminiRequests {
			n.Summary = fallbackSummary(n.Content)
			n.SummaryDanish = "(Ingen AI)"
			n.SummaryUkrainian = "(Немає AI)"
		} else {
			aiResp, err := aiClient.TranslateAndSummarizeNews(n.Title, n.Content)
			if err != nil {
				n.Summary = fallbackSummary(n.Content)
				n.SummaryDanish = "(Ingen AI)"
				n.SummaryUkrainian = "(Немає AI)"
			} else {
				n.Summary = aiResp.Summary
				n.SummaryDanish = aiResp.Danish
				n.SummaryUkrainian = aiResp.Ukrainian
			}
			geminiRequests++
		}
		res = append(res, n)
		time.Sleep(2 * time.Second)
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

// normalizeURL удаляет трекинговые параметры, фрагменты и приводит host/path к нижнему регистру
func normalizeURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		// попытка добавить схему
		u, err = url.Parse("https://" + raw)
		if err != nil {
			return strings.ToLower(strings.TrimSpace(raw))
		}
	}
	u.Fragment = ""
	// удаляем распространённые трекинговые параметры
	q := u.Query()
	for _, p := range []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "fbclid", "gclid"} {
		q.Del(p)
	}
	u.RawQuery = q.Encode()
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	u.Host = host
	// схлопываем дублирующие слеши и убираем завершающий слеш
	u.Path = strings.TrimRight(regexp.MustCompile(`/+`).ReplaceAllString(u.Path, "/"), "/")
	return u.Scheme + "://" + u.Host + u.Path + func() string {
		if u.RawQuery == "" {
			return ""
		}
		return "?" + u.RawQuery
	}()
}

// shingleSet возвращает k-грамные шинглы для строки s (нижний регистр, без пунктуации)
func shingleSet(s string, k int) map[string]struct{} {
	s = strings.ToLower(s)
	// оставляем только буквы/цифры/пробелы
	re := regexp.MustCompile(`[^[:alnum:]\s]+`)
	s = re.ReplaceAllString(s, " ")
	words := strings.Fields(s)
	out := make(map[string]struct{})
	if len(words) == 0 {
		return out
	}
	for i := 0; i <= len(words)-k; i++ {
		sh := strings.Join(words[i:i+k], " ")
		out[sh] = struct{}{}
	}
	// также включаем одиночные слова для коротких текстов
	if len(out) == 0 {
		for _, w := range words {
			out[w] = struct{}{}
		}
	}
	return out
}

// jaccardSimilarity между двумя строками используя k-грамные шинглы
func jaccardSimilarity(a, b string, k int) float64 {
	sa := shingleSet(a, k)
	sb := shingleSet(b, k)
	if len(sa) == 0 || len(sb) == 0 {
		return 0.0
	}
	inter := 0
	for sh := range sa {
		if _, ok := sb[sh]; ok {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 0.0
	}
	return float64(inter) / float64(union)
}

// isSimilarTitle возвращает true если заголовки являются близкими дубликатами (настраиваемый порог)
func isSimilarTitle(a, b string) bool {
	// используем 2-грамные шинглы для заголовков; порог = 0.55
	if a == "" || b == "" {
		return false
	}
	score := jaccardSimilarity(a, b, 2)
	return score >= 0.55
}

// selectDiverse выбирает до limit элементов из отсортированных candidates с ограничениями по источникам и категориям
// candidates ожидается отсортированным по score desc + recency
func selectDiverse(candidates []News, limit int, perSource int, perCategory int) []News {
	if limit <= 0 {
		return nil
	}
	out := make([]News, 0, limit)
	srcCount := make(map[string]int)
	catCount := make(map[string]int)

	// пробуем жадный выбор; если недостаточно, смягчаем квоты во втором проходе
	for _, c := range candidates {
		if len(out) >= limit {
			break
		}
		if c.Link == "" {
			continue
		}
		if perSource > 0 && srcCount[c.SourceName] >= perSource {
			continue
		}
		if perCategory > 0 && catCount[c.Category] >= perCategory {
			continue
		}
		out = append(out, c)
		srcCount[c.SourceName]++
		catCount[c.Category]++
	}

	// если не заполнили, заполняем игнорируя ограничения perSource/perCategory для достижения квоты
	if len(out) < limit {
		for _, c := range candidates {
			if len(out) >= limit {
				break
			}
			already := false
			for _, x := range out {
				if x.Link == c.Link {
					already = true
					break
				}
			}
			if already {
				continue
			}
			out = append(out, c)
		}
	}

	// сохраняем детерминированный порядок (score desc)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Published.After(out[j].Published)
	})
	return out
}
