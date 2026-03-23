package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/deusflow/News/internal/logger"
)

const telegramAPIBase = "https://api.telegram.org/bot"

// Глобальный клиент для переиспользования соединений
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

var (
	hrefURLRegex  = regexp.MustCompile(`href="(https?://[^"]+)"`)
	plainURLRegex = regexp.MustCompile(`https?://[^\s<>"]+`)
)

// SendMessageAllowPreview sends a message allowing web preview
func SendMessageAllowPreview(token string, chatID string, text string) (int, error) {
	return sendMessage(context.Background(), token, chatID, text, nil, true, 0)
}

// SendMessageWithButtons sends a message with inline buttons
func SendMessageWithButtons(token string, chatID string, text string, buttons [][]InlineButton, allowPreview bool, replyToMessageID int) (int, error) {
	return sendMessage(context.Background(), token, chatID, text, buttons, allowPreview, replyToMessageID)
}

func sendMessage(ctx context.Context, token string, chatID string, text string, buttons [][]InlineButton, allowPreview bool, replyToMessageID int) (int, error) {
	url := fmt.Sprintf("%s%s/sendMessage", telegramAPIBase, token)

	body := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}

	if allowPreview {
		if previewURL := extractPreviewURL(text); previewURL != "" {
			body["link_preview_options"] = map[string]interface{}{
				"is_disabled":        false,
				"url":                previewURL,
				"prefer_large_media": true,
				"show_above_text":    true,
			}
		} else {
			body["disable_web_page_preview"] = false
		}
	} else {
		body["disable_web_page_preview"] = true
	}

	if len(buttons) > 0 {
		body["reply_markup"] = map[string]interface{}{
			"inline_keyboard": buttons,
		}
	}
	if replyToMessageID != 0 {
		body["reply_to_message_id"] = replyToMessageID
	}

	return executeRequest(ctx, url, body)
}

func extractPreviewURL(text string) string {
	if text == "" {
		return ""
	}
	if m := hrefURLRegex.FindStringSubmatch(text); len(m) >= 2 {
		return sanitizeURL(m[1])
	}
	if raw := plainURLRegex.FindString(text); raw != "" {
		return sanitizeURL(raw)
	}
	return ""
}

func sanitizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, ".,;:!?)")
	return raw
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

	_, err := executeRequest(context.Background(), url, body)
	return err
}

// retryBaseDelay is the base sleep between network-error retries.
const retryBaseDelay = 2 * time.Second

func executeRequest(ctx context.Context, url string, body map[string]interface{}) (int, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonBody))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			// Network-level error — retry with backoff
			if attempt < maxAttempts-1 {
				sleepWithContext(ctx, retryBaseDelay*time.Duration(attempt+1))
			}
			continue
		}

		switch resp.StatusCode {
		case http.StatusOK:
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
			sleepWithContext(ctx, time.Duration(retryAfter+1)*time.Second)

		case http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			logger.Warn("Telegram server error, will retry", "status", resp.StatusCode, "attempt", attempt+1)
			if attempt < maxAttempts-1 {
				sleepWithContext(ctx, retryBaseDelay*time.Duration(attempt+1))
			} else {
				return 0, fmt.Errorf("telegram server error %d: %s", resp.StatusCode, string(respBody))
			}

		default:
			// 4xx — client error, retrying won't help
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return 0, fmt.Errorf("telegram api error %d: %s", resp.StatusCode, string(respBody))
		}
	}

	return 0, fmt.Errorf("telegram: failed after %d attempts", maxAttempts)
}

// sleepWithContext sleeps for d, but returns immediately if ctx is cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// InlineButton struct
type InlineButton struct {
	Text         string `json:"text"`
	URL          string `json:"url,omitempty"`
	CallbackData string `json:"callback_data,omitempty"`
}
