package gemini

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

type Client struct {
	client *genai.Client
}

type NewsTranslation struct {
	Summary   string
	Danish    string
	Ukrainian string
}

func NewClient(apiKey string) (*Client, error) {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	return &Client{client: client}, nil
}

func (c *Client) Close() {
	if c.client != nil {
		c.client.Close()
	}
}

func (c *Client) TranslateAndSummarizeNews(title, content string) (*NewsTranslation, error) {
	ctx := context.Background()
	model := c.client.GenerativeModel("gemini-1.5-flash")

	prompt := fmt.Sprintf(`
Анализируй эту новость и выполни следующие задачи:

НОВОСТЬ:
Заголовок: %s
Содержание: %s

ЗАДАЧИ:
1. Создай краткую версию новости (до 1900 символов)
2. Переведи суть на датский язык (естественно и точно)
3. Переведи суть на украинский язык (естественно и точно)

ФОРМАТ ОТВЕТА:
СУТЬ: [краткая суть новости]
ДАТСКИЙ: [перевод на датский]
УКРАИНСКИЙ: [перевод на украинский]

Важно: переводы должны быть естественными, а не дословными. Сохраняй смысл и важные детали, не переводи названия брендов.
`, title, content)

	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return nil, fmt.Errorf("failed to generate content: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no response from Gemini")
	}

	response := fmt.Sprintf("%v", resp.Candidates[0].Content.Parts[0])
	return parseGeminiResponse(response)
}

func parseGeminiResponse(response string) (*NewsTranslation, error) {
	lines := strings.Split(response, "\n")

	var summary, danish, ukrainian string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "СУТЬ:") {
			summary = strings.TrimSpace(strings.TrimPrefix(line, "СУТЬ:"))
		} else if strings.HasPrefix(line, "ДАТСКИЙ:") {
			danish = strings.TrimSpace(strings.TrimPrefix(line, "ДАТСКИЙ:"))
		} else if strings.HasPrefix(line, "УКРАИНСКИЙ:") {
			ukrainian = strings.TrimSpace(strings.TrimPrefix(line, "УКРАИНСКИЙ:"))
		}
	}

	// Fallback parsing if the format is different
	if summary == "" || danish == "" || ukrainian == "" {
		log.Printf("Warning: Could not parse Gemini response properly. Raw response: %s", response)

		parts := strings.Split(response, "\n")
		if len(parts) >= 3 {
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part != "" {
					if summary == "" {
						summary = part
					} else if danish == "" {
						danish = part
					} else if ukrainian == "" {
						ukrainian = part
						break
					}
				}
			}
		}
	}

	if summary == "" || danish == "" || ukrainian == "" {
		return nil, fmt.Errorf("could not parse Gemini response: missing required fields")
	}

	return &NewsTranslation{
		Summary:   summary,
		Danish:    danish,
		Ukrainian: ukrainian,
	}, nil
}
