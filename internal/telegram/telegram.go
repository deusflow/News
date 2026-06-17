package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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

// SendAdminAlert sends a plain text alert to the admin chat ID (bypasses html parsing to prevent tag breakage in logs)
func SendAdminAlert(token string, adminChatID string, text string) error {
	if adminChatID == "" {
		return nil
	}
	url := fmt.Sprintf("%s%s/sendMessage", telegramAPIBase, token)
	body := map[string]interface{}{
		"chat_id":                  adminChatID,
		"text":                     "🚨 <b>NewsBot Alert:</b>\n\n" + escapeHTML(text),
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	_, err := executeRequest(context.Background(), url, body)
	return err
}

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
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

// GetRemoteContentLength tries to determine remote content length using HEAD first,
// then a ranged GET as a fallback for servers that don't honor HEAD.
func GetRemoteContentLength(url string) (int64, error) {
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := httpClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			if cl := resp.Header.Get("Content-Length"); cl != "" {
				if size, parseErr := strconv.ParseInt(cl, 10, 64); parseErr == nil && size > 0 {
					return size, nil
				}
			}
		}
	}

	rangeReq, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	rangeReq.Header.Set("Range", "bytes=0-0")
	rangeResp, err := httpClient.Do(rangeReq)
	if err != nil {
		return 0, err
	}
	defer rangeResp.Body.Close()
	if rangeResp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("range request failed: status %d", rangeResp.StatusCode)
	}
	if cr := rangeResp.Header.Get("Content-Range"); cr != "" {
		if size, parseErr := parseContentRangeTotal(cr); parseErr == nil && size > 0 {
			return size, nil
		}
	}
	return 0, fmt.Errorf("content length unknown")
}

func parseContentRangeTotal(contentRange string) (int64, error) {
	// Expected: "bytes 0-0/12345"
	parts := strings.Split(contentRange, "/")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid content-range: %s", contentRange)
	}
	total := strings.TrimSpace(parts[1])
	if total == "*" {
		return 0, fmt.Errorf("content length unspecified")
	}
	return strconv.ParseInt(total, 10, 64)
}

// InlineButton struct
type InlineButton struct {
	Text         string `json:"text"`
	URL          string `json:"url,omitempty"`
	CallbackData string `json:"callback_data,omitempty"`
}

// SendVideoStream uploads video from an io.Reader via multipart/form-data.
// Used for YouTube streams read into memory pipe.
// caption max 1024 chars (Telegram video caption limit).
func SendVideoStream(token, chatID string, reader io.Reader, size int64, filename, caption string, buttons [][]InlineButton) error {
	apiURL := fmt.Sprintf("%s%s/sendVideo", telegramAPIBase, token)

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer mw.Close()

		// chat_id
		_ = mw.WriteField("chat_id", chatID)
		_ = mw.WriteField("parse_mode", "HTML")
		_ = mw.WriteField("supports_streaming", "true")
		if caption != "" {
			_ = mw.WriteField("caption", caption)
		}
		if len(buttons) > 0 {
			markup, _ := json.Marshal(map[string]interface{}{
				"inline_keyboard": buttons,
			})
			_ = mw.WriteField("reply_markup", string(markup))
		}

		part, err := mw.CreateFormFile("video", filename)
		if err != nil {
			return
		}
		_, _ = io.Copy(part, reader)
	}()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, apiURL, pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	// Use longer timeout for video upload
	uploadClient := &http.Client{Timeout: 120 * time.Second}
	resp, err := uploadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram sendVideo error %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// SendVideoEmbed sends a message with large video link preview above text.
// Works when bot IP is banned by YouTube — Telegram fetches preview itself.
func SendVideoEmbed(token, chatID, videoURL, text string, buttons [][]InlineButton) (int, error) {
	return SendMessageWithButtons(token, chatID, text, buttons, true, 0)
	// Note: extractPreviewURL inside sendMessage will extract videoURL from text.
	// Ensure videoURL is embedded as <a href="videoURL"> or plain URL in text before calling.
}

// SendVideoURL sends a video via a direct URL to Telegram.
func SendVideoURL(token, chatID, videoURL, caption string, buttons [][]InlineButton) error {
	apiURL := fmt.Sprintf("%s%s/sendVideo", telegramAPIBase, token)
	body := map[string]interface{}{
		"chat_id":            chatID,
		"video":              videoURL,
		"caption":            caption,
		"parse_mode":         "HTML",
		"supports_streaming": true,
	}
	if len(buttons) > 0 {
		body["reply_markup"] = map[string]interface{}{"inline_keyboard": buttons}
	}
	_, err := executeRequest(context.Background(), apiURL, body)
	return err
}
