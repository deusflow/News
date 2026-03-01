package news

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
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
	Limit             int
	MaxAge            time.Duration
	PerSource         int
	ScrapeMaxArticles int
	ScrapeConcurrency int // Добавили, так как было в app.go

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
	log.Printf("🚀 Starting news fetch cycle...")
	log.Printf("📥 Received %d raw items from RSS", len(items))

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

	// Сортируем: свежие сначала (стабильный порядок для AI-очереди)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].PublishedParsed == nil || candidates[j].PublishedParsed == nil {
			return false
		}
		return candidates[i].PublishedParsed.After(*candidates[j].PublishedParsed)
	})
	log.Printf("🔍 Unique items after dedup: %d", len(candidates))

	// ── Шаг 3: параллельный скрейпинг (ТОЛЬКО HTTP, без AI) ──────────────────
	//
	// Здесь допустим параллелизм: каждый воркер делает HTTP-запрос к источнику.
	// AI в этом шаге не вызывается вообще — никакой очереди AI, никакого блока.
	concurrency := opts.ScrapeConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	log.Printf("🌐 Phase 1 — parallel scraping (%d workers)...", concurrency)

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

	scrapeJobs := make(chan scrapeJob, len(candidates))
	scrapeResults := make(chan scrapeResult, len(candidates))

	var scrapeWg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		scrapeWg.Add(1)
		go func() {
			defer scrapeWg.Done()
			for j := range scrapeJobs {
				if ctx.Err() != nil {
					// контекст отменён — не начинаем новые запросы
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
					log.Printf("⚠️ Scrape failed for %s, using RSS description: %v", j.item.Link, err)
				default:
					log.Printf("⚠️ No content at all for %s: %v", j.item.Link, err)
				}

				scrapeResults <- scrapeResult{index: j.index, item: j.item, content: content, scrapeImageURL: imageURL}
			}
		}()
	}

	for i, item := range candidates {
		scrapeJobs <- scrapeJob{index: i, item: item}
	}
	close(scrapeJobs)

	scrapeWg.Wait()
	close(scrapeResults)

	// Собираем и сортируем по индексу — порядок = по свежести
	scrapeMap := make(map[int]scrapeResult, len(candidates))
	for r := range scrapeResults {
		scrapeMap[r.index] = r
	}
	scraped := make([]scrapedItem, 0, len(scrapeMap))
	for i := 0; i < len(candidates); i++ {
		if r, ok := scrapeMap[i]; ok {
			scraped = append(scraped, scrapedItem{
				index:          r.index,
				item:           r.item,
				content:        r.content,
				scrapeImageURL: r.scrapeImageURL,
			})
		}
	}
	log.Printf("✅ Phase 1 done: %d items scraped", len(scraped))

	// ── Шаг 4: последовательные AI-вызовы (строго один за одним) ────────────
	//
	// AI Manager уже обеспечивает паузу между запросами (defaultDelay).
	// Здесь мы просто идём по списку — никакого параллелизма, никакой очереди,
	// никакого риска потерять контент, который уже scrape-нут.
	//
	// Если контекст отменяется в середине — обрабатываем только то, что успели.
	log.Printf("🤖 Phase 2 — sequential AI processing (%d items)...", len(scraped))

	var result []News
	aiErrors := 0

	for _, s := range scraped {
		if ctx.Err() != nil {
			log.Printf("⚠️ Context cancelled after %d AI calls, stopping early", len(result)+aiErrors)
			break
		}
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}

		n, err := processItemWithContent(ctx, s.item, s.index, s.content, s.scrapeImageURL, opts)
		if err != nil {
			log.Printf("❌ AI failed for item %d ('%s'): %v", s.index+1, s.item.Title, err)
			aiErrors++
			continue
		}
		if n != nil {
			result = append(result, *n)
		}
	}

	log.Printf("✅ Phase 2 done: %d news ready, %d AI errors", len(result), aiErrors)

	// ── Шаг 5: сортировка по score ───────────────────────────────────────────
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Score > result[j].Score
	})

	return result, nil
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
	log.Printf("🤖 AI processing item %d: %s", index+1, title)

	// HIGH-3 guard: если контент пустой и заголовок слишком короткий —
	// AI получит практически нулевой input и начнёт галлюцинировать.
	// Минимальный полезный заголовок: 30 символов (~5-6 слов на датском).
	const minTitleLen = 30
	if content == "" && len([]rune(strings.TrimSpace(title))) < minTitleLen {
		log.Printf("⚠️ Skipping item %d — no content and title too short (%d chars): %q",
			index+1, len([]rune(strings.TrimSpace(title))), title)
		return nil, nil
	}
	if content == "" {
		log.Printf("⚠️ Item %d has no scraped content, AI will work from title only: %q", index+1, title)
	}

	prompt := GenerateNewsPrompt(title, content)

	resp, aiErr := opts.AI.Generate(ctx, title, content, prompt)
	if aiErr != nil {
		log.Printf("❌ All AI providers failed for '%s': %v", title, aiErr)
		return nil, aiErr
	}

	// Валидация AI ответа: проверяем обязательные поля и нормализуем mood
	if err := resp.Validate(); err != nil {
		log.Printf("❌ AI response validation failed for '%s': %v", title, err)
		return nil, err
	}

	published := time.Now()
	if item.PublishedParsed != nil {
		published = *item.PublishedParsed
	}

	// Категорию из AI валидируем через whitelist — галлюцинации не пройдут
	aiCategory := ValidateCategory(resp.Category)

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

	// Скоринг и финальная категория.
	// Keywords могут перезаписать категорию AI если нашлось точное совпадение.
	// Категория из keywords тоже проходит через ValidateCategory — "spam" и
	// неизвестные значения откатятся к CategoryDefault.
	if opts.Keywords != nil {
		score, kwCat := opts.Keywords.CalculateScore(title + " " + content)
		n.Score = score
		if kwCat != "" && kwCat != "spam" {
			n.Category = string(ValidateCategory(kwCat))
		}
	} else {
		n.Score = calculateScore(n)
	}

	log.Printf("📂 Category for '%s': %s (score: %d)", title, n.Category, n.Score)

	// Фильтр мусора (score < 0)
	if n.Score < 0 {
		log.Printf("🗑️ Skipping low score news: %s (%d)", n.Title, n.Score)
		return nil, nil
	}

	return &n, nil
}
