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
	apiKey          string
	model           string
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
	if name == "" {
		name = "groq"
	}
	if baseURL == "" {
		baseURL = "https://api.groq.com/openai/v1/chat/completions"
	}
	reasoningEffort := os.Getenv("GROQ_REASONING_EFFORT")
	if reasoningEffort == "" && (strings.Contains(model, "gpt-oss") || strings.Contains(model, "reasoning")) {
		reasoningEffort = "low"
	}
	return &Client{
		name:            name,
		apiKey:          apiKey,
		model:           model,
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
	Model           string          `json:"model"`
	Messages        []message       `json:"messages"`
	Temperature     float64         `json:"temperature"`
	Stream          bool            `json:"stream"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
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

func (c *Client) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*ai.Response, error) {
	logger.Debug("🔄 Groq Generate", "title", title, "content_length", len(content), "model", c.model)

	// Для Groq лучше явно добавить инструкцию про JSON в конец
	finalPrompt := userPrompt + "\n\nIMPORTANT: Return ONLY valid JSON. No markdown formatting."
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = "You are a helpful news assistant. Output valid JSON only."
	}

	reqBody := groqRequest{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: finalPrompt},
		},
		Temperature:     0.3,
		Stream:          false,
		ResponseFormat:  &responseFormat{Type: "json_object"},
		ReasoningEffort: c.reasoningEffort,
	}

	jsonBody, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		logger.Error("❌ Groq Request Creation Error", "title", title, "error", err)
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logger.Error("❌ Groq HTTP Error", "title", title, "error", err)
		return nil, fmt.Errorf("groq request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		logger.Error("❌ Groq API Error", "status", resp.StatusCode, "body", string(body))
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := 0
			if h := strings.TrimSpace(resp.Header.Get("Retry-After")); h != "" {
				if v, convErr := strconv.Atoi(h); convErr == nil {
					retryAfter = v
				}
			}
			if retryAfter <= 0 {
				if parsed, ok := ai.ParseRetryAfterSeconds(string(body)); ok {
					retryAfter = parsed
				}
			}
			return nil, ai.NewRateLimitedError(c.Name(), fmt.Errorf("groq api error %d: %s", resp.StatusCode, string(body)), time.Duration(retryAfter)*time.Second)
		}
		return nil, fmt.Errorf("groq api error %d: %s", resp.StatusCode, string(body))
	}

	var groqResp groqResponse
	if err := json.NewDecoder(resp.Body).Decode(&groqResp); err != nil {
		logger.Error("❌ Groq Decode Error", "title", title, "error", err)
		return nil, fmt.Errorf("groq decode error: %w", err)
	}

	if len(groqResp.Choices) == 0 {
		logger.Error("❌ Groq No Choices", "title", title)
		return nil, fmt.Errorf("groq returned no choices")
	}

	rawText := groqResp.Choices[0].Message.Content
	// Очистка
	jsonText := strings.TrimSpace(rawText)
	jsonText = strings.ReplaceAll(jsonText, "```json", "")
	jsonText = strings.ReplaceAll(jsonText, "```", "")

	var data ai.Response
	if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
		logger.Error("❌ Groq JSON Parse Error", "raw", jsonText)
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	logger.Debug("✅ Groq Success", "title", title)
	return &data, nil
}

// GenerateRaw returns the raw text output from the model without attempting JSON unmarshaling.
func (c *Client) GenerateRaw(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	messages := []message{}
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, message{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, message{Role: "user", Content: userPrompt})

	reqBody := groqRequest{
		Model:           c.model,
		Messages:        messages,
		Temperature:     0.3,
		ReasoningEffort: c.reasoningEffort,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal groq request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create groq request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("groq http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := 0
			if h := strings.TrimSpace(resp.Header.Get("Retry-After")); h != "" {
				if v, convErr := strconv.Atoi(h); convErr == nil {
					retryAfter = v
				}
			}
			if retryAfter <= 0 {
				if parsed, ok := ai.ParseRetryAfterSeconds(string(body)); ok {
					retryAfter = parsed
				}
			}
			return "", ai.NewRateLimitedError(c.Name(), fmt.Errorf("groq api error %d: %s", resp.StatusCode, string(body)), time.Duration(retryAfter)*time.Second)
		}
		return "", fmt.Errorf("groq api error %d: %s", resp.StatusCode, string(body))
	}

	var groqResp groqResponse
	if err := json.NewDecoder(resp.Body).Decode(&groqResp); err != nil {
		return "", fmt.Errorf("groq decode error: %w", err)
	}

	if len(groqResp.Choices) == 0 {
		return "", fmt.Errorf("groq returned no choices")
	}

	return groqResp.Choices[0].Message.Content, nil
}
