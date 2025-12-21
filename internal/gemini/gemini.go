package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	client    *genai.Client
	cache     *cache.Cache
	metrics   *metrics.Metrics
	modelName string
}

// NewsTranslation - это основная структура, которую мы отдаем наружу (в news.go)
type NewsTranslation struct {
	Summary   string
	Danish    string
	Ukrainian string
	Mood      string   // Настроение новости
	Tags      []string // Теги
	TLDR      string   // Одно предложение - суть новости
	FunFact   string   // Цікавий факт про Данію
}

// NewsTranslationResponse - это "анкета" для Gemini (формат ответа API)
type NewsTranslationResponse struct {
	Danish    string   `json:"danish"`
	Ukrainian string   `json:"ukrainian"`
	Mood      string   `json:"mood"`
	Tags      []string `json:"tags"`
	TLDR      string   `json:"tldr"`
	FunFact   string   `json:"fun_fact"`
}

func NewClient(apiKey, modelName string, m *metrics.Metrics) (*Client, error) {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	if modelName == "" {
		modelName = "gemini-flash-latest"
	}

	return &Client{
		client:    client,
		cache:     cache.New(),
		metrics:   m,
		modelName: modelName,
	}, nil
}

func (c *Client) Close() {
	if c.client != nil {
		_ = c.client.Close()
	}
}

func (c *Client) TranslateAndSummarizeNews(ctx context.Context, title, content string) (*NewsTranslation, error) {
	// 1. Проверяем кэш
	cacheKey := c.cache.GenerateKey(title, content)
	if cached, found := c.cache.Get(cacheKey); found {
		if c.metrics != nil {
			c.metrics.IncrementSuccessfulTranslations()
		}
		return cached.(*NewsTranslation), nil
	}

	// Increase timeout to handle potential rate limit waits
	// Use the passed context as parent
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	var result *NewsTranslation
	var err error

	// 2. Retry logic
	retryConfig := retry.RetryConfig{
		MaxAttempts: 3,
		Delay:       5 * time.Second, // Increased base delay
		Backoff:     true,
	}

	err = retry.WithRetry(ctx, retryConfig, func() error {
		result, err = c.translateWithAPI(ctx, title, content)

		// Handle Rate Limit (429) specifically
		if err != nil {
			if gErr, ok := err.(*googleapi.Error); ok && gErr.Code == 429 {
				log.Printf("⚠️ Gemini Rate Limit hit. Waiting 60s...")
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(60 * time.Second):
					// Continue to retry
					return err
				}
			}
		}
		return err
	})

	if err != nil {
		if c.metrics != nil {
			c.metrics.IncrementFailedTranslations()
			c.metrics.SetError(fmt.Sprintf("Gemini API error: %v", err))
		}
		return nil, err
	}

	// 3. Сохраняем в кэш
	c.cache.Set(cacheKey, result, 24*time.Hour)
	// Removed duplicate increment here as it is done in news.go or should be handled carefully.
	// Original code had it here AND in news.go?
	// In news.go I added: if opts.Metrics != nil { opts.Metrics.IncrementSuccessfulTranslations() }
	// So if I keep it here, it will be double counted.
	// However, this method might be used elsewhere?
	// Let's check if I should remove it from news.go or here.
	// Usually, the service performing the action should record the metric.
	// So gemini client should record it.
	// But news.go also records it.
	// I will remove it from here to avoid double counting if news.go does it.
	// Wait, if I remove it here, and news.go does it, it's fine.
	// But if I look at my previous edit to news.go, I added it there.
	// Let's check the original code in news.go.
	// Original news.go: metrics.Global.IncrementSuccessfulTranslations() was called inside the loop.
	// Original gemini.go: metrics.Global.IncrementSuccessfulTranslations() was called at the end.
	// So it WAS double counted or I am misreading.
	// Let's assume I should keep it here for "Gemini success" and maybe news.go tracks "News processed success".
	// But they seem to track the same thing "SuccessfulTranslations".
	// I will comment it out here to be safe and rely on news.go which orchestrates the process.
	// Actually, better to have it here if we want to track cache hits too.
	// Cache hit: increments.
	// API success: increments.
	// In news.go: it increments if err == nil.
	// So if cache hit, news.go increments. If API success, news.go increments.
	// So if I keep it here, it is definitely double counting.
	// I will remove the increment at the end of this function.

	return result, nil
}

