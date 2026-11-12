package news

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/rss"
)

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

	hash := ""
	if opts.Dedupe != nil {
		hash = opts.Dedupe.GenerateNewsHash(title, link)
	}

	var resp *ai.Response
	var aiErr error

	// Check persistent AI translation cache before calling external AI APIs
	if hash != "" && opts.AICache != nil {
		if cachedJSON, err := opts.AICache.GetCachedAITranslation(hash); err == nil && cachedJSON != "" {
			var cachedResp ai.Response
			if unmarshalErr := json.Unmarshal([]byte(cachedJSON), &cachedResp); unmarshalErr == nil {
				if valErr := cachedResp.Validate(); valErr == nil {
					resp = &cachedResp
					logger.Info("Using cached AI translation (saved AI API quota!)", "title", title, "hash", hash)
				}
			}
		}
	}

	fromCache := false
	if resp != nil {
		fromCache = true
	}

	if resp == nil {
		systemPrompt := GenerateNewsSystemPrompt()
		userPrompt := GenerateNewsUserContent(title, content)

		resp, aiErr = opts.AI.Generate(ctx, title, content, systemPrompt, userPrompt)
		if aiErr != nil {
			logger.Error("all AI providers failed", "title", title, "error", aiErr)
			return nil, aiErr
		}

		// Валидация AI ответа: проверяем обязательные поля и нормализуем mood
		if err := resp.Validate(); err != nil {
			logger.Error("AI response validation failed", "title", title, "error", err)
			return nil, err
		}
	}

	// Guard: отсекаем стабы, пейволлы и попытки модели оправдаться («деталі невідомі»)
	if isStubOrApology(resp) {
		logger.Info("editorial decision: skipped stub/paywall story",
			"index", index+1,
			"title", title,
			"concrete_anchor", resp.ConcreteAnchor,
			"audience_score", resp.AudienceScore,
			"status", "rejected")
		return nil, nil
	}

	// Editorial filter: drop low relevance stories (audience_score 1-2: foreign debates, celebrity gossip, routine sports)
	if resp.AudienceScore <= 2 {
		logger.Info("editorial decision: skipped low relevance story (audience_score 1-2)",
			"index", index+1,
			"title", title,
			"audience_score", resp.AudienceScore,
			"status", "rejected")
		return nil, nil
	}

	// Verification 1: Verify Ukrainian language purity (no Russian characters) across all Ukrainian fields
	fullUkrainian := resp.Ukrainian + " " + resp.TitleUkrainian + " " + resp.TLDR + " " + resp.WhyItMatters + " " + resp.FunFact
	if langErr := VerifyUkrainian(fullUkrainian); langErr != nil {
		logger.Warn("Rejecting article: non-Ukrainian text detected", "title", title, "error", langErr)
		return nil, nil
	}

	// Verification 2: Verify numbers, dates, and amounts in the article body/title against Danish source text.
	// Note: FunFact is intentionally excluded because the prompt asks AI for general cultural/historical
	// facts about Denmark (which frequently contain years or statistics not in the news article).
	danishSource := title + "\n" + content + "\n" + resp.TitleDanish + "\n" + resp.Danish
	ukrainianArticleText := resp.TitleUkrainian + "\n" + resp.Ukrainian
	if numErr := VerifyNumbers(danishSource, ukrainianArticleText); numErr != nil {
		logger.Warn("Rejecting article: unverified number detected (anti-hallucination guard)", "title", title, "error", numErr)
		return nil, nil
	}

	// Save verified translation to cache so delivery retries or runner interruptions don't pay AI twice
	if !fromCache && hash != "" && opts.AICache != nil {
		if respBytes, err := json.Marshal(resp); err == nil {
			_ = opts.AICache.SaveCachedAITranslation(hash, link, title, string(respBytes))
		}
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
	} else if item.UpdatedParsed != nil {
		published = *item.UpdatedParsed
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
		StoryClusterKey:  resp.StoryClusterKey,
		ConcreteAnchor:   resp.ConcreteAnchor,
		Mood:             resp.Mood, // уже нормализован в Validate()
		Tags:             resp.Tags,
		TLDR:             resp.TLDR,
		FunFact:          resp.FunFact,
		WhyItMatters:     resp.WhyItMatters,
		IsExclusive:      resp.IsExclusive,
		IsLongread:       resp.IsLongread,
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

