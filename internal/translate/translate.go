package translate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// SanitizeAIText removes common AI disclaimer lines (e.g., "Note: This translation is a machine translation ...")
func SanitizeAIText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// Normalize newlines
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	// First, remove inline/bracketed disclaimer segments while keeping the rest of the text
	s = removeInlineDisclaimers(s)

	lines := strings.Split(s, "\n")
	filtered := make([]string, 0, len(lines))
	// Only skip lines that START with a disclaimer keyword; inline parts were already stripped
	patStart := regexp.MustCompile(`(?i)^\s*[(\[]?\s*(note|disclaimer)\b`)
	for _, ln := range lines {
		l := strings.TrimSpace(ln)
		if l == "" {
			continue
		}
		// Strip any residual inline disclaimers on this line, then decide
		l = removeInlineDisclaimers(l)
		if l == "" {
			continue
		}
		if patStart.MatchString(l) {
			// skip full-line disclaimer
			continue
		}
		filtered = append(filtered, l)
	}
	out := strings.Join(filtered, "\n")
	// Remove surrounding quotes or backticks
	out = strings.Trim(out, "`\"")
	// Collapse excessive spaces introduced by removals
	out = regexp.MustCompile(`\s{2,}`).ReplaceAllString(out, " ")
	return strings.TrimSpace(out)
}

// removeInlineDisclaimers strips parenthesized/bracketed or sentence-like disclaimer fragments
// without removing the rest of the content on the same line.
func removeInlineDisclaimers(s string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	// Patterns to remove:
	// 1) Parenthesized or bracketed disclaimer segments like (Note: ...), [Note: ...]
	reParen := regexp.MustCompile(`(?i)\s*[(\[]\s*(?:note|disclaimer|this translation is a machine translation|ai language model)[^)\]]*[)\]]\s*`)
	s = reParen.ReplaceAllString(s, " ")
	// 2) Sentence-level notes starting with Note: or Disclaimer: up to a sentence end
	reSentence := regexp.MustCompile(`(?i)(?:^|[.!?]\s+)(?:note|disclaimer)\s*:\s*[^.!?]*[.!?]`)
	s = reSentence.ReplaceAllString(s, " ")
	// 3) Generic disclaimer phrases like "machine translation ..." sentences
	rePhrase := regexp.MustCompile(`(?i)(?:^|\s)(?:this translation is a machine translation|ai language model|double[- ]?check|for accurate translations)[^.!?\n]*[.!?]`)
	s = rePhrase.ReplaceAllString(s, " ")
	// Cleanup extra spaces and newlines that may remain
	s = strings.TrimSpace(s)
	s = regexp.MustCompile(`\s{2,}`).ReplaceAllString(s, " ")
	return s
}

// TranslateText translates text with best available service
func TranslateText(text, from, to string) (string, error) {
	// If text is empty, return as is
	if text == "" {
		return text, nil
	}

	// Normalize target language codes we support
	target := strings.ToLower(strings.TrimSpace(to))
	switch target {
	case "uk", "ukrainian":
		target = "uk"
	case "da", "danish":
		target = "da"
	default:
		// Unsupported target -> return original
		return text, nil
	}

	// Clean text for translation
	text = cleanTextForTranslation(text)

	// Limit text length for API
	originalText := text
	if len(text) > 4000 {
		text = text[:4000] + "..."
	}

	// Try providers in order (fast/free first or as configured)
	if result, err := translateWithGemini(text, from, target); err == nil && result != "" && result != text {
		result = SanitizeAIText(result)
		log.Printf("✅ Gemini API %s->%s ok", from, target)
		return result, nil
	} else {
		log.Printf("⚠️ Gemini API not work for %s->%s: %v", from, target, err)
	}

	if result, err := translateWithGroq(text, from, target); err == nil && result != "" && result != text {
		result = SanitizeAIText(result)
		log.Printf("✅ Groq API %s->%s ok", from, target)
		return result, nil
	} else {
		log.Printf("⚠️ Groq API not work for %s->%s: %v", from, target, err)
	}

	if result, err := translateWithCohere(text, from, target); err == nil && result != "" && result != text {
		result = SanitizeAIText(result)
		log.Printf("✅ Cohere API %s->%s ok", from, target)
		return result, nil
	} else {
		log.Printf("⚠️ Cohere API not work for %s->%s: %v", from, target, err)
	}

	if result, err := translateWithMistralAI(text, from, target); err == nil && result != "" && result != text {
		result = SanitizeAIText(result)
		log.Printf("✅ Mistral AI %s->%s ok", from, target)
		return result, nil
	} else {
		log.Printf("⚠️ Mistral AI not work for %s->%s: %v", from, target, err)
	}

	// Finally try Google Translate as ultimate fallback (FREE!)
	if result, err := translateWithGoogleTranslate(text, from, target); err == nil && result != "" && result != text {
		result = SanitizeAIText(result)
		log.Printf("✅ Google Translate %s->%s ok", from, target)
		return result, nil
	} else {
		log.Printf("⚠️ Google Translate not work for %s->%s: %v", from, target, err)
	}

	log.Printf("⚠️ All translation services not work for %s->%s, use original", from, target)
	return originalText, nil
}

