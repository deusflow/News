package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

// telegramClient is a shared HTTP client with connection pooling for better performance
var telegramClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

// handleRateLimit handles 429 Too Many Requests by respecting Retry-After header
// Returns wait duration. If resp is nil or not 429, returns defaultWait.
func handleRateLimit(resp *http.Response, defaultWait time.Duration) time.Duration {
	if resp == nil || resp.StatusCode != 429 {
		return defaultWait
	}

	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		log.Printf("Rate limit 429 but no Retry-After header, using default backoff")
		return defaultWait
	}

	seconds, err := strconv.Atoi(retryAfter)
	if err != nil {
		log.Printf("Failed to parse Retry-After '%s': %v, using default backoff", retryAfter, err)
		return defaultWait
	}

	// Add 1 second buffer to be safe
	waitTime := time.Duration(seconds+1) * time.Second
	log.Printf("Rate limit 429: Telegram requested wait %d seconds, waiting %v", seconds, waitTime)
	return waitTime
}

// SendMessage sends text message to Telegram chat/channel with retry logic
func SendMessage(token, chatID, text string) error {
	maxRetries := 3

	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := sendMessageOnce(token, chatID, text)
		if err == nil {
			log.Printf("Message sent to Telegram (try %d)", attempt)
			return nil
		}

		log.Printf("Error send to Telegram (try %d/%d): %v", attempt, maxRetries, err)

		if attempt < maxRetries {
			// Exponential backoff: 2^attempt seconds
			defaultWait := time.Duration(1<<attempt) * time.Second
			waitTime := handleRateLimit(resp, defaultWait)
			log.Printf("Wait %v before next try...", waitTime)
			time.Sleep(waitTime)
		}
	}

	return fmt.Errorf("can't send message after %d tries", maxRetries)
}

// sendMessageOnce does one try to send message and returns response for rate limit handling
func sendMessageOnce(token, chatID, text string) (*http.Response, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	payload := map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true, // No link preview for clean
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("error make JSON: %v", err)
	}

	// Use shared client for connection pooling
	resp, err := telegramClient.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("error HTTP request: %v", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
	}(resp.Body)

	if resp.StatusCode != 200 {
		return resp, fmt.Errorf("telegram API error: status %d", resp.StatusCode)
	}

	return nil, nil
}

// SendMessageAllowPreview sends text message and allows link previews (disable_web_page_preview=false)
func SendMessageAllowPreview(token, chatID, text string) error {
	maxRetries := 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
		payload := map[string]interface{}{
			"chat_id":                  chatID,
			"text":                     text,
			"parse_mode":               "HTML",
			"disable_web_page_preview": false,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("error make JSON: %v", err)
		}
		resp, err := telegramClient.Post(url, "application/json", bytes.NewBuffer(body))
		if err != nil {
			log.Printf("Error HTTP request (try %d/%d): %v", attempt, maxRetries, err)
		} else {
			// Close body immediately after use, not with defer in loop
			statusOK := resp.StatusCode == 200
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Printf("Warning: failed to close response body: %v", closeErr)
			}

			if statusOK {
				log.Printf("Message with preview sent to Telegram (try %d)", attempt)
				return nil
			}
			log.Printf("Telegram API error (try %d/%d): status %d", attempt, maxRetries, resp.StatusCode)
		}
		if attempt < maxRetries {
			defaultWait := time.Duration(1<<attempt) * time.Second
			waitTime := handleRateLimit(resp, defaultWait)
			log.Printf("Wait %v before next try...", waitTime)
			time.Sleep(waitTime)
		}
	}
	return fmt.Errorf("can't send message with preview after %d tries", maxRetries)
}

// SendPhoto sends a photo with optional caption to Telegram chat/channel with retry logic
func SendPhoto(token, chatID, photoURL, caption string) error {
	maxRetries := 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := sendPhotoOnce(token, chatID, photoURL, caption)
		if err == nil {
			log.Printf("Photo sent to Telegram (try %d)", attempt)
			return nil
		}
		log.Printf("Error send photo to Telegram (try %d/%d): %v", attempt, maxRetries, err)
		if attempt < maxRetries {
			defaultWait := time.Duration(1<<attempt) * time.Second
			waitTime := handleRateLimit(resp, defaultWait)
			log.Printf("Wait %v before next try...", waitTime)
			time.Sleep(waitTime)
		}
	}
	return fmt.Errorf("can't send photo after %d tries", maxRetries)
}

