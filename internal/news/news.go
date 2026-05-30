package news

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
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

	Summary          string   `json:"summary"`
	SummaryDanish    string   `json:"danish"`
	SummaryUkrainian string   `json:"ukrainian"`
	TitleDanish      string   `json:"title_danish"`
	TitleUkrainian   string   `json:"title_ukrainian"`
	Mood             string   `json:"mood"`
	Tags             []string `json:"tags"`
	TLDR             string   `json:"tldr"`
	FunFact          string   `json:"fun_fact"`
	WhyItMatters     string   `json:"why_it_matters"`
	IsExclusive      bool     `json:"is_exclusive"`

	ImageURL string
	ImageAlt string
	VideoURL string `json:"video_url,omitempty"`

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

	// ГЛАВНОЕ ИЗМЕНЕНИЕ: Используем интерфейс, а не конкретные клиенты
	AI       ai.Provider
	Config   *config.Config
	Metrics  *metrics.Metrics
	Keywords *config.KeywordsConfig
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
}

func FilterAndTranslateWithOptions(ctx context.Context, items []*rss.FeedItem, opts Options) ([]News, error) {
	logger.Info("starting news fetch cycle")
	logger.Info("received raw items from RSS", "count", len(items))

	minKeywordScoreForAI := getMinKeywordScoreForAI()

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

	type preScored struct {
		item              *rss.FeedItem
		kwScore           int
		kwCat             string
		kwCategoryWeights map[string]int
		kwMatches         []config.KeywordMatch
	}

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

	// Сортируем по keyword score (лучшие первые)
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].kwScore > scored[j].kwScore
	})

	// Отсекаем явный мусор (score < 0), лимитируем по ПерСорс и берём топ maxAICandidates
	var topCandidates []preScored
	sourceCounts := make(map[string]int)

	for _, s := range scored {
		if opts.Keywords != nil && s.kwScore < minKeywordScoreForAI {
			logger.Info("pre-filter: skipping non-relevant keyword score",
				"title", s.item.Title,
				"source", s.item.Source.Name,
				"lang", s.item.Source.Lang,
				"kw_score", s.kwScore,
				"threshold", minKeywordScoreForAI)
			continue
		}

		if opts.PerSource > 0 {
			if sourceCounts[s.item.Source.Name] >= opts.PerSource {
				continue // Skip if this source already has enough top candidates
			}
			sourceCounts[s.item.Source.Name]++
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
	impact += categoryWeights["society"]
	impact += categoryWeights["work"]
	impact += categoryWeights["eu"]
	impact += categoryWeights["economy"] / 2
	impact += categoryWeights["visas"] / 2
	impact += categoryWeights["money"] / 2
	impact += categoryWeights["housing"] / 2
	impact += categoryWeights["health"] / 2
	impact += categoryWeights["transport"] / 3
	impact += categoryWeights["local"] / 3

	// Entertainment-only items get a small penalty when no public-impact signal exists.
	if impact == 0 {
		light := categoryWeights["lifestyle"] + categoryWeights["sport"]
		if light > 0 {
			impact -= min(6, light/3)
		}
	}

	// Contextual filter: limit raw local/transport incidents without structural framing.
	structural := categoryWeights["politics"] + categoryWeights["work"] + categoryWeights["money"] + categoryWeights["visas"] + categoryWeights["economy"] + categoryWeights["education"] + categoryWeights["eu"]
	incidentScore := categoryWeights["transport"] + categoryWeights["local"]
	if structural < 5 && incidentScore > 10 {
		impact -= min(10, incidentScore/2) // Penalty for pure traffic/local incident
	} else if categoryWeights["local"] > 0 {
		// local-with-policy synergy: local news + strong policy/money/housing context
		policyWeights := categoryWeights["work"] + categoryWeights["money"] + categoryWeights["visas"] + categoryWeights["housing"] + categoryWeights["society"]
		if policyWeights >= 5 {
			impact += min(15, policyWeights) // solid boost for local law/money/society news
		}
	}

	crimeScore := categoryWeights["crime"]
	if crimeScore > 0 {
		if structural < 5 {
			impact -= min(8, crimeScore/3) // Penalize isolated crime without structural context
		} else {
			impact += min(6, crimeScore/4) // Allow limited boost when crime has broader context
		}
	}

	scaleBoost := categoryWeights["politics"] + categoryWeights["economy"] + categoryWeights["eu"]
	if scaleBoost > 0 {
		impact += min(12, scaleBoost/4)
	}

	if impact > 40 {
		impact = 40
	}
	if impact < -10 {
		impact = -10
	}
	return impact
}

func calculateEditorialSignals(categoryWeights map[string]int) (coreImpact int, softScore int, adjustment int) {
	if len(categoryWeights) == 0 {
		return 0, 0, 0
	}

	coreImpact += categoryWeights["politics"]
	coreImpact += categoryWeights["society"]
	coreImpact += categoryWeights["work"]
	coreImpact += categoryWeights["economy"]
	coreImpact += categoryWeights["visas"]
	coreImpact += categoryWeights["money"]
	coreImpact += categoryWeights["education"]
	coreImpact += categoryWeights["health"]
	coreImpact += categoryWeights["housing"]
	coreImpact += categoryWeights["eu"]

	// Local and transport contribute less directly to structural CoreImpact unless accompanied by context.
	incidentImpact := categoryWeights["local"] + categoryWeights["transport"]
	if coreImpact >= 5 {
		// Structural context exists broadly, allow incident words to add flavor.
		coreImpact += incidentImpact / 2
	} else {
		// Barely structural, heavily local.
		coreImpact += incidentImpact / 4
	}

	softScore += categoryWeights["lifestyle"]
	softScore += categoryWeights["sport"]
	softScore += categoryWeights["family"]

	coreBoost := min(20, coreImpact/4)
	softPenalty := 0
	if coreImpact == 0 && softScore > 0 {
		softPenalty = min(10, softScore/2)
	} else if coreImpact < 10 && softScore > 0 {
		softPenalty = min(6, softScore/3)
	}

	adjustment = coreBoost - softPenalty

	policyFocus := categoryWeights["visas"] + categoryWeights["work"] + categoryWeights["money"] + categoryWeights["housing"] + categoryWeights["society"]
	if policyFocus > 0 {
		adjustment += min(12, policyFocus/4)
	}
	return coreImpact, softScore, adjustment
}

func isImpactCandidate(n News) bool {
	return n.ImpactScore >= impactPriorityThreshold
}

func isCoreImpactCategory(c Category) bool {
	switch c {
	case CategoryPolitics, CategorySociety, CategoryWork, CategoryEconomy, CategoryVisas, CategoryMoney, CategoryEducation, CategoryEU, CategoryWar:
		return true
	default:
		return false
	}
}

// PassesPublicImpactGate marks candidates that should be prioritized for publication.
func PassesPublicImpactGate(n News) bool {
	if n.CoreImpactScore >= 10 {
		return true
	}
	if n.ImpactScore >= impactPriorityThreshold {
		return true
	}
	cat := ValidateCategory(n.Category)
	return isCoreImpactCategory(cat) && n.KeywordScore >= 12
}

// PassesAudienceRelevanceGate is a strict filter ensuring we do not publish empty noise.
// It allows structural/country-wide news, good lifestyle stories, but drops bare local incidents.
func PassesAudienceRelevanceGate(n News) bool {
	// If AI evaluated the news and gave it a low relevance score for Ukrainians, block it immediately
	// 1-3 scores correspond to weak/irrelevant connection.
	if n.AudienceScore > 0 && n.AudienceScore <= 3 {
		return false
	}

	if !n.HasDenmarkContext && !n.HasUkraineContext {
		return false
	}

	cat := ValidateCategory(n.Category)
	if cat == CategoryCrime {
		if !n.HasDenmarkContext {
			return false
		}
		policySignal := 0
		if len(n.KeywordCategoryWeights) > 0 {
			policySignal += n.KeywordCategoryWeights["politics"]
			policySignal += n.KeywordCategoryWeights["visas"]
			policySignal += n.KeywordCategoryWeights["work"]
			policySignal += n.KeywordCategoryWeights["money"]
			policySignal += n.KeywordCategoryWeights["housing"]
			policySignal += n.KeywordCategoryWeights["society"]
		}
		return policySignal >= 20
	}

	if PassesPublicImpactGate(n) {
		return true // Structural news passes
	}

	// If it's a weak local/transport incident, ruthlessly cut it
	if cat == CategoryLocal || cat == CategoryCrime {
		return false // No structural impact = rejected
	}

	// If it's lifestyle/sport/family, we allow it (30% balance) but require a decent score
	if cat == CategoryLifestyle || cat == CategoryFamily || cat == CategorySport {
		// Must not be empty triviality
		return n.KeywordScore >= 20 || n.Score >= 70
	}

	// Fallback for edge cases (including structural society)
	return n.Score >= 60
}

func sortByPublishPriority(items []News) {
	sort.SliceStable(items, func(i, j int) bool {
		iGate := PassesPublicImpactGate(items[i])
		jGate := PassesPublicImpactGate(items[j])
		if iGate != jGate {
			return iGate
		}
		if items[i].CoreImpactScore != items[j].CoreImpactScore {
			return items[i].CoreImpactScore > items[j].CoreImpactScore
		}

		iImpact := isImpactCandidate(items[i])
		jImpact := isImpactCandidate(items[j])
		if iImpact != jImpact {
			return iImpact
		}
		if iImpact && items[i].ImpactScore != items[j].ImpactScore {
			return items[i].ImpactScore > items[j].ImpactScore
		}
		if items[i].AudienceScore != items[j].AudienceScore {
			return items[i].AudienceScore > items[j].AudienceScore
		}
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		iHasImage := items[i].ImageURL != ""
		jHasImage := items[j].ImageURL != ""
		if iHasImage != jHasImage {
			return iHasImage
		}
		return false
	})
}

func topKeywordMatchesForLog(matches []config.KeywordMatch, limit int) string {
	if len(matches) == 0 || limit <= 0 {
		return ""
	}
	sorted := make([]config.KeywordMatch, len(matches))
	copy(sorted, matches)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Weight > sorted[j].Weight
	})
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	parts := make([]string, 0, len(sorted))
	for _, m := range sorted {
		mode := "substr"
		if m.WholeWord {
			mode = "word"
		}
		parts = append(parts, fmt.Sprintf("%s:%s:%d:%s", m.Word, m.Category, m.Weight, mode))
	}
	return strings.Join(parts, ", ")
}