func languageName(code string) string {
	switch strings.ToLower(code) {
	case "uk":
		return "Ukrainian"
	case "da":
		return "Danish"
	default:
		return code
	}
}

// translateWithGemini uses Gemini API for high-quality translation
func translateWithGemini(text, from, to string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return "", errors.New("GEMINI_API_KEY not set")
	}

	// Gemini API endpoint - используем самую новую стабильную версию Gemini 2.5 Flash
	apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key=%s", apiKey)

	// Create translation prompt
	targetName := languageName(to)
	prompt := fmt.Sprintf(`Translate the following text from %s to %s. Return ONLY the translation, no explanations:\n\n%s`, from, targetName, text)

	// Request payload
	payload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{
						"text": prompt,
					},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"temperature":     0.1,
			"maxOutputTokens": 1000,
		},
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("error marshaling request: %v", err)
	}

	// Create HTTP client with timeout
	client := &http.Client{Timeout: 30 * time.Second}

	// Make request
	resp, err := client.Post(apiURL, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return "", fmt.Errorf("HTTP error: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close Gemini response body: %v", closeErr)
		}
	}()

	if resp.StatusCode == 429 {
		return "", fmt.Errorf("quota exceeded (too many requests)")
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gemini API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("error parsing response: %v", err)
	}

	// Extract translation
	candidates, ok := response["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return "", errors.New("no candidates in response")
	}

	candidate := candidates[0].(map[string]interface{})
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return "", errors.New("no content in candidate")
	}

	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return "", errors.New("no parts in content")
	}

	part := parts[0].(map[string]interface{})
	translatedText, ok := part["text"].(string)
	if !ok {
		return "", errors.New("no text in part")
	}

	return strings.TrimSpace(translatedText), nil
}

// translateWithGroq uses Groq API (FREE and very fast)
func translateWithGroq(text, from, to string) (string, error) {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		return "", errors.New("GROQ_API_KEY not set")
	}

	// Groq API endpoint
	apiURL := "https://api.groq.com/openai/v1/chat/completions"

	// Create translation prompt
	targetName := languageName(to)
	prompt := fmt.Sprintf(`Translate the following text from %s to %s. Return ONLY the translation, no explanations or additional text:\n\n%s`, from, targetName, text)

	// Request payload - ОБНОВЛЕНА МОДЕЛЬ!
	payload := map[string]interface{}{
		"model": "llama-3.1-8b-instant", // Новая актуальная модель Groq
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": prompt,
			},
		},
		"temperature": 0.1,
		"max_tokens":  1000,
		"top_p":       1,
		"stream":      false,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("error marshaling request: %v", err)
	}

	// Create HTTP client with timeout
	client := &http.Client{Timeout: 30 * time.Second}

	// Create request
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// Make request
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP error: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close Groq response body: %v", closeErr)
		}
	}()

	if resp.StatusCode == 429 {
		return "", fmt.Errorf("quota exceeded (too many requests)")
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("groq API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("error parsing response: %v", err)
	}

	// Extract translation
	choices, ok := response["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return "", errors.New("no choices in response")
	}

	choice := choices[0].(map[string]interface{})
	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		return "", errors.New("no message in choice")
	}

	content, ok := message["content"].(string)
	if !ok {
		return "", errors.New("no content in message")
	}

	return strings.TrimSpace(content), nil
}

