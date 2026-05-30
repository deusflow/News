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
	clients []*genai.Client
	models  []*genai.GenerativeModel
	keyIdx  int
}

func NewClient(apiKeys []string, modelName string) (*Client, error) {
	ctx := context.Background()

	if modelName == "" {
		modelName = "gemini-2.5-flash"
	}

	var clients []*genai.Client
	var models []*genai.GenerativeModel

	for _, key := range apiKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		c, err := genai.NewClient(ctx, option.WithAPIKey(key))
		if err != nil {
			return nil, fmt.Errorf("failed to create gemini client: %v", err)
		}

		m := c.GenerativeModel(modelName)
		m.SetTemperature(0.3)

		clients = append(clients, c)
		models = append(models, m)
	}

	if len(clients) == 0 {
		return nil, fmt.Errorf("no valid gemini api keys provided")
	}

	return &Client{
		clients: clients,
		models:  models,
	}, nil
}

func (c *Client) Name() string {
	return "gemini"
}

func (c *Client) Close() {
	for _, cl := range c.clients {
		cl.Close()
	}
}

func (c *Client) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*ai.Response, error) {
	logger.Debug("🔄 Gemini Generate", "title", title, "content_length", len(content))

	attempts := len(c.models)

	for i := 0; i < attempts; i++ {
		model := c.models[c.keyIdx]
		if strings.TrimSpace(systemPrompt) != "" {
			model.SystemInstruction = &genai.Content{Parts: []genai.Part{genai.Text(systemPrompt)}}
		} else {
			model.SystemInstruction = nil
		}
		resp, err := model.GenerateContent(ctx, genai.Text(userPrompt))

		if err == nil {
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

			jsonText := strings.TrimSpace(rawText)
			jsonText = strings.ReplaceAll(jsonText, "```json", "")
			jsonText = strings.ReplaceAll(jsonText, "```", "")
			jsonText = strings.TrimSpace(jsonText)

			var data ai.Response
			if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
				logger.Error("❌ Gemini JSON Parse Error", "raw", jsonText)
				return nil, fmt.Errorf("failed to parse JSON: %w", err)
			}

			logger.Debug("✅ Gemini Success", "title", title, "key_idx", c.keyIdx)
			return &data, nil
		}

		logger.Error("❌ Gemini API Error", "title", title, "key_idx", c.keyIdx, "error", err)
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "429") || strings.Contains(lower, "rate") || strings.Contains(lower, "quota") {
			// Switch key automatically
			c.keyIdx = (c.keyIdx + 1) % len(c.models)
			logger.Warn("Gemini 429/Quota limit hit. Switching key.", "next_key_idx", c.keyIdx)
			continue
		}

		// If it's a non-429 error, just fail this model and try to fallback to Groq via manager?
		// Wait, if it's a different error, maybe we shouldn't try next keys.
		return nil, fmt.Errorf("gemini generate error: %w", err)
	}

	// If we exhausted all keys by getting 429, don't return original error with potential retry delays,
	// so the manager directly skips to Groq
	return nil, fmt.Errorf("all gemini keys exhausted due to rate limits")
}
