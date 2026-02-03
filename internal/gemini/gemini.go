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
	client        *genai.Client
	cache         *cache.Cache
	metrics       *metrics.Metrics
	modelName     string
	fallbackModel string           // gemini-2.0-flash as fallback
	rateLimiter   <-chan time.Time // Rate limiter: 1 request per interval
	rateInterval  time.Duration    // Interval between requests
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
	Summary   string   `json:"summary"`
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
		modelName = "gemini-2.5-flash" // always points to latest flash version
	}

	fallbackModel := "gemini-2.0-flash" // fallback if primary model fails

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

	// 2. Try primary model first
	retryConfig := retry.RetryConfig{
		MaxAttempts: 2, // Reduced attempts for primary model
		Delay:       5 * time.Second,
		Backoff:     true,
	}

	err = retry.WithRetry(ctx, retryConfig, func() error {
		result, err = c.translateWithModel(ctx, c.modelName, title, content)

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
			result, err = c.translateWithModel(ctx, c.fallbackModel, title, content)
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

func (c *Client) translateWithModel(ctx context.Context, modelName, title, content string) (*NewsTranslation, error) {
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
	You are a skilled Ukrainian journalist working in Denmark. Your job is to write news for Ukrainians living in Denmark.
	
	TITLE: %s
	CONTENT: %s
	
	TASKS:
	1. "summary": Create a concise summary (max 1500 chars) capturing the key facts.
	
	2. "danish": Rewrite the news in Danish.
	   - Use natural, journalistic Danish.
	   - Keep it informative but engaging.
	   - Max 800 characters.
	
	3. "ukrainian": Write this news AS IF you are a Ukrainian journalist telling this to a friend.
	   
	   STRICT RULES:
	   - Write NATURALLY, like spoken Ukrainian. Use живу мову!
	   - NEVER add notes, explanations, or translator comments like "(Примітка: ...)"
	   - NEVER explain what words mean in parentheses
	   - If a Danish term needs context, weave it into the sentence naturally
	     BAD: "Folketing (данський парламент)"
	     GOOD: "данський парламент Фолькетинг"
	   - Keep the text CLEAN - no meta-commentary about translation
	   - Use Ukrainian idioms and expressions where appropriate
	   - The reader is Ukrainian living in Denmark - they understand both cultures
	   - Max 800 characters.
	   
	   TONE: Informative but friendly, like news from a trusted friend.
	
	4. "mood": Determine the emotional tone. Options: "positive", "negative", "neutral", "shocking", "urgent".
	
	5. "tags": Extract 2-4 relevant keywords in Ukrainian (e.g., "Політика", "Економіка", "Біженці", "Діти", "Сім'я").
	
	6. "tldr": ONE punchy sentence (max 100 chars) in Ukrainian. Start with emoji.
	   Example: "🏛️ Данія виділила 2 млрд на оборону"
	
	7. "fun_fact": ONE interesting fact about Denmark in Ukrainian (max 120 chars), related to the news topic. Start with emoji.
	   Examples:
	   - "🚴 У Копенгагені більше велосипедів, ніж людей"
	   - "🎓 Освіта в Данії безкоштовна навіть для іноземців"
	
	ABSOLUTE PROHIBITIONS:
	- NO "(Примітка: ...)" or any translator notes
	- NO "означає" explanations mid-sentence
	- NO word-by-word translations
	- NO robotic language
	- NO commentary about the translation process
	
	Output valid JSON only.
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
		Mood:      parsedResp.Mood,
		Tags:      parsedResp.Tags,
		TLDR:      parsedResp.TLDR,
		FunFact:   parsedResp.FunFact,
	}, nil
}
