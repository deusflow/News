package news

import (
	"context"
	"log"
	"sort"
	"strings"
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

func FilterAndTranslateWithOptions(ctx context.Context, items []*rss.FeedItem, opts Options) ([]News, error) {
	log.Printf("🚀 Starting news fetch cycle...")

	// 1. Получаем RSS
	// Используем items, которые были переданы в параметрах
	log.Printf("📥 Received %d raw items from RSS", len(items))

	// 2. Фильтруем по дате
	var recent []*rss.FeedItem
	cutoff := time.Now().Add(-opts.MaxAge)
	for _, item := range items {
		if item.PublishedParsed != nil && item.PublishedParsed.After(cutoff) {
			recent = append(recent, item)
		}
	}

	// 3. Удаляем дубликаты ссылок
	unique := make(map[string]*rss.FeedItem)
	for _, item := range recent {
		unique[item.Link] = item
	}

	var candidates []*rss.FeedItem
	for _, item := range unique {
		candidates = append(candidates, item)
	}

	// Сортируем: свежие сначала
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].PublishedParsed == nil || candidates[j].PublishedParsed == nil {
			return false
		}
		return candidates[i].PublishedParsed.After(*candidates[j].PublishedParsed)
	})

	log.Printf("🔍 Unique items to process: %d", len(candidates))

	var result []News

	// 4. Обработка каждой новости
	for i, item := range candidates {
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}
		if opts.ScrapeMaxArticles > 0 && i >= opts.ScrapeMaxArticles {
			log.Printf("🛑 Scrape limit reached")
			break
		}

		title := item.Title
		link := item.Link
		log.Printf("🤖 Processing [%d/%d]: %s", i+1, len(candidates), title)

		// Scrape full article (if available)
		scrapeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		ac, err := scraper.ExtractFullArticle(scrapeCtx, link)
		cancel()
		if err != nil {
			log.Printf("⚠️ Scrape failed for %s: %v", link, err)
		}

		content := ""
		if err == nil && ac != nil && ac.Content != "" {
			content = ac.Content
		} else if item.Description != "" {
			// Fallback to description
			content = item.Description
		}

		// --- AI GENERATION (ГЛАВНОЕ ИЗМЕНЕНИЕ) ---
		prompt := GenerateNewsPrompt(title, content)

		// Вызов AI через Менеджер
		resp, err := opts.AI.Generate(ctx, title, content, prompt)
		if err != nil {
			log.Printf("❌ All AI providers failed for '%s': %v", title, err)
			continue // Пропускаем новость, если никто не смог перевести
		}

		// Маппинг ответа AI в структуру новости
		published := time.Now()
		if item.PublishedParsed != nil {
			published = *item.PublishedParsed
		}

		n := News{
			Title:      title,
			Link:       link,
			Published:  published,
			Category:   "", // Будет заполнено ниже
			SourceName: item.Source.Name,
			SourceLang: item.Source.Lang,

			// Маппинг полей из ai.Response
			Summary:          resp.Summary,
			SummaryDanish:    resp.Danish,    // В ai.Response поле называется Danish
			SummaryUkrainian: resp.Ukrainian, // В ai.Response поле называется Ukrainian
			TitleUkrainian:   resp.TitleUkrainian,
			Mood:             resp.Mood,
			Tags:             resp.Tags,
			TLDR:             resp.TLDR,
			FunFact:          resp.FunFact,

			ImageURL: "", // Будет заполнено скрапером или RSS
		}

		// Картинка из скрапера или RSS или Enclosures
		// ИСПРАВЛЕННАЯ ЛОГИКА КАРТИНОК:
		// 1. Сначала пробуем картинку из скрапера (обычно лучшее качество)
		if err == nil && ac != nil && ac.ImageURL != "" {
			n.ImageURL = ac.ImageURL
		} else if item.Image != nil {
			// 2. Если нет, пробуем из RSS (стандартное поле image)
			n.ImageURL = item.Image.URL
		} else if len(item.Enclosures) > 0 {
			// 3. Иногда картинки бывают в Enclosures
			for _, enc := range item.Enclosures {
				if strings.HasPrefix(enc.Type, "image/") {
					n.ImageURL = enc.URL
					break
				}
			}
		}

		if len(item.Source.Categories) > 0 {
			n.Category = item.Source.Categories[0]
		}

		// Clean tags
		for j, t := range n.Tags {
			n.Tags[j] = strings.TrimPrefix(t, "#")
		}

		// Подсчет очков (если есть Keywords)
		if opts.Keywords != nil {
			// ... логика скоринга ...
			score, cat := opts.Keywords.CalculateScore(title + " " + content)
			n.Score = score
			if cat != "" {
				n.Category = cat
			}
		} else {
			n.Score = calculateScore(n) // Старый метод, если нет кейвордов
		}

		// Фильтр мусора (score < 0)
		if n.Score < 0 {
			log.Printf("🗑️ Skipping low score news: %s (%d)", n.Title, n.Score)
			continue
		}

		result = append(result, n)
		log.Printf("✅ Success: %s (Mood: %s)", n.TitleUkrainian, n.Mood)
	}

	// 5. Сортировка (Оставляем как было)
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Score > result[j].Score
	})

	return result, nil
}

// Вспомогательная функция (если удалил старый calculateScore, можно оставить упрощенную версию или удалить, если используешь KeywordsConfig)
func calculateScore(n News) int {
	// ... (старая логика) ...
	score := 0
	switch strings.ToLower(n.Mood) {
	case "urgent":
		score += 50
	case "shocking":
		score += 30
	case "positive":
		score += 10
	}
	if time.Since(n.Published).Hours() < 4 {
		score += 10
	}
	if n.ImageURL != "" {
		score += 5
	}
	return score
}
