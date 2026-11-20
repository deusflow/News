package groq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/logger"
)

type Client struct {
	name            string
	apiKeys         []string
	keyIdx          int
	model           string
	fallbackModel   string
	baseURL         string
	reasoningEffort string
	httpClient      *http.Client
}

type responseFormat struct {
	Type string `json:"type"`
}

func NewClient(apiKey, model string) *Client {
	baseURL := os.Getenv("GROQ_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.groq.com/openai/v1/chat/completions"
	}
	if model == "" {
		model = "openai/gpt-oss-120b"
	}
	return NewClientWithOptions("groq", apiKey, model, baseURL)
}

// NewClientWithOptions creates an OpenAI-compatible client for any endpoint (Groq, Cerebras, OpenRouter, etc.)
func NewClientWithOptions(name, apiKey, model, baseURL string) *Client {
	var keys []string
	if apiKey != "" {
		for _, k := range strings.Split(apiKey, ",") {
			k = strings.TrimSpace(k)
			if k != "" {
				keys = append(keys, k)
			}
		}
	}
	return NewClientWithKeys(name, keys, model, baseURL)
}

// NewClientWithKeys creates a client with multiple API keys for rotation.
func NewClientWithKeys(name string, apiKeys []string, model, baseURL string) *Client {
	if name == "" {
		name = "groq"
	}
	if baseURL == "" {
		baseURL = "https://api.groq.com/openai/v1/chat/completions"
	}
	if model == "" {
		model = "openai/gpt-oss-120b"
	}

	var validKeys []string
	for _, k := range apiKeys {
		k = strings.TrimSpace(k)
		if k != "" {
			validKeys = append(validKeys, k)
		}
	}

	fallbackModel := ""

	reasoningEffort := os.Getenv("GROQ_REASONING_EFFORT")
	if reasoningEffort == "" && (strings.Contains(model, "gpt-oss") || strings.Contains(model, "reasoning")) {
		// Keep reasoning effort low so gpt-oss-120b does not burn the completion budget on hidden thoughts
		// and truncate JSON mid-document.
		reasoningEffort = "low"
	}
	return &Client{
		name:            name,
		apiKeys:         validKeys,
		model:           model,
		fallbackModel:   fallbackModel,
		baseURL:         baseURL,
		reasoningEffort: reasoningEffort,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (c *Client) SetReasoningEffort(effort string) {
	c.reasoningEffort = effort
}

func (c *Client) Name() string {
	if c.name == "" {
		return "groq"
	}
	return c.name
}

func (c *Client) Close() {
	// HTTP client не требует закрытия
}

type groqRequest struct {
	Model               string          `json:"model"`
	Messages            []message       `json:"messages"`
	Temperature         float64         `json:"temperature"`
	Stream              bool            `json:"stream"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	ResponseFormat      *responseFormat `json:"response_format,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type groqResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (c *Client) currentKey() string {
	if len(c.apiKeys) == 0 {
		return ""
	}
	return c.apiKeys[c.keyIdx%len(c.apiKeys)]
}

func (c *Client) rotateKey() {
	if len(c.apiKeys) > 1 {
		c.keyIdx = (c.keyIdx + 1) % len(c.apiKeys)
		logger.Warn("Groq switching to next API key", "provider", c.Name(), "next_key_idx", c.keyIdx)
	}
}

func (c *Client) isOpenRouter() bool {
	return strings.Contains(strings.ToLower(c.baseURL), "openrouter") ||
		strings.HasPrefix(strings.ToLower(c.Name()), "openrouter")
}

func isTransientStatusOrBody(status int, body string) bool {
	if status == http.StatusServiceUnavailable {
		return true
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "503") || strings.Contains(lower, "unavailable") || strings.Contains(lower, "overloaded")
}

func isJSONValidateFailed(status int, body string) bool {
	if status != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "json_validate_failed") || strings.Contains(lower, "failed to generate json")
}

func (c *Client) sendChatRequest(ctx context.Context, title, activeKey string, reqBody groqRequest) (string, int, string, error) {
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, "", fmt.Errorf("failed to marshal groq request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		logger.Error("❌ Groq Request Creation Error", "title", title, "error", err)
		return "", 0, "", err
	}

	if activeKey != "" {
		req.Header.Set("Authorization", "Bearer "+activeKey)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.isOpenRouter() {
		req.Header.Set("HTTP-Referer", "https://github.com/deusflow/News")
		req.Header.Set("X-Title", "Danish News Bot")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logger.Error("❌ Groq HTTP Error", "title", title, "error", err)
		return "", 0, "", fmt.Errorf("groq request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		bodyStr := string(body)
		logger.Error("❌ Groq API Error", "status", resp.StatusCode, "body", bodyStr, "model", reqBody.Model)

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := 0
			if h := strings.TrimSpace(resp.Header.Get("Retry-After")); h != "" {
				if v, convErr := strconv.Atoi(h); convErr == nil {
					retryAfter = v
				}
			}
			if retryAfter <= 0 {
				if parsed, ok := ai.ParseRetryAfterSeconds(bodyStr); ok {
					retryAfter = parsed
				}
			}
			return "", resp.StatusCode, bodyStr, ai.NewRateLimitedError(c.Name(), fmt.Errorf("groq api error %d: %s", resp.StatusCode, bodyStr), time.Duration(retryAfter)*time.Second)
		}

		return "", resp.StatusCode, bodyStr, fmt.Errorf("groq api error %d: %s", resp.StatusCode, bodyStr)
	}

	var groqResp groqResponse
	decodeErr := json.NewDecoder(resp.Body).Decode(&groqResp)
	_ = resp.Body.Close()
	if decodeErr != nil {
		logger.Error("❌ Groq Decode Error", "title", title, "error", decodeErr)
		return "", http.StatusOK, "", fmt.Errorf("groq decode error: %w", decodeErr)
	}

	if len(groqResp.Choices) == 0 {
		logger.Error("❌ Groq No Choices", "title", title)
		return "", http.StatusOK, "", fmt.Errorf("groq returned no choices")
	}

	return groqResp.Choices[0].Message.Content, http.StatusOK, "", nil
}

func (c *Client) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*ai.Response, error) {
	logger.Debug("🔄 Groq Generate", "title", title, "content_length", len(content), "model", c.model)

	finalPrompt := userPrompt + "\n\nIMPORTANT: Return ONLY valid JSON. No markdown formatting."
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = "You are a helpful news assistant. Output valid JSON only."
	}

	modelsToTry := []string{c.model}
	if c.fallbackModel != "" && c.fallbackModel != c.model {
		modelsToTry = append(modelsToTry, c.fallbackModel)
	}

	keyAttempts := len(c.apiKeys)
	if keyAttempts == 0 {
		keyAttempts = 1
	}

	var lastErr error

	for _, activeModel := range modelsToTry {
		for k := 0; k < keyAttempts; k++ {
			activeKey := c.currentKey()

			var respFmt *responseFormat
			maxTokens := 0
			if !c.isOpenRouter() {
				respFmt = &responseFormat{Type: "json_object"}
				maxTokens = 3072
			}

			reqBody := groqRequest{
				Model:               activeModel,
				Messages:            []message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: finalPrompt}},
				Temperature:         0.3,
				Stream:              false,
				MaxCompletionTokens: maxTokens,
				ResponseFormat:      respFmt,
				ReasoningEffort:     c.reasoningEffort,
			}

			rawText, status, bodyStr, err := c.sendChatRequest(ctx, title, activeKey, reqBody)
			if err != nil && reqBody.ResponseFormat != nil && isJSONValidateFailed(status, bodyStr) {
				logger.Warn("Groq json_validate_failed, retrying without response_format", "model", activeModel)
				reqBody.ResponseFormat = nil
				if strings.Contains(strings.ToLower(bodyStr), "max completion tokens") && reqBody.MaxCompletionTokens < 4096 {
					reqBody.MaxCompletionTokens = 4096
				}
				rawText, status, bodyStr, err = c.sendChatRequest(ctx, title, activeKey, reqBody)
			}

			if err != nil {
				if status == http.StatusTooManyRequests {
					c.rotateKey()
					lastErr = err
					continue
				}
				if status == 0 {
					lastErr = err
					continue
				}
				if isTransientStatusOrBody(status, bodyStr) {
					c.rotateKey()
					lastErr = fmt.Errorf("groq transient error %d: %s", status, bodyStr)
					continue
				}
				return nil, err
			}

			jsonText := ai.SanitizeJSON(rawText)

			var data ai.Response
			if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
				logger.Error("❌ Groq JSON Parse Error", "raw", jsonText, "error", err)
				return nil, fmt.Errorf("failed to parse JSON: %w", err)
			}

			logger.Debug("✅ Groq Success", "title", title, "model", activeModel)
			return &data, nil
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("all groq keys/models failed")
}

