package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/logger"
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// Client wraps the Gemini generative model.
// Rate limiting is intentionally NOT handled here — it is the Manager's
// responsibility to serialise requests and enforce inter-request delays.
// Having a second rate limiter here would cause double-waiting and a
// timer goroutine leak (ticker.Stop() was never called before).
type Client struct {
	genaiClient *genai.Client
	model       *genai.GenerativeModel
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

	return &Client{
		genaiClient: client,
		model:       model,
	}, nil
}

func (c *Client) Name() string {
	return "gemini"
}

func (c *Client) Close() {
	c.genaiClient.Close()
}

func (c *Client) Generate(ctx context.Context, title, content, prompt string) (*ai.Response, error) {
	logger.Debug("🔄 Gemini Generate", "title", title, "content_length", len(content))

	resp, err := c.model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		logger.Error("❌ Gemini API Error", "title", title, "error", err)
		return nil, fmt.Errorf("gemini generate error: %w", err)
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		logger.Error("❌ Gemini Empty Response", "title", title)
		return nil, fmt.Errorf("gemini returned empty response")
	}

	var sb strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if txt, ok := part.(genai.Text); ok {
			sb.WriteString(string(txt))
		}
	}
	rawText := sb.String()

	// Очистка JSON (модель иногда оборачивает ответ в ```json ... ```)
	jsonText := strings.TrimSpace(rawText)
	jsonText = strings.ReplaceAll(jsonText, "```json", "")
	jsonText = strings.ReplaceAll(jsonText, "```", "")
	jsonText = strings.TrimSpace(jsonText)

	var data ai.Response
	if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
		logger.Error("❌ Gemini JSON Parse Error", "raw", jsonText)
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	logger.Debug("✅ Gemini Success", "title", title)
	return &data, nil
}
