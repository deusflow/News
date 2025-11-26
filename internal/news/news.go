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

	// === НОВЫЕ ПОЛЯ (VIBE) ===
	Mood string   // positive, negative, neutral, etc.
	Tags []string // ["Політика", "Данія"]
}

// ... [Здесь остаются твои списки ключевых слов: refugeeBoostKeywords, viborgKeywords и т.д.]
// Я сократил их для визуальной краткости здесь, но в свой файл КОПИРУЙ ВСЕ свои ключевые слова обратно,
// если они не в этом файле.
// Если ты скопируешь этот файл полностью, убедись, что переменные (refugeeBoostKeywords и т.д.)
// присутствуют.
// ВАЖНО: Ниже я привожу ПОЛНЫЙ код с ключевыми словами, чтобы ты мог просто скопировать.

var refugeeBoostKeywords = []string{"refugee", "viborg", "flygtning", "refugee visa", "temporary protection", "asylum", "families", "family"}
var viborgKeywords = []string{"viborg", "8800", "viborg kommune", "midtjylland"}
var visaBoostKeywords = []string{"visum", "visumforlængelse", "opholdstilladelse", "blive i EU"}
var ukraineGeoKeywords = []string{"ukraine", "ukraina", "ukrainer", "ukrainsk", "ukrainere", "flygtninge fra ukraine"}
var denmarkKeywords = []string{"danmark", "danske", "københavn", "aarhus", "viborg", "region", "kommune", "borgere", "lov", "politik", "økonomi", "visum", "arbejde", "bolig"}
var conflictKeywords = []string{"krig", "krigen", "putin", "zelensky", "invasion", "bomb", "missil", "russisk", "war"}
var techKeywords = []string{"teknologi", "innovation", "startup", "forskning", "IT", "cloud", "cyber", "data", "AI", "maskinlæring"}
var aiKeywords = []string{"ai", "artificial intelligence", "maskinlæring", "llm"}
var medicalKeywords = []string{"lægemidler", "medicin", "vaccine", "pharma", "biotek", "behandling"}
var excludeKeywords = []string{"vejr", "musik", "film", "kendis", "fodboldresultat", "sportsresultat", "tv-program", "horoskop", "madopskrift"}
var europeKeywords = []string{"europa", "eu", "european", "eu-lande"}
var youthKeywords = []string{"ungdom", "teenager", "unge", "skole", "gymnasium", "uddannelse", "studerende", "fritid", "sport", "gaming", "esport", "social media", "mobil", "app", "tiktok", "instagram", "mental sundhed", "stress"}
var parentKeywords = []string{"forældre", "børn", "familie", "dagpleje", "børnehave", "skole", "mor", "far", "graviditet", "fødsel", "opdragelse", "børnepenge", "barsel", "sundhedspleje"}
var culturalKeywords = []string{"kultur", "museum", "teater", "kunst", "udstilling", "litteratur", "bog", "festival", "koncert", "film", "design", "arkitektur"}
var sportsKeywords = []string{"sport", "fodbold", "håndbold", "cykling", "svømning", "fitness", "idræt", "konkurrence", "olympiske", "VM", "EM", "superliga"}

func containsAny(text string, keywords []string) bool {
	text = strings.ToLower(text)
	for _, k := range keywords {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if strings.Contains(k, " ") {
			if strings.Contains(text, k) {
				return true
			}
			continue
		}
		if len(k) <= 3 {
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(k) + `\b`)
			if re.MatchString(text) {
				return true
			}
			continue
		}
		if strings.Contains(text, k) {
			return true
		}
	}
	return false
}

func containsAll(text string, needles []string) bool {
	text = strings.ToLower(text)
	for _, k := range needles {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if len(k) <= 3 {
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(k) + `\b`)
			if !re.MatchString(text) {
				return false
			}
			continue
		}
		if !strings.Contains(text, k) {
			return false
		}
	}
	return true
}

func makeNewsKey(title, description string) string {
	h := sha1.New()
	h.Write([]byte(strings.ToLower(title + description)))
	return hex.EncodeToString(h.Sum(nil))
}

