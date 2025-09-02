package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
)

// TranslateTextUkr translates text with best available service
func TranslateTextUkr(text, from, to string) (string, error) {
	// If text is empty, return as is
	if text == "" {
		return text, nil
	}

	// Only support translate to Ukrainian
	if to != "uk" && to != "ukrainian" {
		return text, nil
	}

	// Clean text for translation
	text = cleanTextForTranslation(text)

	// Limit text length for API
	originalText := text
	if len(text) > 4000 {
		text = text[:4000] + "..."
	}

	// First try Google Translate (FREE!)
	result, err := translateWithGoogleTranslate(text, from, to)
	if err == nil && result != "" && result != text {
		log.Printf("✅ Google Translate %s->%s ok", from, to)
		return result, nil
	}
	log.Printf("⚠️ Google Translate not work for %s->%s: %v", from, to, err)

	// Then try OpenAI (if token is set)
	if openaiToken := os.Getenv("OPENAI_API_KEY"); openaiToken != "" {
		result, err := translateWithOpenAI(text, from, to)
		if err == nil && result != "" && result != text {
			log.Printf("✅ OpenAI translate %s->%s ok", from, to)
			return result, nil
		}
		log.Printf("⚠️ OpenAI not work for %s->%s: %v", from, to, err)
	}

	log.Printf("⚠️ All translate services not work for %s->%s, use original", from, to)
	return originalText, nil
}

// translateWithGoogleTranslate uses FREE Google Translate API
func translateWithGoogleTranslate(text, from, to string) (string, error) {
	// Use public Google Translate endpoint (free)
	baseURL := "https://translate.googleapis.com/translate_a/single"

	// Build query params
	params := url.Values{}
	params.Set("client", "gtx")
	params.Set("sl", from) // source language: use parameter
	params.Set("tl", to)   // target language: use parameter
	params.Set("dt", "t")  // return translations
	params.Set("q", text)

	fullURL := baseURL + "?" + params.Encode()

	// Create HTTP client with timeout
	client := &http.Client{Timeout: 15 * time.Second}

	// Make request
	resp, err := client.Get(fullURL)
	if err != nil {
		return "", fmt.Errorf("HTTP error: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("google Translate API returned status: %d", resp.StatusCode)
	}

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}

	// Parse JSON response from Google Translate
	translation, err := parseGoogleTranslateResponse(body)
	if err != nil {
		return "", fmt.Errorf("error parsing response: %v", err)
	}

	return translation, nil
}

// parseGoogleTranslateResponse parses Google Translate API response
func parseGoogleTranslateResponse(body []byte) (string, error) {
	// Google Translate returns array of arrays
	var response []interface{}

	if err := json.Unmarshal(body, &response); err != nil {
		return "", err
	}

	if len(response) == 0 {
		return "", errors.New("empty response from Google Translate")
	}

	// First element contains translations
	translations, ok := response[0].([]interface{})
	if !ok {
		return "", errors.New("unexpected response format")
	}

	var result strings.Builder

	// Collect all translation parts
	for _, translation := range translations {
		if translationArray, ok := translation.([]interface{}); ok && len(translationArray) > 0 {
			if translatedText, ok := translationArray[0].(string); ok {
				result.WriteString(translatedText)
			}
		}
	}

	return result.String(), nil
}

// cleanTextForTranslation cleans text before translation
func cleanTextForTranslation(text string) string {
	// Remove repeating phrases from Ekstra Bladet
	text = strings.ReplaceAll(text, "På Ekstra Bladet lægger vi stor vægt på at have en tæt dialog med jer læsere. Jeres input er guld", "")
	text = strings.ReplaceAll(text, "værd, og mange historier ville ikke kunne lade sig gøre uden jeres tip. Men selv om vi også har", "")
	text = strings.ReplaceAll(text, "tradition for at turde, når andre tier, værner vi om en sober og konstruktiv tone.", "")
	text = strings.ReplaceAll(text, "Ekstra Bladet og evt. politianmeldt.", "")

	// Remove extra spaces and newlines
	lines := strings.Split(text, "\n")
	var cleanLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && len(line) > 5 {
			cleanLines = append(cleanLines, line)
		}
	}

	return strings.Join(cleanLines, " ")
}

// translateWithOpenAI performs quality AI translation through OpenAI (only to Ukrainian)
func translateWithOpenAI(text, from, to string) (string, error) {
	if to != "uk" && to != "ukrainian" {
		return text, nil
	}

	client := openai.NewClient(os.Getenv("OPENAI_API_KEY"))

	// Use the from parameter to determine source language for better translation
	sourceLang := "Danish"
	if from == "en" {
		sourceLang = "English"
	} else if from == "de" {
		sourceLang = "German"
	} else if from == "sv" {
		sourceLang = "Swedish"
	} else if from == "no" {
		sourceLang = "Norwegian"
	}

	prompt := fmt.Sprintf(`Translate the following %s news text to Ukrainian language. 
Keep the meaning, tone and journalistic style of the original.
Translate only the text itself, without additional comments.
Use modern Ukrainian vocabulary.

Text to translate:
%s`, sourceLang, text)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: openai.GPT3Dot5Turbo,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: prompt,
			},
		},
		MaxCompletionTokens: 2000,
	})

	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", errors.New("no response from OpenAI")
	}

	translation := strings.TrimSpace(resp.Choices[0].Message.Content)
	return translation, nil
}