// GenerateRaw returns the raw text output from the model without attempting JSON unmarshaling.
func (c *Client) GenerateRaw(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	messages := []message{}
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, message{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, message{Role: "user", Content: userPrompt})

	modelsToTry := []string{c.model}
	if c.fallbackModel != "" && c.fallbackModel != c.model {
		modelsToTry = append(modelsToTry, c.fallbackModel)
	}

	keyAttempts := len(c.apiKeys)
	if keyAttempts == 0 {
		keyAttempts = 1
	}

	rawEffort := c.reasoningEffort
	if rawEffort != "" {
		rawEffort = "low"
	}
	maxTokens := 0
	if !c.isOpenRouter() {
		maxTokens = 512
	}

	var lastErr error

	for _, activeModel := range modelsToTry {
		for k := 0; k < keyAttempts; k++ {
			activeKey := c.currentKey()

			reqBody := groqRequest{
				Model:               activeModel,
				Messages:            messages,
				Temperature:         0.3,
				MaxCompletionTokens: maxTokens,
				ReasoningEffort:     rawEffort,
			}

			rawText, status, bodyStr, err := c.sendChatRequest(ctx, "triage", activeKey, reqBody)
			if err != nil {
				if status == http.StatusTooManyRequests {
					c.rotateKey()
					lastErr = err
					continue
				}
				if status == 0 {
					lastErr = err
					continue
				}
				if isTransientStatusOrBody(status, bodyStr) {
					c.rotateKey()
					lastErr = fmt.Errorf("groq transient error %d: %s", status, bodyStr)
					continue
				}
				return "", err
			}

			return rawText, nil
		}
	}

	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("all groq keys/models failed")
}