func makeSimilarityKey(item *rss.FeedItem) string {
	const (
		windowHours = 6
		maxWords    = 6
	)
	getHost := func(link string) string {
		if link == "" {
			return "unknown"
		}
		u, err := url.Parse(link)
		if err != nil || u.Host == "" {
			return "unknown"
		}
		return strings.ToLower(u.Host)
	}
	normalize := func(s string) string {
		s = strings.ToLower(s)
		reTags := regexp.MustCompile(`<[^>]*>`)
		s = reTags.ReplaceAllString(s, " ")
		var b []rune
		b = make([]rune, 0, len(s))
		for _, r := range s {
			if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSpace(r) {
				b = append(b, r)
			} else {
				b = append(b, ' ')
			}
		}
		return strings.Join(strings.Fields(string(b)), " ")
	}
	stopWords := map[string]bool{"a": true, "an": true, "the": true, "og": true, "i": true, "på": true, "til": true, "af": true, "med": true, "for": true, "er": true, "der": true, "om": true, "en": true, "et": true, "ikke": true}
	text := strings.TrimSpace(item.Title + " " + item.Description)
	norm := normalize(text)
	words := strings.Fields(norm)
	significant := make([]string, 0, len(words))
	for _, w := range words {
		if len(significant) >= maxWords {
			break
		}
		if stopWords[w] {
			continue
		}
		if len(w) <= 2 {
			continue
		}
		significant = append(significant, w)
	}
	if len(significant) == 0 && len(words) > 0 {
		for i := 0; i < len(words) && i < maxWords; i++ {
			significant = append(significant, words[i])
		}
	}
	var t time.Time
	if item.PublishedParsed != nil {
		t = *item.PublishedParsed
	} else {
		t = time.Now()
	}
	windowStart := t.Truncate(time.Duration(windowHours) * time.Hour).Unix()
	host := getHost(item.Link)
	key := fmt.Sprintf("%s|%s|%d", host, strings.Join(significant, "_"), windowStart)
	return key
}

func calculateNewsScore(item *rss.FeedItem) (string, int) {
	text := strings.ToLower(item.Title + " " + item.Description)
	if containsAny(text, excludeKeywords) {
		return "", 0
	}
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
	hasViborg := containsAny(text, viborgKeywords)
	ctxLocal := hasDenmark || hasUkraineGeo || hasEurope

	if hasConflict && !ctxLocal {
		return "", 0
	}

	var category string
	score := 0
	viborgBoost := 0
	if hasViborg {
		viborgBoost = 15
		if containsAll(text, []string{"viborg", "8800"}) {
			viborgBoost += 10
		}
	}

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
		score += viborgBoost
		return category, score
	}
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
		score += viborgBoost
		return category, score
	}
	if hasParent && ctxLocal {
		category = "family"
		score = 55 + viborgBoost
		if hasDenmark {
			score += 10
		}
		return category, score
	}
	if hasYouth && ctxLocal {
		category = "youth"
		score = 50 + viborgBoost
		if hasDenmark {
			score += 8
		}
		return category, score
	}
	if hasCultural && ctxLocal {
		category = "culture"
		score = 35 + viborgBoost
		if hasDenmark {
			score += 10
		}
		return category, score
	}
	if hasSports && ctxLocal {
		category = "sports"
		score = 30 + viborgBoost
		if hasDenmark {
			score += 8
		}
		return category, score
	}
	if hasDenmark {
		category = "denmark"
		score = 40 + viborgBoost
		if containsAny(text, []string{"politik", "regering", "økonomi", "minister"}) {
			score += 15
		}
		return category, score
	}
	if hasEurope {
		category = "europe"
		score = 25 + viborgBoost
		return category, score
	}
	if hasConflict {
		category = "conflict"
		score = 15 + viborgBoost
		return category, score
	}
	if containsAny(text, []string{"økonomi", "business", "marked", "aktier", "bank"}) {
		category = "economy"
		score = 20 + viborgBoost
	} else if containsAny(text, []string{"miljø", "klima", "climate", "environment", "grøn"}) {
		category = "environment"
		score = 25 + viborgBoost
	} else if containsAny(text, []string{"uddannelse", "education", "universitet"}) {
		category = "education"
		score = 22 + viborgBoost
	} else if containsAny(text, []string{"europa", "european", "eu"}) {
		category = "general"
		score = 10 + viborgBoost
	}

	if category == "" || score == 0 {
		return "", 0
	}
	return category, score
}

