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

func executeRequest(url string, body map[string]interface{}) (int, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}

	for attempt := 0; attempt < 3; attempt++ {
		resp, err := httpClient.Post(url, "application/json", bytes.NewBuffer(jsonBody))
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		// Обработка лимитов (429)
		if resp.StatusCode == 429 {
			resp.Body.Close()
			retryAfterStr := resp.Header.Get("Retry-After")
			retryAfter, _ := strconv.Atoi(retryAfterStr)
			if retryAfter == 0 {
				retryAfter = 5
			} // Дефолт

			logger.Warn("Telegram Rate Limit", "wait_seconds", retryAfter)
			time.Sleep(time.Duration(retryAfter+1) * time.Second)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return 0, fmt.Errorf("telegram api error: %s", string(respBody))
		}

		var response map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			resp.Body.Close()
			return 0, err
		}
		resp.Body.Close()

		// Безопасное извлечение message_id
		if result, ok := response["result"].(map[string]interface{}); ok {
			if mid, ok := result["message_id"].(float64); ok {
				return int(mid), nil
			}
		}

		return 0, nil
	}

	return 0, fmt.Errorf("failed after retries")
}

// InlineButton struct
type InlineButton struct {
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}