// translateWithCohere uses Cohere API (FREE 100 requests/month)
func translateWithCohere(text, from, to string) (string, error) {
	apiKey := os.Getenv("COHERE_API_KEY")
	if apiKey == "" {
		return "", errors.New("COHERE_API_KEY not set")
	}

	// Cohere API endpoint
	apiURL := "https://api.cohere.ai/v1/generate"

	// Create translation prompt
	targetName := languageName(to)
	prompt := fmt.Sprintf(`Translate from %s to %s. Return only the translation:\n\n%s\n\n%s translation:`, from, targetName, text, targetName)

	// Request payload
	payload := map[string]interface{}{
		"model":              "command-light", // Free tier model
		"prompt":             prompt,
		"max_tokens":         500,
		"temperature":        0.1,
		"k":                  0,
		"stop_sequences":     []string{},
		"return_likelihoods": "NONE",
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("error marshaling request: %v", err)
	}

	// Create HTTP client with timeout
	client := &http.Client{Timeout: 30 * time.Second}

	// Create request
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// Make request
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP error: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close Cohere response body: %v", closeErr)
		}
	}()

	if resp.StatusCode == 429 {
		return "", fmt.Errorf("quota exceeded (too many requests)")
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("cohere API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("error parsing response: %v", err)
	}

	// Extract translation
	generations, ok := response["generations"].([]interface{})
	if !ok || len(generations) == 0 {
		return "", errors.New("no generations in response")
	}

	generation := generations[0].(map[string]interface{})
	text_result, ok := generation["text"].(string)
	if !ok {
		return "", errors.New("no text in generation")
	}

	return strings.TrimSpace(text_result), nil
}

// translateWithMistralAI uses Mistral AI (FREE tier available)
func translateWithMistralAI(text, from, to string) (string, error) {
	apiKey := os.Getenv("MISTRALAI_API_KEY")
	if apiKey == "" {
		return "", errors.New("MISTRALAI_API_KEY not set")
	}

	// Mistral AI endpoint
	apiURL := "https://api.mistral.ai/v1/chat/completions"

	// Create translation prompt
	targetName := languageName(to)
	prompt := fmt.Sprintf(`Translate the following text from %s to %s. Return ONLY the translation, no explanations:\n\n%s`, from, targetName, text)

	// Request payload for Mistral AI
	payload := map[string]interface{}{
		"model": "mistral-tiny", // Free tier model
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": prompt,
			},
		},
		"temperature": 0.1,
		"max_tokens":  1000,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("error marshaling request: %v", err)
	}

	// Create HTTP client with timeout
	client := &http.Client{Timeout: 30 * time.Second}

	// Create request
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// Make request
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP error: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close Mistral response body: %v", closeErr)
		}
	}()

	if resp.StatusCode == 429 {
		return "", fmt.Errorf("quota exceeded (too many requests)")
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Mistral AI API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("error parsing response: %v", err)
	}

	// Extract translation from Mistral AI response
	choices, ok := response["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return "", errors.New("no choices in response")
	}

	choice := choices[0].(map[string]interface{})
	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		return "", errors.New("no message in choice")
	}

	content, ok := message["content"].(string)
	if !ok {
		return "", errors.New("no content in message")
	}

	return strings.TrimSpace(content), nil
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

// SummarizeText produces a short, neutral summary in the requested language code (e.g., "da", "uk")
func SummarizeText(text, lang string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		lang = "da"
	}

	// Clean and truncate
	input := cleanTextForTranslation(text)
	if len(input) > 4500 {
		input = input[:4500] + "..."
	}

	if s, err := summarizeWithGroq(input, lang); err == nil && strings.TrimSpace(s) != "" {
		return SanitizeAIText(s), nil
	} else {
		log.Printf("⚠️ Groq summarize failed: %v", err)
	}
	if s, err := summarizeWithCohere(input, lang); err == nil && strings.TrimSpace(s) != "" {
		return SanitizeAIText(s), nil
	} else {
		log.Printf("⚠️ Cohere summarize failed: %v", err)
	}
	if s, err := summarizeWithMistral(input, lang); err == nil && strings.TrimSpace(s) != "" {
		return SanitizeAIText(s), nil
	} else {
		log.Printf("⚠️ Mistral summarize failed: %v", err)
	}
	return "", fmt.Errorf("all summarizers failed")
}

