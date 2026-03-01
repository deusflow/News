package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/deusflow/News/internal/logger"
)

const telegramAPIBase = "https://api.telegram.org/bot"

// Глобальный клиент для переиспользования соединений
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// SendMessageAllowPreview sends a message allowing web preview
func SendMessageAllowPreview(token string, chatID string, text string) (int, error) {
	return sendMessage(token, chatID, text, nil, true, 0)
}

// SendMessageWithButtons sends a message with inline buttons
func SendMessageWithButtons(token string, chatID string, text string, buttons [][]InlineButton, allowPreview bool, replyToMessageID int) (int, error) {
	return sendMessage(token, chatID, text, buttons, allowPreview, replyToMessageID)
}

func sendMessage(token string, chatID string, text string, buttons [][]InlineButton, allowPreview bool, replyToMessageID int) (int, error) {
	url := fmt.Sprintf("%s%s/sendMessage", telegramAPIBase, token)

	body := map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": !allowPreview,
	}

	if len(buttons) > 0 {
		body["reply_markup"] = map[string]interface{}{
			"inline_keyboard": buttons,
		}
	}
	if replyToMessageID != 0 {
		body["reply_to_message_id"] = replyToMessageID
	}

	return executeRequest(url, body)
}

// SendPhoto sends a photo with caption
func SendPhoto(token string, chatID string, photoURL string, caption string) error {
	return SendPhotoWithButtons(token, chatID, photoURL, caption, nil)
}

// SendPhotoWithButtons sends a photo with buttons
func SendPhotoWithButtons(token string, chatID string, photoURL string, caption string, buttons [][]InlineButton) error {
	url := fmt.Sprintf("%s%s/sendPhoto", telegramAPIBase, token)

	body := map[string]interface{}{
		"chat_id":    chatID,
		"photo":      photoURL,
		"caption":    caption,
		"parse_mode": "HTML",
	}

	if len(buttons) > 0 {
		body["reply_markup"] = map[string]interface{}{
			"inline_keyboard": buttons,
		}
	}

	_, err := executeRequest(url, body)
	return err
}

// retryBaseDelay is the base sleep between network-error retries.
const retryBaseDelay = 2 * time.Second

func executeRequest(url string, body map[string]interface{}) (int, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := httpClient.Post(url, "application/json", bytes.NewBuffer(jsonBody))
		if err != nil {
			// Network-level error — retry with backoff
			if attempt < maxAttempts-1 {
				time.Sleep(retryBaseDelay * time.Duration(attempt+1))
			}
			continue
		}

		switch resp.StatusCode {
		case http.StatusOK:
			// Success path
			var response map[string]interface{}
			if decErr := json.NewDecoder(resp.Body).Decode(&response); decErr != nil {
				_ = resp.Body.Close()
				return 0, decErr
			}
			_ = resp.Body.Close()
			if result, ok := response["result"].(map[string]interface{}); ok {
				if mid, ok := result["message_id"].(float64); ok {
					return int(mid), nil
				}
			}
			return 0, nil

		case http.StatusTooManyRequests: // 429 — rate limited
			retryAfter := 5
			if v := resp.Header.Get("Retry-After"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					retryAfter = n
				}
			}
			_ = resp.Body.Close()
			logger.Warn("Telegram rate limit", "wait_seconds", retryAfter, "attempt", attempt+1)
			time.Sleep(time.Duration(retryAfter+1) * time.Second)
			// retry

		case http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout: // 5xx — server-side transient errors, retry
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			logger.Warn("Telegram server error, will retry", "status", resp.StatusCode, "attempt", attempt+1)
			if attempt < maxAttempts-1 {
				time.Sleep(retryBaseDelay * time.Duration(attempt+1))
			} else {
				return 0, fmt.Errorf("telegram server error %d: %s", resp.StatusCode, string(respBody))
			}

		default:
			// 4xx (400, 401, 403, 404 …) — client error, retrying won't help
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return 0, fmt.Errorf("telegram api error %d: %s", resp.StatusCode, string(respBody))
		}
	}

	return 0, fmt.Errorf("telegram: failed after %d attempts", maxAttempts)
}

// InlineButton struct
type InlineButton struct {
	Text         string `json:"text"`
	URL          string `json:"url,omitempty"`
	CallbackData string `json:"callback_data,omitempty"`
}
