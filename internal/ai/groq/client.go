package groq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
)

type Client struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		model:  "llama3-8b-8192", // Быстрая и дешевая модель
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (c *Client) Name() string {
	return "groq"
}

func (c *Client) Close() {
	// HTTP client не требует закрытия
}

type groqRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Temperature float64   `json:"temperature"`
	Stream      bool      `json:"stream"`
	JSONMode    bool      `json:"json_mode,omitempty"` // Groq поддерживает JSON mode
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

func (c *Client) Generate(ctx context.Context, title, content, prompt string) (*ai.Response, error) {
	// Для Groq лучше явно добавить инструкцию про JSON в конец
	finalPrompt := prompt + "\n\nIMPORTANT: Return ONLY valid JSON. No markdown formatting."

	reqBody := groqRequest{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: "You are a helpful news assistant. Output valid JSON only."},
			{Role: "user", Content: finalPrompt},
		},
		Temperature: 0.3,
		Stream:      false,
	}

	jsonBody, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("groq request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("groq api error %d: %s", resp.StatusCode, string(body))
	}

	var groqResp groqResponse
	if err := json.NewDecoder(resp.Body).Decode(&groqResp); err != nil {
		return nil, fmt.Errorf("groq decode error: %w", err)
	}

	if len(groqResp.Choices) == 0 {
		return nil, fmt.Errorf("groq returned no choices")
	}

	rawText := groqResp.Choices[0].Message.Content
	// Очистка
	jsonText := strings.TrimSpace(rawText)
	jsonText = strings.ReplaceAll(jsonText, "```json", "")
	jsonText = strings.ReplaceAll(jsonText, "```", "")

	var data ai.Response
	if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
		log.Printf("❌ Groq JSON Parse Error. Raw: %s", jsonText)
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return &data, nil
}
