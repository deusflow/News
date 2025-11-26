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

// NewsTranslation - это основная структура, которую мы отдаем наружу (в остальную программу)
type NewsTranslation struct {
	Summary   string
	Danish    string
	Ukrainian string
}

// NewsTranslationResponse - это "анкета" для Gemini.
// JSON-теги (`json:"..."`) обязательны, чтобы Go понял, куда раскладывать данные из ответа модели.
type NewsTranslationResponse struct {
	Summary   string `json:"summary"`
	Danish    string `json:"danish"`
	Ukrainian string `json:"ukrainian"`
}

func NewClient(apiKey string) (*Client, error) {
	ctx := context.Background()
	// Создаем клиента - это наш абонемент в зал Google AI
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
	// 1. Сначала проверяем кэш (чтобы не переплачивать за повторные запросы)
	cacheKey := c.cache.GenerateKey(title, content)
	if cached, found := c.cache.Get(cacheKey); found {
		metrics.Global.IncrementSuccessfulTranslations()
		return cached.(*NewsTranslation), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // Flash работает быстро, 30 сек хватит
	defer cancel()

	var result *NewsTranslation
	var err error

	// 2. Логика повторных попыток (Retry), если сеть моргнула
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

	// 3. Сохраняем успех в кэш на 24 часа
	c.cache.Set(cacheKey, result, 24*time.Hour)
	metrics.Global.IncrementSuccessfulTranslations()

	return result, nil
}

func (c *Client) translateWithAPI(ctx context.Context, title, content string) (*NewsTranslation, error) {
	// ВАЖНО: Используем Gemini 2.5 Flash.
	// Это "легковес" (Men's Physique), который быстрый, эстетичный и обычно входит в бесплатный лимит.
	// Версия 3 Pro была бы "тяжеловесом" за дополнительные деньги.
	model := c.client.GenerativeModel("gemini-2.5-flash")

	// Настройки "креативности". 0.7 - золотая середина.
	model.SetTemperature(0.7)

	// === ГЛАВНОЕ ОБНОВЛЕНИЕ ===
	// Мы говорим модели: "Отвечай ТОЛЬКО в формате JSON"
	model.ResponseMIMEType = "application/json"

	// Мы даем модели строгую схему (чертеж), по которому она должна построить ответ.
	model.ResponseSchema = &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"summary":   {Type: genai.TypeString},
			"danish":    {Type: genai.TypeString},
			"ukrainian": {Type: genai.TypeString},
		},
		Required: []string{"summary", "danish", "ukrainian"},
	}

	// Подготовка текста (очистка от мусора и обрезка лишнего)
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

	// Промпт теперь очень простой, нам не нужно учить модель формату "СУТЬ:", она сама поймет из схемы.
	prompt := fmt.Sprintf(`
	Analyze this news article.
	
	TITLE: %s
	CONTENT: %s
	
	TASKS:
	1. "summary": Create a concise summary (max 1500 chars).
	2. "danish": Translate the news to Danish (natural, native tone).
	3. "ukrainian": Translate the news to Ukrainian (natural, native tone).
	
	CONSTRAINTS:
	- Do NOT translate brand names or proper nouns unless they have established localized versions.
	- Output must be valid JSON matching the provided schema.
	`, title, content)

	// Отправляем запрос
	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return nil, fmt.Errorf("failed to generate content: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty response from Gemini")
	}

	// === РАСПАКОВКА ОТВЕТА ===
	var parsedResp NewsTranslationResponse

	for _, part := range resp.Candidates[0].Content.Parts {
		if txt, ok := part.(genai.Text); ok {
			// Превращаем текст ответа в байты и скармливаем JSON-декодеру
			if err := json.Unmarshal([]byte(txt), &parsedResp); err != nil {
				log.Printf("Failed to unmarshal Gemini JSON response. Raw: %s", string(txt))
				return nil, fmt.Errorf("failed to parse JSON response: %w", err)
			}
			break
		}
	}

	// Возвращаем результат в основной код
	return &NewsTranslation{
		Summary:   parsedResp.Summary,
		Danish:    parsedResp.Danish,
		Ukrainian: parsedResp.Ukrainian,
	}, nil
}