func formatCategoryWeightsForLog(weights map[string]int) string {
	if len(weights) == 0 {
		return ""
	}
	keys := make([]string, 0, len(weights))
	for k := range weights {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, weights[k]))
	}
	return strings.Join(parts, ",")
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
	logger.Info("AI response fields",
		"title", title,
		"category_raw", resp.Category,
		"mood", resp.Mood,
		"audience_score", resp.AudienceScore,
		"dk_len", len([]rune(strings.TrimSpace(resp.Danish))),
		"ua_len", len([]rune(strings.TrimSpace(resp.Ukrainian))),
		"tldr_len", len([]rune(strings.TrimSpace(resp.TLDR))),
		"fun_fact_len", len([]rune(strings.TrimSpace(resp.FunFact))),
		"why_it_matters_len", len([]rune(strings.TrimSpace(resp.WhyItMatters))))

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
		TitleDanish:      resp.TitleDanish,
		TitleUkrainian:   resp.TitleUkrainian,
		Mood:             resp.Mood, // уже нормализован в Validate()
		Tags:             resp.Tags,
		TLDR:             resp.TLDR,
		FunFact:          resp.FunFact,
		WhyItMatters:     resp.WhyItMatters,
		IsExclusive:      resp.IsExclusive,
		AudienceScore:    resp.AudienceScore,
	}

	// Картинка: скрапер (фаза 1) → RSS image → Enclosures
	if scrapeImageURL != "" {
		n.ImageURL = scrapeImageURL
	} else if item.Image != nil {
		n.ImageURL = item.Image.URL
	}

	if len(item.Enclosures) > 0 {
		for _, enc := range item.Enclosures {
			if strings.HasPrefix(enc.Type, "image/") && n.ImageURL == "" {
				n.ImageURL = enc.URL
			} else if strings.HasPrefix(enc.Type, "video/") && n.VideoURL == "" {
				n.VideoURL = enc.URL
			} else if n.VideoURL == "" && (strings.Contains(enc.URL, "youtube.com") || strings.Contains(enc.URL, "youtu.be") || strings.HasSuffix(strings.ToLower(enc.URL), ".mp4")) {
				n.VideoURL = enc.URL
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

func getMinKeywordScoreForAI() int {
	const defaultMin = 8
	if v := os.Getenv("MIN_KEYWORD_SCORE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultMin
}

func hasDenmarkContext(categoryWeights map[string]int, matches []config.KeywordMatch) bool {
	if len(categoryWeights) > 0 && categoryWeights["local"] > 0 {
		return true
	}
	for _, m := range matches {
		switch strings.ToLower(m.Word) {
		case "danmark", "denmark", "danish", "i danmark", "dansk", "danske", "danskere", "folketing", "regeringen", "statsminister", "kommune", "kommunen", "kommunerne", "region", "regioner":
			return true
		}
	}
	return false
}

func hasUkraineContext(matches []config.KeywordMatch) bool {
	for _, m := range matches {
		switch strings.ToLower(m.Word) {
		case "ukrainsk", "ukrainer", "ukrainske flygtninge", "ukrainsk statsborger", "sl1", "paragraf 24":
			return true
		}
	}
	return false
}
