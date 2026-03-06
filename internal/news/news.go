package news

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/scraper"
)

// News - основная структура новости
type News struct {
	Title     string
	Content   string
	Link      string
	Published time.Time
	Category  string
	Score     int

	// KeywordScore and ImpactScore are explicit ranking signals.
	// Score remains the final combined value used for ordering.
	KeywordScore int
	ImpactScore  int

	SourceName string
	SourceLang string

	Summary          string
	SummaryDanish    string
	SummaryUkrainian string
	TitleUkrainian   string
	Mood             string
	Tags             []string
	TLDR             string
	FunFact          string

	ImageURL string
	ImageAlt string

	// Legacy поля для совместимости
	ImportanceDanish    string
	ImportanceUkrainian string
}

type Options struct {
	MaxAge            time.Duration
	PerSource         int
	ScrapeMaxArticles int
	ScrapeConcurrency int

	// ГЛАВНОЕ ИЗМЕНЕНИЕ: Используем интерфейс, а не конкретные клиенты
	AI       ai.Provider
	Config   *config.Config
	Metrics  *metrics.Metrics
	Keywords *config.KeywordsConfig
}

// scrapedItem хранит результат первого этапа — параллельного скрейпинга.
// AI-обработка происходит только на втором этапе, строго последовательно.
type scrapedItem struct {
	index          int
	item           *rss.FeedItem
	content        string // итоговый контент: scraper → description → ""
	scrapeImageURL string // og:image из скрапера (может быть "")
}

