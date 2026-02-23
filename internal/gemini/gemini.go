package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deusflow/News/internal/cache"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/retry"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

type Client struct {
	client        *genai.Client
	cache         *cache.Cache
	metrics       *metrics.Metrics
	modelName     string
	fallbackModel string           // gemini-2.0-flash as fallback
	rateLimiter   <-chan time.Time // Rate limiter: 1 request per interval
	rateInterval  time.Duration    // Interval between requests
}

// ContentBudget defines character limits for AI-generated content
type ContentBudget struct {
	DanishChars    int // Max chars for Danish news body
	UkrainianChars int // Max chars for Ukrainian news body
	TLDRChars      int // Max chars for TLDR
	FunFactChars   int // Max chars for FunFact
}

// Predefined budgets for different output modes
var (
	// BudgetPhotoCaption - for Telegram photo caption (1024 total limit)
	// Header ~80, titles ~100, tags ~60, funfact ~160, separators ~40 = ~440 overhead
	// Remaining ~580 for content = ~290 per language
	BudgetPhotoCaption = ContentBudget{
		DanishChars:    280,
		UkrainianChars: 280,
		TLDRChars:      80,
		FunFactChars:   120,
	}

	// BudgetTextMessage - for regular Telegram message (4096 limit)
	// Much more space available
	BudgetTextMessage = ContentBudget{
		DanishChars:    600,
		UkrainianChars: 600,
		TLDRChars:      100,
		FunFactChars:   140,
	}

	// BudgetDefault - balanced default
	BudgetDefault = ContentBudget{
		DanishChars:    400,
		UkrainianChars: 400,
		TLDRChars:      90,
		FunFactChars:   130,
	}
)

// NewsTranslation - это основная структура, которую мы отдаем наружу (в news.go)
type NewsTranslation struct {
	Summary        string
	Danish         string
	Ukrainian      string
	TitleUkrainian string   // Український переклад заголовку
	Mood           string   // Настроение новости
	Tags           []string // Теги
	TLDR           string   // Одно предложение - суть новости
	FunFact        string   // Цікавий факт про Данію
}

// NewsTranslationResponse - это "анкета" для Gemini (формат ответа API)
type NewsTranslationResponse struct {
	Summary        string   `json:"summary"`
	Danish         string   `json:"danish"`
	Ukrainian      string   `json:"ukrainian"`
	TitleUkrainian string   `json:"title_ukrainian"`
	Mood           string   `json:"mood"`
	Tags           []string `json:"tags"`
	TLDR           string   `json:"tldr"`
	FunFact        string   `json:"fun_fact"`
}

func NewClient(apiKey, modelName string, m *metrics.Metrics) (*Client, error) {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	if modelName == "" {
		modelName = "gemini-2.5-flash" // latest stable flash model
	}

	fallbackModel := "gemini-2.0-flash" // fallback to older stable model if primary fails

	// Rate limiter: 1 request per 40 seconds
	rateInterval := 40 * time.Second
	rateLimiter := time.NewTicker(rateInterval).C

	log.Printf("✅ Gemini client initialized with model: %s (fallback: %s)", modelName, fallbackModel)
	log.Printf("📊 Rate limit: 1 request per %v (~1.5 RPM, safe for Free Tier)", rateInterval)

	return &Client{
		client:        client,
		cache:         cache.New(),
		metrics:       m,
		modelName:     modelName,
		fallbackModel: fallbackModel,
		rateLimiter:   rateLimiter,
		rateInterval:  rateInterval,
	}, nil
}

func (c *Client) Close() {
	if c.client != nil {
		_ = c.client.Close()
	}
}

// TranslateAndSummarizeNews - backward compatible wrapper using photo caption budget (strictest)
func (c *Client) TranslateAndSummarizeNews(ctx context.Context, title, content string) (*NewsTranslation, error) {
	// Use BudgetPhotoCaption as default - it's the strictest limit (1024 chars total)
	// This ensures content fits in any output mode (photo or text)
	return c.TranslateAndSummarizeNewsWithBudget(ctx, title, content, BudgetPhotoCaption)
}