func sendPhotoOnce(token, chatID, photoURL, caption string) (*http.Response, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", token)
	// Telegram caption max ~1024 chars; trim rune-aware if longer
	if utf8.RuneCountInString(caption) > 1024 {
		r := []rune(caption)
		if len(r) > 1024 {
			caption = string(r[:1024])
		}
	}

	payload := map[string]interface{}{
		"chat_id":    chatID,
		"photo":      photoURL,
		"caption":    caption,
		"parse_mode": "HTML",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("error make JSON: %v", err)
	}

	resp, err := telegramClient.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("error HTTP request: %v", err)
	}
	defer func(Body io.ReadCloser) {
		if err := Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
	}(resp.Body)

	if resp.StatusCode != 200 {
		return resp, fmt.Errorf("telegram API error: status %d", resp.StatusCode)
	}
	return nil, nil
}

type InlineButton struct {
	Text         string
	CallbackData string // for callback buttons
	URL          string // for URL buttons (mutually exclusive with CallbackData)
}

// SendMessageWithButtons sends a message with optional inline buttons and returns message_id.
func SendMessageWithButtons(token, chatID, text string, buttons [][]InlineButton, allowPreview bool, replyTo int) (int, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": !allowPreview,
	}
	if replyTo > 0 {
		payload["reply_to_message_id"] = replyTo
	}
	if len(buttons) > 0 {
		var kb [][]map[string]interface{}
		for _, row := range buttons {
			var kbRow []map[string]interface{}
			for _, btn := range row {
				btnMap := map[string]interface{}{"text": btn.Text}
				if btn.URL != "" {
					btnMap["url"] = btn.URL
				} else if btn.CallbackData != "" {
					btnMap["callback_data"] = btn.CallbackData
				}
				kbRow = append(kbRow, btnMap)
			}
			kb = append(kb, kbRow)
		}
		payload["reply_markup"] = map[string]interface{}{"inline_keyboard": kb}
	}
	body, _ := json.Marshal(payload)

	// Use shared client for connection pooling
	resp, err := telegramClient.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return 0, fmt.Errorf("error HTTP request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("telegram API error: status %d", resp.StatusCode)
	}
	respBody, _ := io.ReadAll(resp.Body)
	var decoded map[string]interface{}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return 0, fmt.Errorf("decode error: %v", err)
	}
	res, _ := decoded["result"].(map[string]interface{})
	midFloat, _ := res["message_id"].(float64)
	return int(midFloat), nil
}

// SendPhotoWithButtons sends a photo with caption and inline buttons (reply_markup) if provided.
func SendPhotoWithButtons(token, chatID, photoURL, caption string, buttons [][]InlineButton) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", token)
	// Trim caption to 1024 runes (Telegram limit for photo captions)
	if utf8.RuneCountInString(caption) > 1024 {
		r := []rune(caption)
		caption = string(r[:1024])
	}
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"photo":      photoURL,
		"caption":    caption,
		"parse_mode": "HTML",
	}
	if len(buttons) > 0 {
		var kb [][]map[string]interface{}
		for _, row := range buttons {
			var kbRow []map[string]interface{}
			for _, btn := range row {
				btnMap := map[string]interface{}{"text": btn.Text}
				if btn.URL != "" {
					btnMap["url"] = btn.URL
				} else if btn.CallbackData != "" {
					btnMap["callback_data"] = btn.CallbackData
				}
				kbRow = append(kbRow, btnMap)
			}
			kb = append(kb, kbRow)
		}
		payload["reply_markup"] = map[string]interface{}{"inline_keyboard": kb}
	}
	body, _ := json.Marshal(payload)
	resp, err := telegramClient.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("error HTTP request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("telegram API error: status %d", resp.StatusCode)
	}
	return nil
}