func FilterAndTranslateWithOptions(ctx context.Context, items []*rss.FeedItem, opts Options) ([]News, error) {
	logger.Info("starting news fetch cycle")
	logger.Info("received raw items from RSS", "count", len(items))

	const minKeywordScoreForAI = 1

	// ── Шаг 1: фильтрация по дате ────────────────────────────────────────────
	var recent []*rss.FeedItem
	cutoff := time.Now().Add(-opts.MaxAge)
	for _, item := range items {
		if item.PublishedParsed != nil && item.PublishedParsed.After(cutoff) {
			recent = append(recent, item)
		}
	}

	// ── Шаг 2: дедупликация ссылок ───────────────────────────────────────────
	unique := make(map[string]*rss.FeedItem)
	for _, item := range recent {
		unique[item.Link] = item
	}
	candidates := make([]*rss.FeedItem, 0, len(unique))
	for _, item := range unique {
		candidates = append(candidates, item)
	}

	// Сортируем: свежие сначала; при равной свежести — выше приоритет источника.
	sort.Slice(candidates, func(i, j int) bool {
		ti, tj := candidates[i].PublishedParsed, candidates[j].PublishedParsed
		if ti == nil || tj == nil {
			return false
		}
		if !ti.Equal(*tj) {
			return ti.After(*tj)
		}
		return candidates[i].Source.Priority > candidates[j].Source.Priority
	})
	logger.Info("unique items after dedup", "count", len(candidates))

	// ── Шаг 2б: лимит новостей с одного источника ────────────────────────────
	if opts.PerSource > 0 {
		sourceCounts := make(map[string]int, len(candidates))
		filtered := candidates[:0]
		for _, item := range candidates {
			name := item.Source.Name
			if sourceCounts[name] < opts.PerSource {
				filtered = append(filtered, item)
				sourceCounts[name]++
			}
		}
		if len(filtered) < len(candidates) {
			logger.Info("PerSource limit applied",
				"per_source", opts.PerSource,
				"before", len(candidates),
				"after", len(filtered))
		}
		candidates = filtered
	}

	// ── Шаг 3: KEYWORD PRE-SCORING — дешёвая фильтрация ДО AI ───────────────
	//
	// Ключевые слова — ГЛАВНЫЙ критерий релевантности.
	// AI — дополнительная оценка (перевод + точная категоризация).
	//
	// Зачем: AI-вызов стоит ~7-16 сек на новость (Gemini Free Tier).
	// Вызывать AI для всех 32 кандидатов — 3-8 минут + расход RPD лимита.
	// Вместо этого: предварительно оцениваем ВСЕ кандидаты ключевыми словами
	// (мгновенно), отсекаем мусор (score < 0), и отправляем в AI только
	// топовых кандидатов (maxAICandidates).
	//
	// Количество AI-кандидатов: берём больше чем нужно для публикации,
	// потому что AI может отвергнуть некоторые (пустой контент, валидация).
	// 5 кандидатов достаточно, чтобы гарантировать хотя бы 1 качественную новость.
	const maxAICandidates = 5

	type preScored struct {
		item              *rss.FeedItem
		kwScore           int
		kwCat             string
		kwCategoryWeights map[string]int
	}

	var scored []preScored
	for _, item := range candidates {
		kwScore := 0
		kwCat := ""
		kwCategoryWeights := map[string]int{}
		if opts.Keywords != nil {
			// Pre-score по title + RSS description (контент ещё не скрейпнут)
			text := item.Title
			if item.Description != "" {
				text += " " + item.Description
			}
			kwScore, kwCat, kwCategoryWeights = opts.Keywords.CalculateScoreDetailed(text)
		}
		scored = append(scored, preScored{
			item:              item,
			kwScore:           kwScore,
			kwCat:             kwCat,
			kwCategoryWeights: kwCategoryWeights,
		})
	}

	// Сортируем по keyword score (лучшие первые)
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].kwScore > scored[j].kwScore
	})

	// Отсекаем явный мусор (score < 0) и берём топ maxAICandidates
	var topCandidates []preScored
	for _, s := range scored {
		if opts.Keywords != nil && s.kwScore < minKeywordScoreForAI {
			logger.Info("pre-filter: skipping non-relevant keyword score",
				"title", s.item.Title, "kw_score", s.kwScore)
			continue
		}
		topCandidates = append(topCandidates, s)
		if len(topCandidates) >= maxAICandidates {
			break
		}
	}

	logger.Info("keyword pre-filter done",
		"total_candidates", len(candidates),
		"passed_filter", len(topCandidates),
		"rejected", len(candidates)-len(topCandidates))

	if len(topCandidates) == 0 {
		logger.Info("no candidates passed keyword pre-filter")
		return nil, nil
	}

	// ── Шаг 4: параллельный скрейпинг (ТОЛЬКО HTTP, без AI) ──────────────────
	concurrency := opts.ScrapeConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	logger.Info("phase 1: parallel scraping", "workers", concurrency)

	type scrapeJob struct {
		index int
		item  *rss.FeedItem
	}
	type scrapeResult struct {
		index          int
		item           *rss.FeedItem
		content        string
		scrapeImageURL string
	}

	scrapeJobs := make(chan scrapeJob, len(topCandidates))
	scrapeResults := make(chan scrapeResult, len(topCandidates))

	var scrapeWg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		scrapeWg.Add(1)
		go func() {
			defer scrapeWg.Done()
			for j := range scrapeJobs {
				if ctx.Err() != nil {
					scrapeResults <- scrapeResult{index: j.index, item: j.item, content: ""}
					continue
				}

				scrapeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				ac, err := scraper.ExtractFullArticle(scrapeCtx, j.item.Link)
				cancel()

				content := ""
				imageURL := ""
				switch {
				case err == nil && ac != nil && ac.Content != "":
					content = ac.Content
					imageURL = ac.ImageURL
				case j.item.Description != "":
					content = j.item.Description
					logger.Warn("scrape failed, using RSS description", "url", j.item.Link, "error", err)
				default:
					logger.Warn("no content for item", "url", j.item.Link, "error", err)
				}

				scrapeResults <- scrapeResult{index: j.index, item: j.item, content: content, scrapeImageURL: imageURL}
			}
		}()
	}

	for i, tc := range topCandidates {
		scrapeJobs <- scrapeJob{index: i, item: tc.item}
	}
	close(scrapeJobs)

	scrapeWg.Wait()
	close(scrapeResults)

	scrapeMap := make(map[int]scrapeResult, len(topCandidates))
	for r := range scrapeResults {
		scrapeMap[r.index] = r
	}
	scraped := make([]scrapedItem, 0, len(scrapeMap))
	for i := 0; i < len(topCandidates); i++ {
		if r, ok := scrapeMap[i]; ok {
			scraped = append(scraped, scrapedItem{
				index:          i,
				item:           r.item,
				content:        r.content,
				scrapeImageURL: r.scrapeImageURL,
			})
		}
	}
	logger.Info("phase 1 done", "scraped", len(scraped))

	// ── Шаг 5: последовательные AI-вызовы ────────────────────────────────────
	//
	// AI вызывается ТОЛЬКО для topCandidates (max 5), не для всех 32.
	// Это экономит 80%+ AI-лимита.
	logger.Info("phase 2: sequential AI processing", "items", len(scraped))

	var result []News
	aiErrors := 0

	for idx, s := range scraped {
		if ctx.Err() != nil {
			logger.Warn("context cancelled, stopping early", "completed", len(result)+aiErrors)
			break
		}

		n, err := processItemWithContent(ctx, s.item, s.index, s.content, s.scrapeImageURL, opts)
		if err != nil {
			logger.Error("AI failed for item", "index", s.index+1, "title", s.item.Title, "error", err)
			aiErrors++
			continue
		}
		if n != nil {
			kwScore := topCandidates[idx].kwScore
			kwCat := topCandidates[idx].kwCat
			kwCategoryWeights := topCandidates[idx].kwCategoryWeights

			// Re-score on full text (title + scraped content) for final ranking precision.
			// Pre-score still controls the cheap pre-filter before AI.
			if opts.Keywords != nil {
				fullText := s.item.Title
				if s.content != "" {
					fullText += " " + s.content
				}
				fullKwScore, fullKwCat, fullKwWeights := opts.Keywords.CalculateScoreDetailed(fullText)
				if fullKwScore > kwScore {
					kwScore = fullKwScore
					kwCat = fullKwCat
					kwCategoryWeights = fullKwWeights
				}
			}

			impactScore := calculateImpactScore(kwCategoryWeights)

			// Keywords remain primary. Impact is a separate architectural signal.
			// AI (mood/freshness) stays secondary via n.Score from processItemWithContent().
			n.KeywordScore = kwScore
			n.ImpactScore = impactScore
			n.Score += kwScore + impactScore

			// If keywords found a category, prefer it after strict normalization.
			if kwCat != "" && kwCat != "spam" {
				if coerced, ok := CoerceCategory(kwCat); ok {
					n.Category = string(coerced)
				}
			}
			result = append(result, *n)
		}
	}

	logger.Info("phase 2 done", "ready", len(result), "errors", aiErrors)

	// ── Шаг 6: финальная сортировка с impact-priority ─────────────────────────
	// 1) Сильные public-impact новости идут выше остальных.
	// 2) Внутри группы порядок по final score.
	sortByPublishPriority(result)

	return result, nil
}