// TranslateAndSummarizeNewsWithBudget - generates news with specific character budget
func (c *Client) TranslateAndSummarizeNewsWithBudget(ctx context.Context, title, content string, budget ContentBudget) (*NewsTranslation, error) {
	// 1. Проверяем кэш
	cacheKey := c.cache.GenerateKey(title, content)
	if cached, found := c.cache.Get(cacheKey); found {
		if c.metrics != nil {
			c.metrics.IncrementSuccessfulTranslations()
		}
		return cached.(*NewsTranslation), nil
	}

	// Minimum: 4 × 40s = 160s, plus API response time → 900s is safe
	ctx, cancel := context.WithTimeout(ctx, 900*time.Second)
	defer cancel()

	var result *NewsTranslation
	var err error

	// 2. Try primary model first
	retryConfig := retry.RetryConfig{
		MaxAttempts: 2, // Reduced attempts for primary model
		Delay:       5 * time.Second,
		Backoff:     true,
	}

	err = retry.WithRetry(ctx, retryConfig, func() error {
		result, err = c.translateWithModel(ctx, c.modelName, title, content, budget)

		// Handle Rate Limit (429) specifically
		if err != nil {
			if gErr, ok := err.(*googleapi.Error); ok && gErr.Code == 429 {
				log.Printf("⚠️ Gemini Rate Limit hit on %s. Will try fallback...", c.modelName)
				return err // Don't wait, go to fallback
			}
		}
		return err
	})

	// 3. If primary model failed, try fallback model
	if err != nil && c.fallbackModel != "" && c.fallbackModel != c.modelName {
		log.Printf("⚠️ Primary model %s failed, trying fallback: %s", c.modelName, c.fallbackModel)

		fallbackRetryConfig := retry.RetryConfig{
			MaxAttempts: 2,
			Delay:       3 * time.Second,
			Backoff:     true,
		}

		fallbackErr := retry.WithRetry(ctx, fallbackRetryConfig, func() error {
			result, err = c.translateWithModel(ctx, c.fallbackModel, title, content, budget)
			return err
		})

		if fallbackErr == nil {
			log.Printf("✅ Fallback model %s succeeded!", c.fallbackModel)
			// Save to cache and return
			c.cache.Set(cacheKey, result, 24*time.Hour)
			return result, nil
		}

		// Both models failed
		log.Printf("❌ Both models failed. Primary: %s, Fallback: %s", c.modelName, c.fallbackModel)
		err = fmt.Errorf("all Gemini models failed: primary=%v, fallback=%v", err, fallbackErr)
	}

	if err != nil {
		if c.metrics != nil {
			c.metrics.IncrementFailedTranslations()
			c.metrics.SetError(fmt.Sprintf("Gemini API error: %v", err))
		}
		return nil, err
	}

	// 3. Сохраняем в кэш
	c.cache.Set(cacheKey, result, 24*time.Hour)

	return result, nil
}

// sanitizeAIResponse removes Markdown code block wrappers from AI responses
// Models often return JSON wrapped in ```json ... ``` which breaks json.Unmarshal
func sanitizeAIResponse(response string) string {
	response = strings.TrimSpace(response)

	// Remove opening markdown code block (case-insensitive)
	// Matches: ```json, ```JSON, ```Json, or just ```
	re := regexp.MustCompile(`(?i)^\s*` + "```" + `(?:json)?\s*\n?`)
	response = re.ReplaceAllString(response, "")

	// Remove closing markdown code block
	// Matches: ``` at the end (with optional whitespace)
	re = regexp.MustCompile(`\n?\s*` + "```" + `\s*$`)
	response = re.ReplaceAllString(response, "")

	return strings.TrimSpace(response)
}

