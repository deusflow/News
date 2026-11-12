package news

import (
	"context"
	"fmt"
	"sort"
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
	KeywordScore           int
	ImpactScore            int
	CoreImpactScore        int
	SoftScore              int
	EditorialAdjustment    int
	AudienceScore          int
	KeywordCategoryWeights map[string]int
	KeywordMatches         []config.KeywordMatch
	HasDenmarkContext      bool
	HasUkraineContext      bool

	SourceName string
	SourceLang string

	Summary          string    `json:"summary"`
	SummaryDanish    string    `json:"danish"`
	SummaryUkrainian string    `json:"ukrainian"`
	TitleDanish      string    `json:"title_danish"`
	TitleUkrainian   string    `json:"title_ukrainian"`
	StoryClusterKey  string    `json:"story_cluster_key,omitempty"`
	ConcreteAnchor   string    `json:"concrete_anchor,omitempty"`
	Embedding        []float32 `json:"-"`
	Mood             string    `json:"mood"`
	Tags             []string  `json:"tags"`
	TLDR             string    `json:"tldr"`
	FunFact          string    `json:"fun_fact"`
	WhyItMatters     string    `json:"why_it_matters"`
	IsExclusive      bool      `json:"is_exclusive"`
	IsLongread       bool      `json:"is_longread"`

	ImageURL string
	ImageAlt string
	VideoURL string `json:"video_url,omitempty"`

	ReplyToMessageID int `json:"reply_to_message_id,omitempty"`

	// Legacy поля для совместимости
	ImportanceDanish    string
	ImportanceUkrainian string
}

type Options struct {
	MaxAge            time.Duration
	PerSource         int
	ScrapeMaxArticles int
	ScrapeConcurrency int

	// Dedupe allows skipping already-sent items before AI.
	Dedupe DedupeChecker

	// AICache allows caching AI translations to avoid paying AI twice on retries or delivery errors.
	AICache AICacheChecker

	// ГЛАВНОЕ ИЗМЕНЕНИЕ: Используем интерфейс, а не конкретные клиенты
	AI       ai.Provider
	Config   *config.Config
	Metrics  *metrics.Metrics
	Keywords *config.KeywordsConfig
}

// AICacheChecker provides access to cached AI translations.
type AICacheChecker interface {
	GetCachedAITranslation(hash string) (string, error)
	SaveCachedAITranslation(hash, link, title, responseJSON string) error
}

// DedupeChecker provides pre-AI duplicate checks without importing app.
type DedupeChecker interface {
	GenerateNewsHash(title, link string) string
	IsAlreadySent(hash string) bool
	IsSourceURLSent(sourceURL string) bool
}

// scrapedItem хранит результат первого этапа — параллельного скрейпинга.
// AI-обработка происходит только на втором этапе, строго последовательно.
type scrapedItem struct {
	index          int
	item           *rss.FeedItem
	content        string // итоговый контент: scraper → description → ""
	scrapeImageURL string // og:image из скрапера (может быть "")
	isPaywalled    bool
	paywallReason  string
}

// preScored holds a candidate after cheap keyword evaluation but before AI.
// Package-level so helpers like applyCrossSourceBoost can operate on it.
type preScored struct {
	item              *rss.FeedItem
	kwScore           int
	kwCat             string
	kwCategoryWeights map[string]int
	kwMatches         []config.KeywordMatch
}

