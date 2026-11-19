package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"google.golang.org/genai"
)

// perAttemptTimeout caps each single Gemini API call so a hung 503/connection
// never stalls the entire GitHub Actions job.
const perAttemptTimeout = 15 * time.Second

// Client wraps the Gemini unified genai SDK clients with API key rotation and model fallback.
type Client struct {
	clients        []*genai.Client
	modelName      string
	fallbackModels []string
	keyIdx         int
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

	var fallbacks []string
	for _, fb := range []string{"gemini-3.6-flash", "gemini-3.5-flash-lite"} {
		if !strings.EqualFold(fb, modelName) {
			fallbacks = append(fallbacks, fb)
		}
	}

	return &Client{
		clients:        clients,
		modelName:      modelName,
		fallbackModels: fallbacks,
	}, nil
}

func (c *Client) Name() string {
	if c.modelName != "" && c.modelName != config.DefaultGeminiModel {
		return "gemini:" + c.modelName
	}
	return "gemini"
}

func (c *Client) Close() {
	// genai.Client uses standard HTTP transport under the hood without explicit Close requirements
}

func isTransient503OrOverload(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "503") ||
		strings.Contains(lower, "504") ||
		strings.Contains(lower, "unavailable") ||
		strings.Contains(lower, "overloaded") ||
		strings.Contains(lower, "high demand") ||
		strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "deadline_exceeded") ||
		strings.Contains(lower, "deadline expired") ||
		strings.Contains(lower, "context deadline")
}

func isQuotaOrRateLimit(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "429") ||
		strings.Contains(lower, "rate") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "resource_exhausted")
}

func (c *Client) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*ai.Response, error) {
	logger.Debug("🔄 Gemini Generate", "title", title, "content_length", len(content), "model", c.modelName)

	modelsToTry := append([]string{c.modelName}, c.fallbackModels...)
	attempts := len(c.clients)

	for _, activeModel := range modelsToTry {
		for i := 0; i < attempts; i++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
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

			attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
			resp, err := client.Models.GenerateContent(attemptCtx, activeModel, genai.Text(userPrompt), cfg)
			cancel()

			if err == nil {
				if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
					logger.Error("❌ Gemini Empty Response", "title", title, "model", activeModel)
					return nil, fmt.Errorf("gemini returned empty response")
				}

				rawText := resp.Text()
				jsonText := ai.SanitizeJSON(rawText)

				var data ai.Response
				if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
					logger.Error("❌ Gemini JSON Parse Error", "raw", jsonText, "error", err)
					return nil, fmt.Errorf("failed to parse JSON: %w", err)
				}

				logger.Debug("✅ Gemini Success", "title", title, "model", activeModel, "key_idx", c.keyIdx)
				return &data, nil
			}

			logger.Error("❌ Gemini API Error", "title", title, "model", activeModel, "key_idx", c.keyIdx, "error", err)

			// If Google's model cluster is globally overloaded (503 / timeout), rotating keys on the same model
			// wastes time. Switch key once and immediately jump to the next fallback Gemini model.
			if isTransient503OrOverload(err) {
				c.keyIdx = (c.keyIdx + 1) % len(c.clients)
				logger.Warn("Gemini model cluster overloaded/timed out (503). Switching to fallback Gemini model.",
					"failed_model", activeModel,
					"next_key_idx", c.keyIdx)
				break
			}

			if isQuotaOrRateLimit(err) {
				c.keyIdx = (c.keyIdx + 1) % len(c.clients)
				logger.Warn("Gemini quota/rate limit hit. Switching key.", "model", activeModel, "next_key_idx", c.keyIdx)
				continue
			}

			// If a fallback model is not found (404), move to the next fallback model
			if strings.Contains(strings.ToLower(err.Error()), "404") || strings.Contains(strings.ToLower(err.Error()), "not found") {
				break
			}

			return nil, fmt.Errorf("gemini generate error: %w", err)
		}
	}

	return nil, ai.NewRateLimitedError(c.Name(), fmt.Errorf("all gemini keys and fallback models exhausted due to rate limits/errors"), 5*time.Minute)
}

// GenerateRaw returns raw text output for lightweight triage without enforcing JSON schema.
func (c *Client) GenerateRaw(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	modelsToTry := append([]string{c.modelName}, c.fallbackModels...)
	attempts := len(c.clients)

	for _, activeModel := range modelsToTry {
		for i := 0; i < attempts; i++ {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			client := c.clients[c.keyIdx]

			cfg := &genai.GenerateContentConfig{
				Temperature: genai.Ptr[float32](0.2),
			}
			if strings.TrimSpace(systemPrompt) != "" {
				cfg.SystemInstruction = &genai.Content{
					Parts: []*genai.Part{{Text: systemPrompt}},
				}
			}

			attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
			resp, err := client.Models.GenerateContent(attemptCtx, activeModel, genai.Text(userPrompt), cfg)
			cancel()

			if err == nil {
				if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
					return "", fmt.Errorf("gemini returned empty response")
				}

				return resp.Text(), nil
			}

			if isTransient503OrOverload(err) {
				c.keyIdx = (c.keyIdx + 1) % len(c.clients)
				break
			}

			if isQuotaOrRateLimit(err) {
				c.keyIdx = (c.keyIdx + 1) % len(c.clients)
				continue
			}

			if strings.Contains(strings.ToLower(err.Error()), "404") || strings.Contains(strings.ToLower(err.Error()), "not found") {
				break
			}

			return "", fmt.Errorf("gemini generate error: %w", err)
		}
	}

	return "", ai.NewRateLimitedError(c.Name(), fmt.Errorf("all gemini keys and fallback models exhausted due to rate limits/errors"), 5*time.Minute)
}