var aiClient *gemini.Client

func SetGeminiClient(c *gemini.Client) {
	aiClient = c
}

type Options struct {
	Limit                int
	MaxAge               time.Duration
	PerSource            int
	PerCategory          int
	MaxGeminiRequests    int
	ScrapeMaxArticles    int
	ScrapeConcurrency    int
	EnableImportanceLine bool
}

func FilterAndTranslate(items []*rss.FeedItem) ([]News, error) {
	return FilterAndTranslateWithOptions(items, Options{})
}

func FilterAndTranslateWithOptions(items []*rss.FeedItem, opts Options) ([]News, error) {
	startTime := time.Now()
	defer func() {
		metrics.Global.RecordProcessingTime(time.Since(startTime))
		metrics.Global.SetLastRun()
	}()

	if aiClient == nil {
		return nil, fmt.Errorf("gemini client not initialized")
	}
	log.Println("[Gemini] Starting filter + scrape + summarize pipeline")

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

	for _, item := range items {
		metrics.Global.IncrementNewsProcessed()
		if item.PublishedParsed != nil && time.Since(*item.PublishedParsed) > opts.MaxAge {
			continue
		}
		normalizedLink := normalizeURL(item.Link)
		if _, dup := seenLinks[normalizedLink]; dup {
			metrics.Global.IncrementDuplicatesFiltered()
			continue
		}
		seenLinks[normalizedLink] = struct{}{}
		key := makeNewsKey(item.Title, item.Description)
		if _, dup := seenContent[key]; dup {
			metrics.Global.IncrementDuplicatesFiltered()
			continue
		}
		seenContent[key] = struct{}{}
		similarKey := makeSimilarityKey(item)
		if _, dup := seenSimilar[similarKey]; dup {
			metrics.Global.IncrementDuplicatesFiltered()
			continue
		}
		seenSimilar[similarKey] = struct{}{}
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
			ImageURL:         extractImageURL(item),
			ImageAlt:         item.Title,
		})
		seenTitles = append(seenTitles, item.Title)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Published.After(candidates[j].Published)
	})

	if len(candidates) == 0 {
		return nil, nil
	}

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

	maxArticles := opts.ScrapeMaxArticles
	if maxArticles <= 0 {
		maxArticles = 10
	}
	concurrency := opts.ScrapeConcurrency
	if concurrency <= 0 {
		concurrency = 8
	}

	log.Printf("Извлекаем полный контент %d статей...", newsLimit)
	fullArticles := scraper.ExtractArticlesInBackgroundWithLimits(urls, maxArticles, concurrency)

	res := make([]News, 0, newsLimit)
	geminiRequests := 0
	for i := 0; i < newsLimit; i++ {
		n := diverseCandidates[i]
		if fa, ok := fullArticles[n.Link]; ok && len(fa.Content) > 200 {
			n.Content = fa.Content
		}
		sourceLang := "da"
		if n.SourceLang != "" {
			sourceLang = n.SourceLang
		}

		if opts.MaxGeminiRequests > 0 && geminiRequests >= opts.MaxGeminiRequests {
			log.Printf("⚠️ Gemini limit exceeded, using fallback")
			n.Summary = fallbackSummary(n.Content)
			n.SummaryDanish = fallbackSummary(n.Content)
			n.SummaryUkrainian = fallbackSummary(n.Content)
			n.Mood = "neutral" // Fallback Mood
		} else {
			aiResp, err := aiClient.TranslateAndSummarizeNews(n.Title, n.Content)
			if err != nil {
				log.Printf("⚠️ Gemini failed: %v", err)
				n.Summary = fallbackSummary(n.Content)
				n.SummaryUkrainian = fallbackSummary(n.Content)
				n.SummaryDanish = fallbackSummary(n.Content)
				if ukTitle, err := translate.TranslateText(n.Title, sourceLang, "uk"); err == nil {
					n.TitleUkrainian = ukTitle
				}
				n.Mood = "neutral"
			} else {
				n.Summary = aiResp.Summary
				n.SummaryDanish = aiResp.Danish
				n.SummaryUkrainian = aiResp.Ukrainian
				// === ЗАПИСЫВАЕМ НОВЫЕ ДАННЫЕ ===
				n.Mood = aiResp.Mood
				n.Tags = aiResp.Tags

				if ukTitle, err := translate.TranslateText(n.Title, sourceLang, "uk"); err == nil && strings.TrimSpace(ukTitle) != "" {
					n.TitleUkrainian = ukTitle
				}
			}
			geminiRequests++
		}
		// Importance generation... (оставляем как было)
		if opts.EnableImportanceLine {
			if impDa, err := translate.ImportanceLine(n.Content, "da"); err == nil && strings.TrimSpace(impDa) != "" {
				n.ImportanceDanish = impDa
			}
			if impUk, err := translate.ImportanceLine(n.Content, "uk"); err == nil && strings.TrimSpace(impUk) != "" {
				n.ImportanceUkrainian = impUk
			}
		}
		res = append(res, n)
		time.Sleep(1 * time.Second)
	}
	res = postProcessNews(res, configFromEnv())
	return res, nil
}