func (c *Client) translateWithModel(ctx context.Context, modelName, title, content string, budget ContentBudget) (*NewsTranslation, error) {
	// Wait for rate limiter before making request
	if c.rateLimiter != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.rateLimiter:
			log.Printf("⏱️ Rate limiter: proceeding with request to %s", modelName)
		}
	}

	// Используем указанную модель
	model := c.client.GenerativeModel(modelName)

	model.SetTemperature(0.7)
	model.ResponseMIMEType = "application/json"

	// === ОБНОВЛЕННАЯ СХЕМА ===
	// Добавили Mood (Enum), Tags (Array), TLDR, FunFact
	model.ResponseSchema = &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"summary":         {Type: genai.TypeString},
			"danish":          {Type: genai.TypeString},
			"ukrainian":       {Type: genai.TypeString},
			"title_ukrainian": {Type: genai.TypeString},
			"mood": {
				Type: genai.TypeString,
				Enum: []string{"positive", "negative", "neutral", "shocking", "urgent"},
			},
			"tags": {
				Type:  genai.TypeArray,
				Items: &genai.Schema{Type: genai.TypeString},
			},
			"tldr":     {Type: genai.TypeString},
			"fun_fact": {Type: genai.TypeString},
		},
		Required: []string{"summary", "danish", "ukrainian", "title_ukrainian", "mood", "tags", "tldr", "fun_fact"},
	}

	// Очистка контента
	content = strings.ReplaceAll(content, "\r", "")
	content = strings.TrimSpace(content)
	content = strings.Join(strings.Fields(content), " ")
	maxChars := 15000
	if utf8.RuneCountInString(content) > maxChars {
		runes := []rune(content)
		trimmed := string(runes[:maxChars])
		if idx := strings.LastIndex(trimmed, ". "); idx > 1200 {
			trimmed = trimmed[:idx+1]
		}
		content = trimmed + "\n[TRUNCATED]"
	}

	// === ПРОМПТ С ДИНАМИЧЕСКИМ БЮДЖЕТОМ ===
	prompt := fmt.Sprintf(`
You are an editor in a bilingual newsroom. Create ONE news item in two languages: Danish and Ukrainian.

INPUT:
TITLE: %s
CONTENT: %s

GLOBAL STYLE (applies to ALL fields):
- Journalistic / reporter tone: neutral, factual, readable, dynamic
- No opinions, no эмоции, no публицистика
- Not bureaucratic and not "machine-translation" sounding
- Keep proper nouns EXACTLY as in source: personal names, brands, organizations, countries, cities, events
  Examples: "Tinderbox", "EU", "New Delhi", "Fredericia", "Skanderborg", "NATO" must stay unchanged

CRITICAL CONSISTENCY RULE:
- Danish and Ukrainian must describe the SAME facts, logic, and key accents.
- They must NOT contradict each other.
- They should NOT be word-for-word identical; wording should be natural in each language.

⚠️ CHARACTER LIMITS ARE STRICT - content will be used in Telegram with hard limits!

TASKS (return valid JSON only):
1) "summary": internal working summary (max 500 chars)

2) "danish": News BODY text in Danish (STRICT MAX %d characters!)
   - DO NOT include the title! Title is shown separately above this text.
   - Write 2-4 sentences with key facts
   - Start directly with the main fact/event
   - Be concise but informative

3) "ukrainian": News BODY text in Ukrainian (STRICT MAX %d characters!)
   - DO NOT include the title! Title is shown separately above this text.
   - Write 2-4 sentences with the SAME facts as Danish version
   - Start directly with the main fact/event
   - No greetings, no rhetorical questions

4) "title_ukrainian": Ukrainian translation of the TITLE only (max 100 chars)
   - Proper nouns unchanged
   - Neutral newsroom headline style

5) "mood": One of: "positive", "negative", "neutral", "shocking", "urgent"

6) "tags": 2-4 Ukrainian tags (short nouns, NO # symbol)

7) "tldr": ONE Ukrainian TL;DR sentence (STRICT MAX %d chars) starting with ONE emoji
   - Captures the essence of the news

8) "fun_fact": ONE interesting fact about Denmark (STRICT MAX %d chars)
   - Ukrainian, start with ONE emoji
   - MUST be unrelated to this specific news topic
   - General interesting fact about Danish Kingdom
   - Make it surprising, unique, or lesser-known. Avoid overused facts like the age of the monarchy, hygge, Lego origins, bicycle culture, or tourist attractions.

ABSOLUTE PROHIBITIONS:
- No "(Примітка: ...)" or translator commentary
- No explanations like "це означає"
- No hashtags in danish/ukrainian (tags field is separate)
- DO NOT repeat or paraphrase the title in danish/ukrainian fields!

Output valid JSON only.
	`, title, content, budget.DanishChars, budget.UkrainianChars, budget.TLDRChars, budget.FunFactChars)

	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		// 1) Detailed API error diagnostics
		if gErr, ok := err.(*googleapi.Error); ok {
			log.Printf("🛑 Gemini API Error Details:\n\tHTTP Code: %d\n\tMessage: %s\n\tRaw Body: %s", gErr.Code, gErr.Message, string(gErr.Body))
		}
		return nil, fmt.Errorf("failed to generate content: %w", err)
	}

	// 2) Prompt safety blocking diagnostics
	if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != 0 {
		log.Printf("🛑 Gemini blocked the PROMPT. Reason: %s", resp.PromptFeedback.BlockReason.String())
		return nil, fmt.Errorf("prompt blocked by safety filters: %s", resp.PromptFeedback.BlockReason.String())
	}

	// 3) Candidate finish reason diagnostics
	if len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("empty response from Gemini (no candidates)")
	}

	candidate := resp.Candidates[0]
	if candidate.FinishReason != genai.FinishReasonStop {
		log.Printf("⚠️ Gemini candidate finished unusually. Reason: %s", candidate.FinishReason.String())
		switch candidate.FinishReason {
		case genai.FinishReasonSafety:
			log.Printf("🛑 Content blocked due to SAFETY settings in response")
			return nil, fmt.Errorf("content generation blocked (Safety)")
		case genai.FinishReasonRecitation:
			log.Printf("🛑 Content blocked due to RECITATION (Copyright/Memorization)")
			return nil, fmt.Errorf("content generation blocked (Recitation)")
		}
	}

	if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini returned candidates but no content parts")
	}

	var parsedResp NewsTranslationResponse
	for _, part := range candidate.Content.Parts {
		if txt, ok := part.(genai.Text); ok {
			// Sanitize response to remove Markdown code blocks if present
			sanitized := sanitizeAIResponse(string(txt))

			if err := json.Unmarshal([]byte(sanitized), &parsedResp); err != nil {
				log.Printf("❌ Failed to unmarshal Gemini JSON response.\n\tRaw: %s\n\tSanitized: %s\n\tError: %v", string(txt), sanitized, err)
				return nil, fmt.Errorf("failed to parse JSON response: %w", err)
			}
			break
		}
	}

	return &NewsTranslation{
		Summary:        parsedResp.Summary,
		Danish:         parsedResp.Danish,
		Ukrainian:      parsedResp.Ukrainian,
		TitleUkrainian: parsedResp.TitleUkrainian,
		Mood:           parsedResp.Mood,
		Tags:           parsedResp.Tags,
		TLDR:           parsedResp.TLDR,
		FunFact:        parsedResp.FunFact,
	}, nil
}
