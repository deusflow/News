package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"google.golang.org/genai"
)

// Client wraps the Gemini unified genai SDK clients with API key rotation.
type Client struct {
	clients   []*genai.Client
	modelName string
	keyIdx    int
}

// NewClient creates a new Gemini client using google.golang.org/genai.
func NewClient(apiKeys []string, modelName string) (*Client, error) {
	ctx := context.Background()

	if modelName == "" {
		modelName = config.DefaultGeminiModel
	}

	var clients []*genai.Client

	for _, key := range apiKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		c, err := genai.NewClient(ctx, &genai.ClientConfig{
			APIKey:  key,
			Backend: genai.BackendGeminiAPI,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create gemini client: %v", err)
		}

		clients = append(clients, c)
	}

	if len(clients) == 0 {
		return nil, fmt.Errorf("no valid gemini api keys provided")
	}

	return &Client{
		clients:   clients,
		modelName: modelName,
	}, nil
}

func (c *Client) Name() string {
	return "gemini"
}

func (c *Client) Close() {
	// genai.Client uses standard HTTP transport under the hood without explicit Close requirements
}

func (c *Client) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*ai.Response, error) {
	logger.Debug("🔄 Gemini Generate", "title", title, "content_length", len(content), "model", c.modelName)

	attempts := len(c.clients)

	for i := 0; i < attempts; i++ {
		client := c.clients[c.keyIdx]

		cfg := &genai.GenerateContentConfig{
			Temperature:      genai.Ptr[float32](0.3),
			ResponseMIMEType: "application/json",
		}
		if strings.TrimSpace(systemPrompt) != "" {
			cfg.SystemInstruction = &genai.Content{
				Parts: []*genai.Part{{Text: systemPrompt}},
			}
		}

		resp, err := client.Models.GenerateContent(ctx, c.modelName, genai.Text(userPrompt), cfg)
		if err == nil {
			if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
				logger.Error("❌ Gemini Empty Response", "title", title)
				return nil, fmt.Errorf("gemini returned empty response")
			}

			rawText := resp.Text()
			jsonText := strings.TrimSpace(rawText)
			jsonText = strings.TrimPrefix(jsonText, "```json")
			jsonText = strings.TrimPrefix(jsonText, "```")
			jsonText = strings.TrimSuffix(jsonText, "```")
			jsonText = strings.TrimSpace(jsonText)

			var data ai.Response
			if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
				logger.Error("❌ Gemini JSON Parse Error", "raw", jsonText, "error", err)
				return nil, fmt.Errorf("failed to parse JSON: %w", err)
			}

			logger.Debug("✅ Gemini Success", "title", title, "key_idx", c.keyIdx)
			return &data, nil
		}

		logger.Error("❌ Gemini API Error", "title", title, "key_idx", c.keyIdx, "error", err)
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "429") || strings.Contains(lower, "rate") || strings.Contains(lower, "quota") || strings.Contains(lower, "resource_exhausted") {
			c.keyIdx = (c.keyIdx + 1) % len(c.clients)
			logger.Warn("Gemini 429/Quota limit hit. Switching key.", "next_key_idx", c.keyIdx)
			continue
		}

		return nil, fmt.Errorf("gemini generate error: %w", err)
	}

	return nil, fmt.Errorf("all gemini keys exhausted due to rate limits")
}

// GenerateRaw returns raw text output for lightweight triage without enforcing JSON schema.
func (c *Client) GenerateRaw(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	attempts := len(c.clients)

	for i := 0; i < attempts; i++ {
		client := c.clients[c.keyIdx]

		cfg := &genai.GenerateContentConfig{
			Temperature: genai.Ptr[float32](0.2),
		}
		if strings.TrimSpace(systemPrompt) != "" {
			cfg.SystemInstruction = &genai.Content{
				Parts: []*genai.Part{{Text: systemPrompt}},
			}
		}

		resp, err := client.Models.GenerateContent(ctx, c.modelName, genai.Text(userPrompt), cfg)
		if err == nil {
			if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
				return "", fmt.Errorf("gemini returned empty response")
			}

			return resp.Text(), nil
		}

		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "429") || strings.Contains(lower, "rate") || strings.Contains(lower, "quota") || strings.Contains(lower, "resource_exhausted") {
			c.keyIdx = (c.keyIdx + 1) % len(c.clients)
			continue
		}

		return "", fmt.Errorf("gemini generate error: %w", err)
	}

	return "", fmt.Errorf("all gemini keys exhausted due to rate limits")
}
