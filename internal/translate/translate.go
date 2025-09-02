package translate

import (
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
)

// TranslateText translates text with best available service
func TranslateText(text, from, to string) (string, error) {
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

	// First try Hugging Face MarianMT (FREE and high quality!)
	result, err := translateWithHuggingFace(text, from, to)
	if err == nil && result != "" && result != text {
		log.Printf("✅ Hugging Face MarianMT %s->%s ok", from, to)
		return result, nil
	}
	log.Printf("⚠️ Hugging Face MarianMT not work for %s->%s: %v", from, to, err)

	// Then try Google Translate (FREE!)
	result, err = translateWithGoogleTranslate(text, from, to)
	if err == nil && result != "" && result != text {
		log.Printf("✅ Google Translate %s->%s ok", from, to)
		return result, nil
	}
	log.Printf("⚠️ Google Translate not work for %s->%s: %v", from, to, err)

	log.Printf("⚠️ All FREE translation services not work for %s->%s, use original", from, to)
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

// translateWithHuggingFace uses Hugging Face MarianMT models (FREE and high quality)
func translateWithHuggingFace(text, from, to string) (string, error) {
	// Map language codes to Hugging Face model names
	var modelName string
	switch {
	case from == "da" && (to == "uk" || to == "ukrainian"):
		// Use Finnish-Ugric to Ukrainian model (covers more languages including Danish)
		modelName = "Helsinki-NLP/opus-mt-fiu-uk"
	case from == "en" && (to == "uk" || to == "ukrainian"):
		modelName = "Helsinki-NLP/opus-mt-en-uk"
	default:
		return "", fmt.Errorf("no suitable MarianMT model for %s->%s", from, to)
	}

	// Hugging Face Inference API endpoint
	apiURL := fmt.Sprintf("https://api-inference.huggingface.co/models/%s", modelName)

	// Prepare request body
	requestBody := map[string]interface{}{
		"inputs": text,
		"options": map[string]interface{}{
			"wait_for_model": true,
		},
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("error marshaling request: %v", err)
	}

	// Create HTTP client with timeout
	client := &http.Client{Timeout: 30 * time.Second}

	// Create request
	req, err := http.NewRequest("POST", apiURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")

	// Use HF token if available (increases rate limits)
	if hfToken := os.Getenv("HUGGINGFACE_API_KEY"); hfToken != "" {
		req.Header.Set("Authorization", "Bearer "+hfToken)
	}

	// Make request
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP error: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close HF response body: %v", closeErr)
		}
	}()

	if resp.StatusCode == 503 {
		return "", fmt.Errorf("model loading, try again later")
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("hugging Face API returned status: %d", resp.StatusCode)
	}

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}

	// Parse response
	var response []map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("error parsing response: %v", err)
	}

	if len(response) == 0 {
		return "", errors.New("empty response from Hugging Face")
	}

	// Extract translation
	if translationText, ok := response[0]["translation_text"].(string); ok {
		return strings.TrimSpace(translationText), nil
	}

	return "", errors.New("no translation found in response")
}