func summarizeWithGroq(text, lang string) (string, error) {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		return "", errors.New("GROQ_API_KEY not set")
	}
	apiURL := "https://api.groq.com/openai/v1/chat/completions"
	prompt := fmt.Sprintf("Summarize the text in %s in 3-4 concise sentences. No preface, no lists, plain text.\n\nTEXT:\n%s", languageName(lang), text)
	payload := map[string]interface{}{
		"model": "llama-3.1-8b-instant", // ОБНОВЛЕНА МОДЕЛЬ!
		"messages": []map[string]interface{}{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.2,
		"max_tokens":  600,
	}
	jsonPayload, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close Groq summarize response body: %v", closeErr)
		}
	}()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("groq summarize status %d: %s", resp.StatusCode, string(b))
	}
	b, _ := io.ReadAll(resp.Body)
	var response map[string]interface{}
	if err := json.Unmarshal(b, &response); err != nil {
		return "", err
	}
	choices, _ := response["choices"].([]interface{})
	if len(choices) == 0 {
		return "", fmt.Errorf("no choices")
	}
	choice := choices[0].(map[string]interface{})
	message := choice["message"].(map[string]interface{})
	content := message["content"].(string)
	return strings.TrimSpace(content), nil
}

func summarizeWithCohere(text, lang string) (string, error) {
	apiKey := os.Getenv("COHERE_API_KEY")
	if apiKey == "" {
		return "", errors.New("COHERE_API_KEY not set")
	}
	apiURL := "https://api.cohere.ai/v1/generate"
	prompt := fmt.Sprintf("Summarize the following text in %s in 3-4 concise sentences. No lists, no meta text.\n\nTEXT:\n%s\n\nSummary:", languageName(lang), text)
	payload := map[string]interface{}{
		"model":       "command-light",
		"prompt":      prompt,
		"max_tokens":  500,
		"temperature": 0.2,
	}
	jsonPayload, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close Cohere summarize response body: %v", closeErr)
		}
	}()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("cohere summarize status %d: %s", resp.StatusCode, string(b))
	}
	b, _ := io.ReadAll(resp.Body)
	var response map[string]interface{}
	if err := json.Unmarshal(b, &response); err != nil {
		return "", err
	}
	gens, _ := response["generations"].([]interface{})
	if len(gens) == 0 {
		return "", fmt.Errorf("no generations")
	}
	gen := gens[0].(map[string]interface{})
	textOut, _ := gen["text"].(string)
	return strings.TrimSpace(textOut), nil
}

func summarizeWithMistral(text, lang string) (string, error) {
	apiKey := os.Getenv("MISTRALAI_API_KEY")
	if apiKey == "" {
		return "", errors.New("MISTRALAI_API_KEY not set")
	}
	apiURL := "https://api.mistral.ai/v1/chat/completions"
	prompt := fmt.Sprintf("Summarize the text in %s in 3-4 concise sentences. No bullet points.\n\nTEXT:\n%s", languageName(lang), text)
	payload := map[string]interface{}{
		"model":       "mistral-tiny",
		"messages":    []map[string]interface{}{{"role": "user", "content": prompt}},
		"temperature": 0.2,
		"max_tokens":  600,
	}
	jsonPayload, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close Mistral summarize response body: %v", closeErr)
		}
	}()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("mistral summarize status %d: %s", resp.StatusCode, string(b))
	}
	b, _ := io.ReadAll(resp.Body)
	var response map[string]interface{}
	if err := json.Unmarshal(b, &response); err != nil {
		return "", err
	}
	choices, _ := response["choices"].([]interface{})
	if len(choices) == 0 {
		return "", fmt.Errorf("no choices")
	}
	choice := choices[0].(map[string]interface{})
	message := choice["message"].(map[string]interface{})
	content := message["content"].(string)
	return strings.TrimSpace(content), nil
}
