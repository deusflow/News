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
	"google.golang.org/api/option"
)

type Client struct {
	client *genai.Client
	cache  *cache.Cache
}

// NewsTranslation - это основная структура, которую мы отдаем наружу (в news.go)
type NewsTranslation struct {
	Summary   string
	Danish    string
	Ukrainian string
	Mood      string   // Новый параметр: Настроение новости
	Tags      []string // Новый параметр: Теги
}

// NewsTranslationResponse - это "анкета" для Gemini (формат ответа API)
type NewsTranslationResponse struct {
	Summary   string   `json:"summary"`
	Danish    string   `json:"danish"`
	Ukrainian string   `json:"ukrainian"`
	Mood      string   `json:"mood"`
	Tags      []string `json:"tags"`
}

func NewClient(apiKey string) (*Client, error) {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	return &Client{
		client: client,
		cache:  cache.New(),
	}, nil
}

func (c *Client) Close() {
	if c.client != nil {
		_ = c.client.Close()
	}
}

func (c *Client) TranslateAndSummarizeNews(title, content string) (*NewsTranslation, error) {
	// 1. Проверяем кэш
	cacheKey := c.cache.GenerateKey(title, content)
	if cached, found := c.cache.Get(cacheKey); found {
		metrics.Global.IncrementSuccessfulTranslations()
		return cached.(*NewsTranslation), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var result *NewsTranslation
	var err error

	// 2. Retry logic
	retryConfig := retry.RetryConfig{
		MaxAttempts: 3,
		Delay:       2 * time.Second,
		Backoff:     true,
	}

	err = retry.WithRetry(ctx, retryConfig, func() error {
		result, err = c.translateWithAPI(ctx, title, content)
		return err
	})

	if err != nil {
		metrics.Global.IncrementFailedTranslations()
		metrics.Global.SetError(fmt.Sprintf("Gemini API error: %v", err))
		return nil, err
	}

	// 3. Сохраняем в кэш
	c.cache.Set(cacheKey, result, 24*time.Hour)
	metrics.Global.IncrementSuccessfulTranslations()

	return result, nil
}

func (c *Client) translateWithAPI(ctx context.Context, title, content string) (*NewsTranslation, error) {
	// Используем Flash модель для скорости
	model := c.client.GenerativeModel("gemini-2.5-flash")

	model.SetTemperature(0.7)
	model.ResponseMIMEType = "application/json"

	// === ОБНОВЛЕННАЯ СХЕМА ===
	// Добавили Mood (Enum) и Tags (Array)
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
		},
		Required: []string{"summary", "danish", "ukrainian", "mood", "tags"},
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
	// Мы объясняем модели, как выбирать Mood и Tags
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
	
	CONSTRAINTS:
	- Do NOT translate brand names.
	- Output valid JSON.
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
		Summary:   parsedResp.Summary,
		Danish:    parsedResp.Danish,
		Ukrainian: parsedResp.Ukrainian,
		Mood:      parsedResp.Mood, // Передаем Mood дальше
		Tags:      parsedResp.Tags, // Передаем Tags дальше
	}, nil
}