func fallbackSummary(content string) string {
	c := strings.TrimSpace(content)
	if c == "" {
		return ""
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

// === ВСПОМОГАТЕЛЬНАЯ ФУНКЦИЯ ДЛЯ EMOJI ===
func GetMoodEmoji(mood string) string {
	switch strings.ToLower(mood) {
	case "positive":
		return "🟢" // Зеленый круг (хорошо)
	case "negative":
		return "🔴" // Красный круг (плохо)
	case "shocking":
		return "⚡" // Молния (шок/срочно)
	case "urgent":
		return "🚨" // Сирена (важно/опасно)
	default:
		return "⚪" // Белый круг (нейтрально)
	}
}

// FormatNews produces concise formatted output with summaries.
func FormatNews(n News) string {
	// Добавляем Mood Emoji в заголовок
	emoji := GetMoodEmoji(n.Mood)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 🇩🇰 *%s*\n", emoji, n.Title))

	// Добавляем теги
	if len(n.Tags) > 0 {
		tags := make([]string, len(n.Tags))
		for i, t := range n.Tags {
			tags[i] = "#" + strings.ReplaceAll(t, " ", "_")
		}
		b.WriteString(strings.Join(tags, " ") + "\n\n")
	}

	if n.ImportanceUkrainian != "" {
		b.WriteString("🔥 🇺🇦 " + n.ImportanceUkrainian + "\n")
	}
	if n.SummaryUkrainian != "" {
		b.WriteString("🇺🇦 " + n.SummaryUkrainian + "\n")
	}
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━")
	return b.String()
}

// FormatNewsWithImage - Текстовый режим (с превью ссылки)
func FormatNewsWithImage(n News, minSentencesPerLang, maxSentencesPerLang int) string {
	if minSentencesPerLang <= 0 {
		minSentencesPerLang = 2
	}
	if maxSentencesPerLang < minSentencesPerLang {
		maxSentencesPerLang = minSentencesPerLang
	}
	useSentences := maxSentencesPerLang

	var b strings.Builder
	// Хедер с Vibe
	moodEmoji := GetMoodEmoji(n.Mood)
	b.WriteString(fmt.Sprintf("%s 🇩🇰 <b>Danish News</b> 🇺🇦\n", moodEmoji))

	// Теги сразу под заголовком
	if len(n.Tags) > 0 {
		tags := make([]string, len(n.Tags))
		for i, t := range n.Tags {
			tags[i] = "#" + strings.ReplaceAll(t, " ", "_")
		}
		b.WriteString("<i>" + strings.Join(tags, " ") + "</i>\n\n")
	} else {
		b.WriteString("\n")
	}

	// Датский блок
	daTitle := n.Title
	daText := strings.TrimSpace(n.SummaryDanish)
	if daText == "" {
		daText = fallbackSummary(n.Content)
	}
	daText = condenseSummary(daText, useSentences)
	if daTitle != "" {
		b.WriteString("🇩🇰 <b>" + daTitle + "</b>\n")
	}
	if n.ImportanceDanish != "" {
		b.WriteString("🔥 " + n.ImportanceDanish + "\n")
	}
	if daText != "" {
		b.WriteString(daText + "\n\n")
	}

	// Украинский блок
	ukTitle := strings.TrimSpace(n.TitleUkrainian)
	if ukTitle == "" {
		ukTitle = n.Title
	}
	ukText := strings.TrimSpace(n.SummaryUkrainian)
	if ukText == "" {
		ukText = fallbackSummary(n.Content)
	}
	ukText = condenseSummary(ukText, useSentences)
	if ukTitle != "" {
		b.WriteString("🇺🇦 <b>" + ukTitle + "</b>\n")
	}
	if n.ImportanceUkrainian != "" {
		b.WriteString("🔥 " + n.ImportanceUkrainian + "\n")
	}
	if ukText != "" {
		b.WriteString(ukText + "\n")
	}

	if strings.TrimSpace(n.Link) != "" {
		b.WriteString("\n🔗 " + n.Link)
	}

	return b.String()
}

func trimToWordBoundary(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	cutRunes := runes[:max]
	cutStr := string(cutRunes)
	if i := strings.LastIndex(cutStr, " "); i >= 0 && utf8.RuneCountInString(cutStr)-utf8.RuneCountInString(cutStr[:i]) <= 50 {
		cutStr = strings.TrimSpace(cutStr[:i])
	} else {
		cutStr = strings.TrimSpace(cutStr)
	}
	if cutStr == "" {
		return string(cutRunes)
	}
	return cutStr + "..."
}

// FormatCaptionForPhoto - Режим фото
func FormatCaptionForPhoto(n News, maxLen int, sentencesPerLang int, minPerLang int) string {
	if maxLen <= 0 || maxLen > 1024 {
		maxLen = 1024
	}
	if sentencesPerLang <= 0 {
		sentencesPerLang = 2
	}
	if minPerLang <= 0 {
		minPerLang = 120
	}
	daTitle := strings.TrimSpace(n.Title)
	ukTitle := strings.TrimSpace(n.TitleUkrainian)
	if ukTitle == "" {
		ukTitle = daTitle
	}
	daSum := strings.TrimSpace(n.SummaryDanish)
	if daSum == "" {
		daSum = fallbackSummary(n.Content)
	}
	ukSum := strings.TrimSpace(n.SummaryUkrainian)
	if ukSum == "" {
		ukSum = fallbackSummary(n.Content)
	}
	daSum = condenseSummary(daSum, sentencesPerLang)
	ukSum = condenseSummary(ukSum, sentencesPerLang)

	// Добавляем Vibe
	moodEmoji := GetMoodEmoji(n.Mood)
	header := fmt.Sprintf("%s 🇩🇰 Danish News 🇺🇦\n", moodEmoji)

	// Теги (добавляем, если влезут, пока просто формируем строку)
	tagsStr := ""
	if len(n.Tags) > 0 {
		t := make([]string, len(n.Tags))
		for i, tag := range n.Tags {
			t[i] = "#" + strings.ReplaceAll(tag, " ", "_")
		}
		tagsStr = strings.Join(t, " ") + "\n\n"
	} else {
		tagsStr = "\n"
	}
	header += tagsStr

	footer := ""
	impBlock := ""
	if n.ImportanceDanish != "" {
		impBlock += "🔥 🇩🇰 " + n.ImportanceDanish + "\n"
	}
	if n.ImportanceUkrainian != "" {
		impBlock += "🔥 🇺🇦 " + n.ImportanceUkrainian + "\n"
	}
	if impBlock != "" {
		impBlock += "\n"
	}

	composeBase := func(daT, ukT string) string {
		var b strings.Builder
		b.WriteString(header)
		b.WriteString(impBlock)
		b.WriteString("🇩🇰 <b>" + daT + "</b>\n")
		b.WriteString("%DA%\n\n")
		b.WriteString("🇺🇦 <b>" + ukT + "</b>\n")
		b.WriteString("%UK%")
		b.WriteString(footer)
		return b.String()
	}

	capStr := composeBase(daTitle, ukTitle)
	baseLen := utf8.RuneCountInString(strings.ReplaceAll(strings.ReplaceAll(capStr, "%DA%", ""), "%UK%", ""))

	if baseLen >= maxLen-40 {
		roomForTitles := maxLen - utf8.RuneCountInString(header) - utf8.RuneCountInString(footer) - 8 - 40
		if roomForTitles < 20 {
			roomForTitles = 20
		}
		each := roomForTitles / 2
		daTitle = trimToWordBoundary(daTitle, each)
		ukTitle = trimToWordBoundary(ukTitle, each)
		capStr = composeBase(daTitle, ukTitle)
		baseLen = utf8.RuneCountInString(strings.ReplaceAll(strings.ReplaceAll(capStr, "%DA%", ""), "%UK%", ""))
	}

	available := maxLen - baseLen
	if available < 40 {
		available = 40
	}
	minFloor := minPerLang
	if minFloor > available/2 {
		minFloor = available / 2
	}
	rem := available - 2*minFloor
	if rem < 0 {
		rem = 0
	}
	daLen := utf8.RuneCountInString(daSum)
	ukLen := utf8.RuneCountInString(ukSum)
	totalLen := daLen + ukLen
	var daBudget, ukBudget int
	if totalLen > 0 && rem > 0 {
		daBudget = minFloor + rem*daLen/totalLen
		ukBudget = minFloor + rem*ukLen/totalLen
	} else {
		daBudget = available / 2
		ukBudget = available - daBudget
	}

	daSum = trimToWordBoundary(daSum, daBudget)
	ukSum = trimToWordBoundary(ukSum, ukBudget)

	caption := strings.Replace(capStr, "%DA%", daSum, 1)
	caption = strings.Replace(caption, "%UK%", ukSum, 1)

	if utf8.RuneCountInString(caption) > maxLen {
		r := []rune(caption)
		caption = string(r[:maxLen-1]) + "…"
	}
	return caption
}

// ShouldUsePhoto - проверка, стоит ли использовать фото (с учетом места под текст)
func ShouldUsePhoto(n News, maxLen int, sentencesPerLang int, minPerLang int, minTotal int) bool {
	if maxLen <= 0 || maxLen > 1024 {
		maxLen = 1024
	}
	if sentencesPerLang <= 0 {
		sentencesPerLang = 2
	}
	if minPerLang <= 0 {
		minPerLang = 120
	}
	if minTotal <= 0 {
		minTotal = 180
	}
	daTitle := strings.TrimSpace(n.Title)
	ukTitle := strings.TrimSpace(n.TitleUkrainian)
	if ukTitle == "" {
		ukTitle = daTitle
	}
	daSum := strings.TrimSpace(n.SummaryDanish)
	if daSum == "" {
		daSum = fallbackSummary(n.Content)
	}
	ukSum := strings.TrimSpace(n.SummaryUkrainian)
	if ukSum == "" {
		ukSum = fallbackSummary(n.Content)
	}
	if daSum == "" || ukSum == "" {
		return false
	}
	daSum = condenseSummary(daSum, sentencesPerLang)
	ukSum = condenseSummary(ukSum, sentencesPerLang)
	if daSum == "" || ukSum == "" {
		return false
	}
	if utf8.RuneCountInString(daSum)+utf8.RuneCountInString(ukSum) < minTotal {
		return false
	}
	moodEmoji := GetMoodEmoji(n.Mood)
	header := fmt.Sprintf("%s 🇩🇰 Danish News 🇺🇦\n\n", moodEmoji)

	composeBase := func(daT, ukT string) string {
		var b strings.Builder
		b.WriteString(header)
		b.WriteString("🇩🇰 <b>" + daT + "</b>\n")
		b.WriteString("%DA%\n\n")
		b.WriteString("🇺🇦 <b>" + ukT + "</b>\n")
		b.WriteString("%UK%")
		return b.String()
	}
	capStr := composeBase(daTitle, ukTitle)
	baseLen := utf8.RuneCountInString(strings.ReplaceAll(strings.ReplaceAll(capStr, "%DA%", ""), "%UK%", ""))
	if baseLen >= maxLen-40 {
		roomForTitles := maxLen - utf8.RuneCountInString(header) - 8 - 40
		if roomForTitles < 20 {
			roomForTitles = 20
		}
		each := roomForTitles / 2
		daTitle = trimToWordBoundary(daTitle, each)
		ukTitle = trimToWordBoundary(ukTitle, each)
		capStr = composeBase(daTitle, ukTitle)
		baseLen = utf8.RuneCountInString(strings.ReplaceAll(strings.ReplaceAll(capStr, "%DA%", ""), "%UK%", ""))
	}
	available := maxLen - baseLen
	if available < 40 {
		return false
	}
	minFloor := minPerLang
	if minFloor > available/2 {
		minFloor = available / 2
	}
	rem := available - 2*minFloor
	if rem < 0 {
		rem = 0
	}
	daLen := utf8.RuneCountInString(daSum)
	ukLen := utf8.RuneCountInString(ukSum)
	totalLen := daLen + ukLen
	var daBudget, ukBudget int
	if totalLen > 0 && rem > 0 {
		daBudget = minFloor + rem*daLen/totalLen
		ukBudget = minFloor + rem*ukLen/totalLen
	} else {
		daBudget = available / 2
		ukBudget = available - daBudget
	}
	if daBudget < minPerLang || ukBudget < minPerLang {
		return false
	}
	return true
}

func condenseSummary(s string, maxSentences int) string {
	s = strings.TrimSpace(s)
	if s == "" || maxSentences <= 0 {
		return s
	}
	seps := []rune{'.', '!', '?'}
	var sentences []string
	var cur []rune
	for _, r := range []rune(s) {
		cur = append(cur, r)
		for _, sep := range seps {
			if r == sep {
				str := strings.TrimSpace(string(cur))
				if len([]rune(str)) >= 15 {
					sentences = append(sentences, str)
				}
				cur = cur[:0]
				break
			}
		}
		if len(sentences) >= maxSentences {
			break
		}
	}
	if len(sentences) == 0 {
		parts := strings.Split(s, ".")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			sentences = append(sentences, p+".")
			if len(sentences) >= maxSentences {
				break
			}
		}
	}
	res := strings.Join(sentences, " ")
	return strings.TrimSpace(res)
}

func normalizeURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		u, err = url.Parse("https://" + raw)
		if err != nil {
			return strings.ToLower(strings.TrimSpace(raw))
		}
	}
	u.Fragment = ""
	q := u.Query()
	for _, p := range []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "fbclid", "gclid"} {
		q.Del(p)
	}
	u.RawQuery = q.Encode()
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	u.Host = host
	u.Path = strings.TrimRight(regexp.MustCompile(`/+`).ReplaceAllString(u.Path, "/"), "/")
	return u.Scheme + "://" + u.Host + u.Path + func() string {
		if u.RawQuery == "" {
			return ""
		}
		return "?" + u.RawQuery
	}()
}