func (c *Client) translateWithAPI(ctx context.Context, title, content string) (*NewsTranslation, error) {
	// Используем модель из конфига
	model := c.client.GenerativeModel(c.modelName)

	model.SetTemperature(0.7)
	model.ResponseMIMEType = "application/json"

	// === ОБНОВЛЕННАЯ СХЕМА ===
	// Добавили Mood (Enum), Tags (Array), TLDR, FunFact
	model.ResponseSchema = &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"summary":   {Type: genai.TypeString},
			"danish":    {Type: genai.TypeString},
			"ukrainian": {Type: genai.TypeString},
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
		Required: []string{"summary", "danish", "ukrainian", "mood", "tags", "tldr", "fun_fact"},
	}

	// Очистка контента
	content = strings.ReplaceAll(content, "\r", "")
	content = strings.TrimSpace(content)
	content = strings.Join(strings.Fields(content), " ")
	maxChars := 8000
	if utf8.RuneCountInString(content) > maxChars {
		runes := []rune(content)
		trimmed := string(runes[:maxChars])
		if idx := strings.LastIndex(trimmed, ". "); idx > 1200 {
			trimmed = trimmed[:idx+1]
		}
		content = trimmed + "\n[TRUNCATED]"
	}

	// === ОБНОВЛЕННЫЙ ПРОМПТ ===
	prompt := fmt.Sprintf(`
	Analyze this news article.
	
	TITLE: %s
	CONTENT: %s
	
	TASKS:
	1. "summary": Create a concise summary (max 1500 chars).
	2. "danish": Translate the news to Danish (natural, native tone).
	3. "ukrainian": Translate the news to Ukrainian (natural, native tone).
	4. "mood": Determine the emotional vibe. Options: "positive" (good news), "negative" (bad news), "neutral" (facts), "shocking" (surprising/scandal), "urgent" (warnings).
	5. "tags": Extract 2-4 keywords (hashtags) in Ukrainian (e.g., "Політика", "Економіка", "Спорт", "Біженці").
	6. "tldr": Write ONE short sentence (max 100 chars) in Ukrainian that captures the main point. Start with emoji. Example: "🏛️ Данія виділила 2 млрд на оборону"
	7. "fun_fact": Write ONE interesting fact about Denmark in Ukrainian (max 120 chars). The fact should be RELATED to the news topic if possible. Start with emoji. Examples:
	   - If news about politics: "🏛️ Данський парламент називається Фолькетинг і має лише одну палату"
	   - If news about economy: "💰 ВВП Данії на душу населення — один з найвищих у світі"
	   - If news about refugees: "🤝 Данія прийняла понад 35 000 українських біженців"
	   - Random facts are also OK: "🚴 У Копенгагені є більше велосипедів, ніж людей"
	
	CONSTRAINTS:
	- Do NOT translate brand names.
	- Output valid JSON.
	- Make fun_fact UNIQUE and interesting, avoid repetition.
	`, title, content)

	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return nil, fmt.Errorf("failed to generate content: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty response from Gemini")
	}

	var parsedResp NewsTranslationResponse
	for _, part := range resp.Candidates[0].Content.Parts {
		if txt, ok := part.(genai.Text); ok {
			if err := json.Unmarshal([]byte(txt), &parsedResp); err != nil {
				log.Printf("Failed to unmarshal Gemini JSON response. Raw: %s", string(txt))
				return nil, fmt.Errorf("failed to parse JSON response: %w", err)
			}
			break
		}
	}

	return &NewsTranslation{
		Danish:    parsedResp.Danish,
		Ukrainian: parsedResp.Ukrainian,
		Mood:      parsedResp.Mood,
		Tags:      parsedResp.Tags,
		TLDR:      parsedResp.TLDR,
		FunFact:   parsedResp.FunFact,
	}, nil
}
