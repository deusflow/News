package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
)

type Client struct {
	apiKey string
	model  string
	client *http.Client
}

func NewClient(cfg *config.Config) *Client {
	// Prefer explicit env var for GROQ key; fallback to cfg fields if present
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" && cfg != nil {
		// cfg may or may not contain GroqAPIKey depending on branch; try a safe read via exported getter not available — so skip
		// To avoid build-time dependency, prefer env var. If needed, set cfg.GroqAPIKey in config package.
	}
	return &Client{
		apiKey: apiKey,
		model:  "mistral-large-latest",
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// GenerateContent выполняет запрос к API и возвращает JSON строку
func (c *Client) GenerateContent(ctx context.Context, prompt string) (string, error) {
	if c.apiKey == "" {
		logger.Error("API key is empty")
		return "", fmt.Errorf("API key is empty")
	}

	reqBody := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: "You are a news editor backend. Output valid JSON only."},
			{Role: "user", Content: prompt},
		},
		Temperature:    0.1,
		ResponseFormat: &responseFormat{Type: "json_object"},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		logger.Error("Failed to marshal request body", "error", err)
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		logger.Error("Failed to create HTTP request", "error", err)
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		logger.Error("HTTP request failed", "error", err)
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		logger.Error("API returned non-200 status", "status", resp.StatusCode, "body", string(body))
		return "", fmt.Errorf("api error %d: %s", resp.StatusCode, string(body))
	}

	var result chatResponse
	if err := json.Unmarshal(body, &result); err != nil {
		logger.Error("Failed to unmarshal response", "error", err, "body", string(body))
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if result.Error != nil {
		logger.Error("API returned error in response", "message", result.Error.Message)
		return "", fmt.Errorf("api returned error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		logger.Error("API returned empty choices")
		return "", fmt.Errorf("empty choices")
	}

	return cleanJSONString(result.Choices[0].Message.Content), nil
}

func cleanJSONString(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```", "")
	return s
}