func shingleSet(s string, k int) map[string]struct{} {
	s = strings.ToLower(s)
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
	if len(out) == 0 {
		for _, w := range words {
			out[w] = struct{}{}
		}
	}
	return out
}

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

func isSimilarTitle(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	score := jaccardSimilarity(a, b, 2)
	return score >= 0.55
}

func selectDiverse(candidates []News, limit int, perSource int, perCategory int) []News {
	if limit <= 0 {
		return nil
	}
	out := make([]News, 0, limit)
	srcCount := make(map[string]int)
	catCount := make(map[string]int)
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
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Published.After(out[j].Published)
	})
	return out
}

func extractImageURL(item *rss.FeedItem) string {
	if item.Enclosures != nil {
		for _, e := range item.Enclosures {
			if e == nil {
				continue
			}
			if strings.HasPrefix(strings.ToLower(e.Type), "image/") && strings.TrimSpace(e.URL) != "" {
				return e.URL
			}
			if strings.TrimSpace(e.URL) != "" && (strings.HasSuffix(strings.ToLower(e.URL), ".jpg") || strings.HasSuffix(strings.ToLower(e.URL), ".jpeg") || strings.HasSuffix(strings.ToLower(e.URL), ".png") || strings.HasSuffix(strings.ToLower(e.URL), ".webp") || strings.HasSuffix(strings.ToLower(e.URL), ".gif")) {
				return e.URL
			}
		}
	}
	if item.Description != "" {
		if u := findFirstImgURL(item.Description); u != "" {
			return u
		}
	}
	if item.Content != "" {
		if u := findFirstImgURL(item.Content); u != "" {
			return u
		}
	}
	if strings.TrimSpace(item.Link) != "" {
		if og, err := scraper.ExtractImageURL(item.Link); err == nil && strings.TrimSpace(og) != "" {
			return og
		}
	}
	return ""
}