const impactPriorityThreshold = 12

// calculateImpactScore derives an explicit public-impact signal from keyword
// category contributions. It is intentionally independent from AI.
func calculateImpactScore(categoryWeights map[string]int) int {
	if len(categoryWeights) == 0 {
		return 0
	}

	impact := 0
	impact += categoryWeights["politics"]
	impact += categoryWeights["economy"] / 2
	impact += categoryWeights["society"] / 2
	impact += categoryWeights["visas"] / 2
	impact += categoryWeights["work"] / 2
	impact += categoryWeights["money"] / 2
	impact += categoryWeights["housing"] / 2
	impact += categoryWeights["health"] / 2
	impact += categoryWeights["transport"] / 2

	// Entertainment-only items get a small penalty when no public-impact signal exists.
	if impact == 0 {
		light := categoryWeights["lifestyle"] + categoryWeights["sport"]
		if light > 0 {
			impact -= min(6, light/3)
		}
	}

	if impact > 40 {
		impact = 40
	}
	if impact < -10 {
		impact = -10
	}
	return impact
}

func isImpactCandidate(n News) bool {
	return n.ImpactScore >= impactPriorityThreshold
}

func sortByPublishPriority(items []News) {
	sort.SliceStable(items, func(i, j int) bool {
		iImpact := isImpactCandidate(items[i])
		jImpact := isImpactCandidate(items[j])
		if iImpact != jImpact {
			return iImpact
		}
		if iImpact && items[i].ImpactScore != items[j].ImpactScore {
			return items[i].ImpactScore > items[j].ImpactScore
		}
		return items[i].Score > items[j].Score
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Веса для mood-based fallback scoring.
// Используется ТОЛЬКО когда Keywords config недоступен.
// При наличии Keywords — scoring идёт через CalculateScore() из YAML.
const (
	scoreMoodUrgent   = 50 // Срочные новости — максимальный приоритет
	scoreMoodShocking = 30 // Шокирующие новости — высокий приоритет
	scoreMoodNegative = 20 // Негативные новости важнее позитивных (влияют на жизнь)
	scoreMoodPositive = 10 // Позитивные новости — базовый приоритет
	// neutral = 0 — нейтральные не получают бонуса, но и не фильтруются
	scoreFreshNews  = 10 // Бонус за свежесть: опубликована менее чем scoreFreshHours назад
	scoreFreshHours = 4  // Порог "свежей" новости в часах
)

// calculateScore — fallback scoring по mood и свежести.
// Применяется только если Keywords config не задан.
// Картинка намеренно НЕ влияет на score — наличие фото не делает новость важнее.
func calculateScore(n News) int {
	score := 0
	switch n.Mood { // Mood уже нормализован в Validate() → всегда lowercase
	case "urgent":
		score += scoreMoodUrgent
	case "shocking":
		score += scoreMoodShocking
	case "negative":
		score += scoreMoodNegative
	case "positive":
		score += scoreMoodPositive
		// "neutral" → 0, без бонуса
	}
	if time.Since(n.Published).Hours() < scoreFreshHours {
		score += scoreFreshNews
	}
	return score
}

// processItemWithContent вызывается на втором этапе (строго последовательно).
// Контент и картинка уже получены на этапе параллельного скрейпинга — здесь только AI + маппинг.
func processItemWithContent(ctx context.Context, item *rss.FeedItem, index int, content, scrapeImageURL string, opts Options) (*News, error) {
	title := item.Title
	link := item.Link
	logger.Info("AI processing item", "index", index+1, "title", title)

	// HIGH-3 guard: если контент пустой и заголовок слишком короткий —
	// AI получит практически нулевой input и начнёт галлюцинировать.
	// Минимальный полезный заголовок: 30 символов (~5-6 слов на датском).
	const minTitleLen = 30
	if content == "" && len([]rune(strings.TrimSpace(title))) < minTitleLen {
		logger.Warn("skipping item — no content and title too short",
			"index", index+1,
			"title_len", len([]rune(strings.TrimSpace(title))),
			"title", title)
		return nil, nil
	}
	if content == "" {
		logger.Warn("no scraped content, AI works from title only", "index", index+1, "title", title)
	}

	prompt := GenerateNewsPrompt(title, content)

	resp, aiErr := opts.AI.Generate(ctx, title, content, prompt)
	if aiErr != nil {
		logger.Error("all AI providers failed", "title", title, "error", aiErr)
		return nil, aiErr
	}

	// Валидация AI ответа: проверяем обязательные поля и нормализуем mood
	if err := resp.Validate(); err != nil {
		logger.Error("AI response validation failed", "title", title, "error", err)
		return nil, err
	}

	published := time.Now()
	if item.PublishedParsed != nil {
		published = *item.PublishedParsed
	}

	// Категорию из AI валидируем через whitelist/aliases — галлюцинации не пройдут.
	aiCategory, aiCategoryValid := CoerceCategory(resp.Category)
	if !aiCategoryValid {
		logger.Warn("AI returned unknown category, using default",
			"title", title,
			"raw_category", resp.Category,
			"fallback", CategoryDefault)
	}

	n := News{
		Title:      title,
		Link:       link,
		Published:  published,
		Category:   string(aiCategory),
		SourceName: item.Source.Name,
		SourceLang: item.Source.Lang,
		Content:    content,

		Summary:          resp.Summary,
		SummaryDanish:    resp.Danish,
		SummaryUkrainian: resp.Ukrainian,
		TitleUkrainian:   resp.TitleUkrainian,
		Mood:             resp.Mood, // уже нормализован в Validate()
		Tags:             resp.Tags,
		TLDR:             resp.TLDR,
		FunFact:          resp.FunFact,
	}

	// Картинка: скрапер (фаза 1) → RSS image → Enclosures
	if scrapeImageURL != "" {
		n.ImageURL = scrapeImageURL
	} else if item.Image != nil {
		n.ImageURL = item.Image.URL
	} else if len(item.Enclosures) > 0 {
		for _, enc := range item.Enclosures {
			if strings.HasPrefix(enc.Type, "image/") {
				n.ImageURL = enc.URL
				break
			}
		}
	}

	// Clean tags
	for j, t := range n.Tags {
		n.Tags[j] = strings.TrimPrefix(t, "#")
	}

	// Скоринг: mood-based fallback.
	// Keyword score is added by the caller (FilterAndTranslateWithOptions)
	// after combining with the pre-filter keyword score.
	// Category override from keywords also happens in the caller.
	n.Score = calculateScore(n)

	logger.Info("category assigned", "title", title, "category", n.Category, "score", n.Score)

	return &n, nil
}