func FilterAndTranslateWithOptions(ctx context.Context, items []*rss.FeedItem, opts Options) ([]News, error) {
	logger.Info("starting news fetch cycle")
	logger.Info("received raw items from RSS", "count", len(items))

	minKeywordScoreForAI := getMinKeywordScoreForAI()

	// ── Шаг 1: фильтрация по дате ────────────────────────────────────────────
	var recent []*rss.FeedItem
	cutoff := time.Now().Add(-opts.MaxAge)
	for _, item := range items {
		pubTime := item.PublishedParsed
		if pubTime == nil {
			pubTime = item.UpdatedParsed
		}
		if pubTime != nil && pubTime.After(cutoff) {
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
		ti := candidates[i].PublishedParsed
		if ti == nil {
			ti = candidates[i].UpdatedParsed
		}
		tj := candidates[j].PublishedParsed
		if tj == nil {
			tj = candidates[j].UpdatedParsed
		}
		if ti == nil || tj == nil {
			return false
		}
		if !ti.Equal(*tj) {
			return ti.After(*tj)
		}
		return candidates[i].Source.Priority > candidates[j].Source.Priority
	})
	logger.Info("unique items after dedup", "count", len(candidates))

	// ── Шаг 2б: дедупликация ДО AI (экономия запросов) ───────────────────────
	if opts.Dedupe != nil {
		filtered := candidates[:0]
		for _, item := range candidates {
			if opts.Dedupe.IsSourceURLSent(item.Link) {
				logger.Info("pre-AI dedup: source URL already sent", "title", item.Title, "source", item.Source.Name)
				continue
			}
			hash := opts.Dedupe.GenerateNewsHash(item.Title, item.Link)
			if opts.Dedupe.IsAlreadySent(hash) {
				logger.Info("pre-AI dedup: hash already sent", "title", item.Title, "source", item.Source.Name)
				continue
			}
			filtered = append(filtered, item)
		}
		if len(filtered) < len(candidates) {
			logger.Info("pre-AI dedup applied",
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
	// Количество AI-кандидатов берём из конфига скрапера.
	// Это устраняет рассинхрон между конфигурацией и фактическим поведением.
	maxAICandidates := opts.ScrapeMaxArticles
	if maxAICandidates <= 0 {
		maxAICandidates = 5
	}
	// Не скачиваем и не отправляем в ИИ больше статей, чем позволяет бесплатный лимит запросов (минус 1 запрос на AI Triage)
	if opts.Config != nil && opts.Config.AI.MaxGeminiRequests > 1 {
		budget := opts.Config.AI.MaxGeminiRequests - 1
		if maxAICandidates > budget {
			maxAICandidates = budget
		}
	}

	// preScored is defined at package level (below) so it can be used
	// by applyCrossSourceBoost and other helpers.

	var scored []preScored
	for _, item := range candidates {
		kwScore := 0
		kwCat := ""
		kwCategoryWeights := map[string]int{}
		var kwMatches []config.KeywordMatch
		if opts.Keywords != nil {
			// Pre-score по title + RSS description (контент ещё не скрейпнут)
			text := item.Title
			if item.Description != "" {
				text += " " + item.Description
			}
			kwScore, kwCat, kwCategoryWeights, kwMatches = opts.Keywords.CalculateScoreDetailedWithMatches(text)
		}

		// Apply source authority multiplier instead of raw additive
		// Weight 12 -> +60% multiplier
		// Weight -5 -> -25% multiplier
		multiplier := 1.0 + (float64(item.Source.Weight) * 0.05)
		if multiplier < 0.5 {
			multiplier = 0.5
		}
		kwScore = int(float64(kwScore) * multiplier)

		scored = append(scored, preScored{
			item:              item,
			kwScore:           kwScore,
			kwCat:             kwCat,
			kwCategoryWeights: kwCategoryWeights,
			kwMatches:         kwMatches,
		})
	}

	// ── Шаг 3а: Cross-source boost ────────────────────────────────────────
	// If multiple RSS sources report the same story, it's a strong editorial
	// signal. We use bigram Jaccard similarity on titles to cluster stories.
	// Each extra source mention adds +15 to keyword score (free, no AI).
	applyCrossSourceBoost(scored)

	// Сортируем по keyword score (лучшие первые)
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].kwScore > scored[j].kwScore
	})

	// 50/50 prefilter: половина слотов под строгие keyword матчи,
	// остальное — VIP/пограничные кейсы (аккуратно, чтобы не сжечь AI лимит).
	primarySlots := maxAICandidates
	reserveSlots := 0
	if maxAICandidates >= 4 {
		primarySlots = maxAICandidates / 2
		reserveSlots = maxAICandidates - primarySlots
	}

	primary := make([]preScored, 0, primarySlots)
	reserve := make([]preScored, 0, reserveSlots)
	picked := make(map[string]bool, maxAICandidates)
	sourceCounts := make(map[string]int)

	appendCandidate := func(target *[]preScored, s preScored) bool {
		if picked[s.item.Link] {
			return false
		}
		if opts.PerSource > 0 {
			if sourceCounts[s.item.Source.Name] >= opts.PerSource {
				return false
			}
			sourceCounts[s.item.Source.Name]++
		}
		picked[s.item.Link] = true
		*target = append(*target, s)
		return true
	}

	for _, s := range scored {
		if s.kwScore >= minKeywordScoreForAI {
			if len(primary) >= primarySlots {
				continue
			}
			appendCandidate(&primary, s)
		}
	}

	if reserveSlots > 0 {
		for _, s := range scored {
			if len(reserve) >= reserveSlots {
				break
			}
			if s.kwScore >= minKeywordScoreForAI {
				continue
			}
			if !isVIPKeywordMatch(s.kwMatches) {
				continue
			}
			appendCandidate(&reserve, s)
		}

		if len(reserve) < reserveSlots {
			for _, s := range scored {
				if len(reserve) >= reserveSlots {
					break
				}
				if s.kwScore >= minKeywordScoreForAI {
					continue
				}
				if isVIPKeywordMatch(s.kwMatches) {
					continue
				}
				if !isBorderlineKeywordScore(s.kwScore, minKeywordScoreForAI) {
					continue
				}
				if !hasDenmarkContext(s.kwCategoryWeights, s.kwMatches) && !hasUkraineContext(s.kwMatches) {
					continue
				}
				appendCandidate(&reserve, s)
			}
		}
	}

	topCandidates := append(primary, reserve...)
	if len(topCandidates) < maxAICandidates {
		for _, s := range scored {
			if len(topCandidates) >= maxAICandidates {
				break
			}
			if s.kwScore < minKeywordScoreForAI {
				continue
			}
			appendCandidate(&topCandidates, s)
		}
	}

	logger.Info("keyword pre-filter done",
		"total_candidates", len(candidates),
		"passed_filter", len(topCandidates),
		"rejected", len(candidates)-len(topCandidates),
		"primary_slots", primarySlots,
		"reserve_slots", reserveSlots,
		"primary_used", len(primary),
		"reserve_used", len(reserve),
		"min_keyword_score", minKeywordScoreForAI)

	// ── Шаг 3б: AI Triage — дешёвая rescue отвергнутых заголовков ─────────
	// Один batch-запрос (~200 input tokens) оценивает ВСЕ заголовки,
	// не прошедшие keyword filter, и спасает до 3 потенциально важных.
	if opts.AI != nil && len(topCandidates) < maxAICandidates {
		rejected := collectRejectedHeadlines(candidates, picked)
		if len(rejected) > 0 {
			triageSlots := maxAICandidates - len(topCandidates)
			if triageSlots > 3 {
				triageSlots = 3
			}
			rescued := runTriage(ctx, rejected, opts.AI)
			if len(rescued) > 0 {
				// Map rescued indices back to original candidates
				rejectedItems := make([]*rss.FeedItem, 0, len(rejected))
				for _, item := range candidates {
					if !picked[item.Link] {
						rejectedItems = append(rejectedItems, item)
					}
				}
				added := 0
				for _, rIdx := range rescued {
					if added >= triageSlots {
						break
					}
					if rIdx < len(rejectedItems) {
						item := rejectedItems[rIdx]
						// Find the preScored entry for this item
						for _, s := range scored {
							if s.item.Link == item.Link {
								if appendCandidate(&topCandidates, s) {
									added++
								}
								break
							}
						}
					}
				}
				logger.Info("AI triage added candidates",
					"rescued", added,
					"total_after_triage", len(topCandidates))
			}
		}
	}

	if len(topCandidates) == 0 {
		logger.Info("no candidates passed keyword pre-filter or AI triage")
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
		isPaywalled    bool
		paywallReason  string
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
				isPaywalled := false
				paywallReason := ""
				switch {
				case err == nil && ac != nil && ac.Content != "":
					content = ac.Content
					imageURL = ac.ImageURL
					isPaywalled = ac.IsPaywalled
					paywallReason = ac.PaywallReason
				case j.item.Description != "":
					content = j.item.Description
					logger.Warn("scrape failed, using RSS description", "url", j.item.Link, "error", err)
					isPaywalled, paywallReason = scraper.IsPaywalledOrStub(content, j.item.Link)
				default:
					logger.Warn("no content for item", "url", j.item.Link, "error", err)
				}

				scrapeResults <- scrapeResult{
					index:          j.index,
					item:           j.item,
					content:        content,
					scrapeImageURL: imageURL,
					isPaywalled:    isPaywalled,
					paywallReason:  paywallReason,
				}
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
				isPaywalled:    r.isPaywalled,
				paywallReason:  r.paywallReason,
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
	aiAttempts := 0
	aiErrors := 0

	for idx, s := range scraped {
		if ctx.Err() != nil {
			logger.Warn("context cancelled, stopping early", "completed", len(result)+aiErrors)
			break
		}

		// Paywall & Stub Buster: Skip paywalled content before wasting AI quota or hallucinations
		if s.isPaywalled {
			logger.Warn("Skipping paywalled or stub article before AI",
				"index", s.index+1,
				"title", s.item.Title,
				"source", s.item.Source.Name,
				"url", s.item.Link,
				"reason", s.paywallReason)
			continue
		}
		if isPay, reason := scraper.IsPaywalledOrStub(s.content, s.item.Link); isPay {
			logger.Warn("Skipping paywalled or stub article before AI",
				"index", s.index+1,
				"title", s.item.Title,
				"source", s.item.Source.Name,
				"url", s.item.Link,
				"reason", reason)
			continue
		}

		aiAttempts++
		n, err := processItemWithContent(ctx, s.item, s.index, s.content, s.scrapeImageURL, opts)
		if err != nil {
			logger.Error("AI failed for item", "index", s.index+1, "title", s.item.Title, "error", err)
			if opts.Metrics != nil {
				opts.Metrics.IncrementFailedTranslations()
			}
			aiErrors++
			continue
		}
		if n != nil {
			if opts.Metrics != nil {
				opts.Metrics.IncrementSuccessfulTranslations()
			}

			if n.AudienceScore < 3 {
				logger.Info("audience_score below threshold, skipping",
					"title", n.Title,
					"source", n.SourceName,
					"audience_score", n.AudienceScore)
				continue
			}

			kwScore := topCandidates[idx].kwScore
			kwCat := topCandidates[idx].kwCat
			kwCategoryWeights := topCandidates[idx].kwCategoryWeights
			kwMatches := topCandidates[idx].kwMatches

			// Re-score on full text (title + scraped content) for final ranking precision.
			// Pre-score still controls the cheap pre-filter before AI.
			if opts.Keywords != nil {
				fullText := s.item.Title
				if s.content != "" {
					fullText += " " + s.content
				}
				fullKwScore, fullKwCat, fullKwWeights, fullMatches := opts.Keywords.CalculateScoreDetailedWithMatches(fullText)

				multiplier := 1.0 + (float64(s.item.Source.Weight) * 0.05)
				if multiplier < 0.5 {
					multiplier = 0.5
				}
				fullKwScore = int(float64(fullKwScore) * multiplier)

				if fullKwScore > kwScore {
					kwScore = fullKwScore
					kwCat = fullKwCat
					kwCategoryWeights = fullKwWeights
					kwMatches = fullMatches
				}
			}

			n.KeywordCategoryWeights = kwCategoryWeights
			n.KeywordMatches = kwMatches
			n.HasDenmarkContext = hasDenmarkContext(kwCategoryWeights, kwMatches)
			n.HasUkraineContext = hasUkraineContext(kwMatches)

			// Auto-DK context: Danish-language sources are by definition about Denmark.
			// Danish media don't write "i Danmark" in every article about local events
			// (e.g. "Skuddrab i Nørrebro"), so keyword-based detection misses them.
			if s.item.Source.Lang == "da" {
				n.HasDenmarkContext = true
			}

			impactScore := calculateImpactScore(kwCategoryWeights)
			coreImpact, softScore, editorialAdjustment := calculateEditorialSignals(kwCategoryWeights)

			// Audience relevance tuning: integrate fully.
			// Since AudienceScore is 1..12 and extremely accurate about relevance, scale it strongly.
			impactScore += n.AudienceScore * 5

			// Keywords remain primary. Impact is a separate architectural signal.
			// AI (mood/freshness) stays secondary via n.Score from processItemWithContent().
			n.KeywordScore = kwScore
			n.ImpactScore = impactScore
			n.CoreImpactScore = coreImpact
			n.SoftScore = softScore
			n.EditorialAdjustment = editorialAdjustment
			n.Score += kwScore + impactScore + editorialAdjustment

			// If keywords found a category, prefer it after strict normalization.
			if kwCat != "" && kwCat != "spam" {
				if coerced, ok := CoerceCategory(kwCat); ok {
					n.Category = string(coerced)
				}
			}

			if opts.Config != nil && opts.Config.Feature.EnableDecisionLog {
				logger.Info("candidate decision",
					"title", n.Title,
					"source", n.SourceName,
					"category", n.Category,
					"keyword_score", n.KeywordScore,
					"impact_score", n.ImpactScore,
					"core_impact_score", n.CoreImpactScore,
					"soft_score", n.SoftScore,
					"editorial_adjustment", n.EditorialAdjustment,
					"audience_score", n.AudienceScore,
					"final_score", n.Score,
					"passes_public_impact_gate", PassesPublicImpactGate(*n),
					"has_denmark_context", n.HasDenmarkContext,
					"has_ukraine_context", n.HasUkraineContext,
					"keyword_top_matches", topKeywordMatchesForLog(kwMatches, 8),
					"category_weights", formatCategoryWeightsForLog(kwCategoryWeights))
			}

			result = append(result, *n)
		}
	}

	logger.Info("phase 2 done", "ready", len(result), "errors", aiErrors)

	if len(result) == 0 && aiAttempts > 0 && aiErrors >= aiAttempts {
		return nil, fmt.Errorf("all AI candidates failed processing (%d AI errors out of %d candidates attempted, check AI quota/API keys)", aiErrors, aiAttempts)
	}

	// ── Шаг 6: финальная сортировка с impact-priority ─────────────────────────
	// 1) Сильные public-impact новости идут выше остальных.
	// 2) Внутри группы порядок по final score.
	sortByPublishPriority(result)

	return result, nil
}
