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
	"unicode/utf8"

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
		"chat_id":              adminChatID,
		"text":                 "🚨 <b>NewsBot Alert:</b>\n\n" + escapeHTML(text),
		"parse_mode":           "HTML",
		"link_preview_options": map[string]interface{}{"is_disabled": true},
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
			body["link_preview_options"] = map[string]interface{}{
				"is_disabled": false,
			}
		}
	} else {
		body["link_preview_options"] = map[string]interface{}{
			"is_disabled": true,
		}
	}

	if len(buttons) > 0 {
		body["reply_markup"] = map[string]interface{}{
			"inline_keyboard": buttons,
		}
	}
	if replyToMessageID != 0 {
		body["reply_parameters"] = map[string]interface{}{
			"message_id":                  replyToMessageID,
			"allow_sending_without_reply": true,
		}
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

const maxTelegramCaptionRunes = 1024

// SendPhoto sends a photo with caption
func SendPhoto(token string, chatID string, photoURL string, caption string) (int, error) {
	return SendPhotoWithButtons(token, chatID, photoURL, caption, nil)
}

// SendPhotoWithButtons sends a photo with buttons.
// If caption exceeds 1024 runes, it gracefully falls back by sending the photo without caption
// followed by the full caption text and buttons as a separate message, preventing Telegram HTTP 400 Bad Request.
func SendPhotoWithButtons(token string, chatID string, photoURL string, caption string, buttons [][]InlineButton) (int, error) {
	if utf8.RuneCountInString(caption) > maxTelegramCaptionRunes {
		logger.Warn("Telegram photo caption exceeds 1024 runes; falling back to photo + separate text message",
			"caption_len", utf8.RuneCountInString(caption))
		photoURLReq := fmt.Sprintf("%s%s/sendPhoto", telegramAPIBase, token)
		photoMsgID, photoErr := executeRequest(context.Background(), photoURLReq, map[string]interface{}{
			"chat_id": chatID,
			"photo":   photoURL,
		})
		if photoErr == nil {
			return SendMessageWithButtons(token, chatID, caption, buttons, false, photoMsgID)
		}
		logger.Warn("Failed to send photo in caption length fallback; falling back to text message", "error", photoErr)
		return SendMessageWithButtons(token, chatID, caption, buttons, true, 0)
	}

	url := fmt.Sprintf("%s%s/sendPhoto", telegramAPIBase, token)
	body := map[string]interface{}{
		"chat_id":                  chatID,
		"photo":                    photoURL,
		"caption":                  caption,
		"parse_mode":               "HTML",
		"show_caption_above_media": true,
	}

	if len(buttons) > 0 {
		body["reply_markup"] = map[string]interface{}{
			"inline_keyboard": buttons,
		}
	}

	msgID, err := executeRequest(context.Background(), url, body)
	if err != nil && strings.Contains(err.Error(), "caption is too long") {
		logger.Warn("Telegram API rejected caption length; retrying as photo + separate text message", "error", err)
		photoMsgID, photoErr := executeRequest(context.Background(), url, map[string]interface{}{
			"chat_id": chatID,
			"photo":   photoURL,
		})
		if photoErr == nil {
			return SendMessageWithButtons(token, chatID, caption, buttons, false, photoMsgID)
		}
		return SendMessageWithButtons(token, chatID, caption, buttons, true, 0)
	}

	return msgID, err
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
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			var errResp struct {
				Parameters struct {
					RetryAfter int `json:"retry_after"`
				} `json:"parameters"`
			}
			if json.Unmarshal(respBody, &errResp) == nil && errResp.Parameters.RetryAfter > 0 {
				retryAfter = errResp.Parameters.RetryAfter
			} else if v := resp.Header.Get("Retry-After"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					retryAfter = n
				}
			}
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

// CopyTextButton represents Telegram's copy_text button payload (copies text to clipboard on tap, 1-256 chars).
type CopyTextButton struct {
	Text string `json:"text"`
}

// InlineButton struct
type InlineButton struct {
	Text         string          `json:"text"`
	URL          string          `json:"url,omitempty"`
	CallbackData string          `json:"callback_data,omitempty"`
	CopyText     *CopyTextButton `json:"copy_text,omitempty"`
}

// RichBlockTableCell represents a single cell in Telegram Bot API 10.1+ InputRichBlockTable.
type RichBlockTableCell struct {
	Text      string `json:"text,omitempty"`
	IsHeader  bool   `json:"is_header,omitempty"`
	Alignment string `json:"alignment,omitempty"`
	Colspan   int    `json:"colspan,omitempty"`
	Rowspan   int    `json:"rowspan,omitempty"`
}

// RichBlockButton represents a button inside InputRichBlockButtons (Bot API 10.1+).
type RichBlockButton struct {
	Type  string `json:"type"` // "url" or "copy_text"
	Text  string `json:"text"`
	URL   string `json:"url,omitempty"`
	Value string `json:"value,omitempty"` // text to copy when Type == "copy_text"
	Style string `json:"style,omitempty"` // "primary", "success", "danger"
}

// InputRichBlock represents a block in Telegram Bot API 10.1–10.3 InputRichMessage.
type InputRichBlock struct {
	Type       string                 `json:"type"` // "paragraph", "heading", "subheading", "table", "blockquote", "expandable_blockquote", "divider", "buttons", "unordered_list"
	Text       string                 `json:"text,omitempty"`
	Level      int                    `json:"level,omitempty"`
	Items      []InputRichBlock       `json:"items,omitempty"`
	Cells      [][]RichBlockTableCell `json:"cells,omitempty"`
	IsBordered bool                   `json:"is_bordered,omitempty"`
	IsStriped  bool                   `json:"is_striped,omitempty"`
	IsCompact  bool                   `json:"is_compact,omitempty"` // Bot API 10.3 compact table mode
	Buttons    []RichBlockButton      `json:"buttons,omitempty"`
}

// InputRichMessage represents the rich_message payload for sendRichMessage (Bot API 10.1–10.3).
type InputRichMessage struct {
	HTML   string           `json:"html,omitempty"`
	Blocks []InputRichBlock `json:"blocks,omitempty"`
}

// SendRichMessage sends a rich message using Telegram Bot API 10.1–10.3 (/sendRichMessage),
// supporting compact tables (InputRichBlockTable with is_compact: true) and rich blocks.
// If the Bot API server or client does not support sendRichMessage yet, it transparently
// falls back to SendMessageWithButtons / SendMessageAllowPreview so posts never fail.
func SendRichMessage(ctx context.Context, token string, chatID string, richMsg InputRichMessage, fallbackHTML string, buttons [][]InlineButton) (int, error) {
	url := fmt.Sprintf("%s%s/sendRichMessage", telegramAPIBase, token)

	body := map[string]interface{}{
		"chat_id":      chatID,
		"rich_message": richMsg,
		"link_preview_options": map[string]interface{}{
			"is_disabled": true,
		},
	}
	if len(buttons) > 0 {
		body["reply_markup"] = map[string]interface{}{
			"inline_keyboard": buttons,
		}
	}

	msgID, err := executeRequest(ctx, url, body)
	if err == nil {
		return msgID, nil
	}

	logger.Warn("sendRichMessage returned error; falling back to standard HTML sendMessage", "error", err)
	if len(buttons) > 0 {
		return sendMessage(ctx, token, chatID, fallbackHTML, buttons, true, 0)
	}
	return sendMessage(ctx, token, chatID, fallbackHTML, nil, true, 0)
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
		"chat_id":                  chatID,
		"video":                    videoURL,
		"caption":                  caption,
		"parse_mode":               "HTML",
		"supports_streaming":       true,
		"show_caption_above_media": true,
	}
	if len(buttons) > 0 {
		body["reply_markup"] = map[string]interface{}{"inline_keyboard": buttons}
	}
	_, err := executeRequest(context.Background(), apiURL, body)
	return err
}
