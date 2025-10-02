package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// SendMessage sends text message to Telegram chat/channel with retry logic
func SendMessage(token, chatID, text string) error {
	maxRetries := 3

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := sendMessageOnce(token, chatID, text)
		if err == nil {
			log.Printf("Message sent to Telegram (try %d)", attempt)
			return nil
		}

		log.Printf("Error send to Telegram (try %d/%d): %v", attempt, maxRetries, err)

		if attempt < maxRetries {
			// Exponential backoff: 2^attempt seconds
			waitTime := time.Duration(1<<attempt) * time.Second
			log.Printf("Wait %v before next try...", waitTime)
			time.Sleep(waitTime)
		}
	}

	return fmt.Errorf("can't send message after %d tries", maxRetries)
}

// sendMessageOnce does one try to send message
func sendMessageOnce(token, chatID, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	payload := map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true, // No link preview for clean
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error make JSON: %v", err)
	}

	// Add timeout for HTTP request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("error HTTP request: %v", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
	}(resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("telegram API error: status %d", resp.StatusCode)
	}

	return nil
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
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewBuffer(body))
		if err != nil {
			log.Printf("Error HTTP request (try %d/%d): %v", attempt, maxRetries, err)
		} else {
			func() {
				defer func() {
					if err := resp.Body.Close(); err != nil {
						log.Printf("Warning: failed to close response body: %v", err)
					}
				}()
				if resp.StatusCode == 200 {
					log.Printf("Message with preview sent to Telegram (try %d)", attempt)
					return
				}
				log.Printf("Telegram API error (try %d/%d): status %d", attempt, maxRetries, resp.StatusCode)
			}()
		}
		if attempt < maxRetries {
			waitTime := time.Duration(1<<attempt) * time.Second
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
		err := sendPhotoOnce(token, chatID, photoURL, caption)
		if err == nil {
			log.Printf("Photo sent to Telegram (try %d)", attempt)
			return nil
		}
		log.Printf("Error send photo to Telegram (try %d/%d): %v", attempt, maxRetries, err)
		if attempt < maxRetries {
			waitTime := time.Duration(1<<attempt) * time.Second
			log.Printf("Wait %v before next try...", waitTime)
			time.Sleep(waitTime)
		}
	}
	return fmt.Errorf("can't send photo after %d tries", maxRetries)
}

func sendPhotoOnce(token, chatID, photoURL, caption string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", token)
	// Telegram caption max ~1024 chars; trim if longer
	if len(caption) > 1000 {
		caption = caption[:1000]
	}

	payload := map[string]interface{}{
		"chat_id":    chatID,
		"photo":      photoURL,
		"caption":    caption,
		"parse_mode": "HTML",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error make JSON: %v", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("error HTTP request: %v", err)
	}
	defer func(Body io.ReadCloser) {
		if err := Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
	}(resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("telegram API error: status %d", resp.StatusCode)
	}
	return nil
}
