package telegram

import (
	"bytes"
	"context"
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
	return sendMessage(context.Background(), token, chatID, text, nil, true, 0)
}

// SendMessageWithButtons sends a message with inline buttons
func SendMessageWithButtons(token string, chatID string, text string, buttons [][]InlineButton, allowPreview bool, replyToMessageID int) (int, error) {
	return sendMessage(context.Background(), token, chatID, text, buttons, allowPreview, replyToMessageID)
}

func sendMessage(ctx context.Context, token string, chatID string, text string, buttons [][]InlineButton, allowPreview bool, replyToMessageID int) (int, error) {
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

	return executeRequest(ctx, url, body)
}

// SendPhoto sends a photo with caption
func SendPhoto(token string, chatID string, photoURL string, caption string) (int, error) {
	return SendPhotoWithButtons(token, chatID, photoURL, caption, nil)
}

// SendPhotoWithButtons sends a photo with buttons
func SendPhotoWithButtons(token string, chatID string, photoURL string, caption string, buttons [][]InlineButton) (int, error) {
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

	return executeRequest(context.Background(), url, body)
}

// GetUpdates fetches pending Telegram updates for callback processing.
func GetUpdates(token string, offset int64, limit int) ([]Update, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}

	url := fmt.Sprintf("%s%s/getUpdates", telegramAPIBase, token)
	body := map[string]interface{}{
		"offset":          offset,
		"limit":           limit,
		"timeout":         0,
		"allowed_updates": []string{"callback_query"},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("telegram getUpdates error %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if !parsed.OK {
		return nil, fmt.Errorf("telegram getUpdates returned ok=false")
	}

	return parsed.Result, nil
}

// AnswerCallbackQuery sends a small toast notification in Telegram UI.
func AnswerCallbackQuery(token string, callbackID string, text string) error {
	url := fmt.Sprintf("%s%s/answerCallbackQuery", telegramAPIBase, token)
	body := map[string]interface{}{
		"callback_query_id": callbackID,
		"text":              text,
		"show_alert":        false,
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

// Update is a Telegram update payload.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// CallbackQuery represents button click payload.
type CallbackQuery struct {
	ID   string            `json:"id"`
	From CallbackQueryUser `json:"from"`
	Data string            `json:"data"`
}

// CallbackQueryUser is the Telegram user who clicked a button.
type CallbackQueryUser struct {
	ID int64 `json:"id"`
}
