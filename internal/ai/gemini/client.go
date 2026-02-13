package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai" // Импортируем наш новый пакет
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

type Client struct {
	genaiClient *genai.Client
	model       *genai.GenerativeModel
	rateLimiter <-chan time.Time
}

func NewClient(apiKey, modelName string) (*Client, error) {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create gemini client: %v", err)
	}

	if modelName == "" {
		modelName = "gemini-1.5-flash"
	}

	model := client.GenerativeModel(modelName)
	model.SetTemperature(0.3)

	// Лимит: 1 запрос раз в 5 секунд (12 RPM) - безопасно для Free Tier
	ticker := time.NewTicker(5 * time.Second)

	return &Client{
		genaiClient: client,
		model:       model,
		rateLimiter: ticker.C,
	}, nil
}

func (c *Client) Name() string {
	return "gemini"
}

func (c *Client) Close() {
	c.genaiClient.Close()
}

func (c *Client) Generate(ctx context.Context, title, content, prompt string) (*ai.Response, error) {
	// Ждем своей очереди (Rate Limit)
	select {
	case <-c.rateLimiter:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	resp, err := c.model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return nil, fmt.Errorf("gemini generate error: %w", err)
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("gemini returned empty response")
	}

	var sb strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if txt, ok := part.(genai.Text); ok {
			sb.WriteString(string(txt))
		}
	}
	rawText := sb.String()

	// Очистка JSON
	jsonText := strings.TrimSpace(rawText)
	jsonText = strings.ReplaceAll(jsonText, "```json", "")
	jsonText = strings.ReplaceAll(jsonText, "```", "")
	jsonText = strings.TrimSpace(jsonText)

	var data ai.Response
	if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
		log.Printf("❌ Gemini JSON Parse Error. Raw: %s", jsonText)
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return &data, nil
}