func findFirstImgURL(html string) string {
	reSrcset := regexp.MustCompile(`(?i)<img[^>]+srcset=["']([^"']+)["'][^>]*>`)
	if m := reSrcset.FindStringSubmatch(html); len(m) > 1 {
		parts := strings.Split(m[1], ",")
		if len(parts) > 0 {
			first := strings.TrimSpace(parts[0])
			u := strings.Fields(first)
			if len(u) > 0 {
				return u[0]
			}
		}
	}
	reData := regexp.MustCompile(`(?i)<img[^>]+data-src=["']([^"']+)["'][^>]*>`)
	if m := reData.FindStringSubmatch(html); len(m) > 1 {
		return m[1]
	}
	reSrc := regexp.MustCompile(`(?i)<img[^>]+src=["']([^"']+)["'][^>]*>`)
	if m := reSrc.FindStringSubmatch(html); len(m) > 1 {
		return m[1]
	}
	return ""
}

func postProcessNews(list []News, cfg *config.Config) []News {
	for i := range list {
		n := &list[i]
		if cfg.ImportanceTopN > 0 && i >= cfg.ImportanceTopN {
			n.ImportanceDanish = ""
			n.ImportanceUkrainian = ""
		}
		if isNearDuplicate(n.ImportanceDanish, n.SummaryDanish) {
			n.ImportanceDanish = ""
		}
		if isNearDuplicate(n.ImportanceUkrainian, n.SummaryUkrainian) {
			n.ImportanceUkrainian = ""
		}
		if n.ImportanceUkrainian != "" && !containsCyrillic(n.ImportanceUkrainian) && containsCyrillic(n.SummaryUkrainian) {
			n.ImportanceUkrainian = ""
		}
		if n.ImportanceDanish != "" && containsCyrillic(n.ImportanceDanish) && !containsCyrillic(n.SummaryDanish) {
			n.ImportanceDanish = ""
		}
	}
	return list
}

func containsCyrillic(s string) bool {
	for _, r := range s {
		if r >= 0x0400 && r <= 0x04FF {
			return true
		}
	}
	return false
}

func isNearDuplicate(a, b string) bool {
	a = strings.TrimSpace(strings.ToLower(a))
	b = strings.TrimSpace(strings.ToLower(b))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if strings.Contains(a, b) || strings.Contains(b, a) {
		return true
	}
	tA := strings.Fields(a)
	tB := strings.Fields(b)
	if len(tA) == 0 || len(tB) == 0 {
		return false
	}
	setA := map[string]struct{}{}
	setB := map[string]struct{}{}
	for _, t := range tA {
		setA[t] = struct{}{}
	}
	for _, t := range tB {
		setB[t] = struct{}{}
	}
	inter := 0
	for t := range setA {
		if _, ok := setB[t]; ok {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return false
	}
	j := float64(inter) / float64(union)
	return j >= 0.85
}

func configFromEnv() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		return &config.Config{}
	}
	return cfg
}
