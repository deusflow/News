package publisher

import (
	"context"
	"strings"

	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/telegram"
)

// CacheAdapter defines the interface needed for caching and DLQ operations during publishing.
type CacheAdapter interface {
	IsFunFactRecentlyUsed(fact string) bool
	MarkFunFactUsed(fact string) error
	SaveFailedNews(title, link, imageURL, messageText, errorMsg string) error
	MarkAsSentWithContent(hash, title, link, content, category, source string) error
}

// TelegramPublisher handles formatting and sending news to Telegram.
type TelegramPublisher struct {
	cfg          *config.Config
	cacheAdapter CacheAdapter
	metrics      *metrics.Metrics
}

func NewTelegramPublisher(cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics) *TelegramPublisher {
	return &TelegramPublisher{
		cfg:          cfg,
		cacheAdapter: cacheAdapter,
		metrics:      m,
	}
}

// Publish formats and sends a single news item to Telegram, handling images, videos, and fallbacks.
// It returns the final text sent to Telegram, and a boolean indicating if it was successfully sent.
func (p *TelegramPublisher) Publish(ctx context.Context, n news.News, hash string) (string, bool) {
	// 1. Fallback Image
	if n.ImageURL == "" {
		c := news.ValidateCategory(n.Category)
		n.ImageURL = news.GetCategoryImage(c)
		logger.Info("Using default category image", "category", c, "url", n.ImageURL)
	}

	// 2. Fun Fact deduplication
	funFactOriginal := strings.TrimSpace(n.FunFact)
	if funFactOriginal != "" && p.cacheAdapter.IsFunFactRecentlyUsed(funFactOriginal) {
		logger.Info("dropping repeated fun_fact for this run", "title", n.Title)
		n.FunFact = ""
	}

	// 3. Render mode decision
	videoURL := news.ExtractVideoURL(n)
	canPhoto := news.ShouldUsePhoto(n, p.cfg.Posting.PhotoTextLimit)
	if videoURL != "" {
		canPhoto = false
	}

	logger.Info("telegram render mode decision",
		"title", n.Title,
		"has_image", n.ImageURL != "",
		"has_video_url", videoURL != "",
		"use_photo", canPhoto,
		"photo_text_limit", p.cfg.Posting.PhotoTextLimit)

	var outText string
	var err error

	// 4. Buttons
	var buttons [][]telegram.InlineButton
	if p.cfg.Feature.EnableInlineButtons && n.Link != "" {
		buttons = append(buttons, []telegram.InlineButton{
			{Text: "🔗 Читати оригінал / Læs mere", URL: n.Link},
		})
		if videoURL != "" && videoURL != n.Link {
			buttons = append(buttons, []telegram.InlineButton{
				{Text: "🎬 Дивитись відео", URL: videoURL},
			})
		}
	}

	maxVideoSeconds := p.cfg.Posting.VideoMaxSeconds
	if maxVideoSeconds <= 0 {
		maxVideoSeconds = 180
	}
	maxTelegramURLVideoBytes := p.cfg.Posting.VideoURLMaxBytes
	if maxTelegramURLVideoBytes <= 0 {
		maxTelegramURLVideoBytes = 20 * 1024 * 1024
	}

	videoSent := false

	// 5. Video pipeline
	if n.VideoURL != "" {
		duration, durErr := telegram.GetVideoDurationSeconds(n.VideoURL)
		allowNative := false
		if durErr == nil && duration <= maxVideoSeconds {
			allowNative = true
		}

		if allowNative {
			videoCaption := ""
			if canPhoto {
				videoCaption = news.FormatCaptionForPhoto(n, p.cfg.Posting.PhotoTextLimit)
			} else {
				videoCaption = news.FormatCaptionForPhoto(n, 1024)
			}

			if telegram.IsYouTubeURL(n.VideoURL) {
				reader, size, streamErr := telegram.GetYouTubeStream(n.VideoURL)
				if streamErr == nil {
					defer reader.Close()
					err = telegram.SendVideoStream(p.cfg.Telegram.Token, p.cfg.Telegram.ChatID, reader, size, "video.mp4", videoCaption, buttons)
					if err == nil {
						videoSent = true
					}
				}
			} else if telegram.IsDRDirectVideo(n.VideoURL) {
				size, sizeErr := telegram.GetRemoteContentLength(n.VideoURL)
				if sizeErr == nil && size <= maxTelegramURLVideoBytes {
					err = telegram.SendVideoURL(p.cfg.Telegram.Token, p.cfg.Telegram.ChatID, n.VideoURL, videoCaption, buttons)
					if err == nil {
						videoSent = true
					}
				}
			}
		}

		if !videoSent {
			videoCaption := news.FormatNewsWithImage(n)
			textWithLink := videoCaption + "\n\n🎥 " + n.VideoURL
			_, err = telegram.SendVideoEmbed(p.cfg.Telegram.Token, p.cfg.Telegram.ChatID, n.VideoURL, textWithLink, buttons)
			if err == nil {
				videoSent = true
			}
		}
	}

	// 6. Photo/Text pipeline
	if !videoSent {
		if canPhoto {
			outText = news.FormatCaptionForPhoto(n, p.cfg.Posting.PhotoTextLimit)
			if len(buttons) > 0 {
				err = telegram.SendPhotoWithButtons(p.cfg.Telegram.Token, p.cfg.Telegram.ChatID, n.ImageURL, outText, buttons)
			} else {
				err = telegram.SendPhoto(p.cfg.Telegram.Token, p.cfg.Telegram.ChatID, n.ImageURL, outText)
			}
		} else {
			outText = news.FormatNewsWithImage(n)
			if len(buttons) > 0 {
				_, err = telegram.SendMessageWithButtons(p.cfg.Telegram.Token, p.cfg.Telegram.ChatID, outText, buttons, true, 0)
			} else {
				_, err = telegram.SendMessageAllowPreview(p.cfg.Telegram.Token, p.cfg.Telegram.ChatID, outText)
			}
		}
	}

	// 7. Error handling & DLQ
	if err != nil {
		logger.Error("Failed to send telegram message", "title", n.Title, "error", err)
		if saveErr := p.cacheAdapter.SaveFailedNews(n.Title, n.Link, n.ImageURL, outText, err.Error()); saveErr != nil {
			logger.Error("Failed to save to DLQ", "error", saveErr)
		}
		return outText, false
	}

	// 8. Success handling
	_ = p.cacheAdapter.MarkAsSentWithContent(hash, n.Title, n.Link, n.Content, n.Category, n.SourceName)
	if strings.TrimSpace(n.FunFact) != "" {
		_ = p.cacheAdapter.MarkFunFactUsed(n.FunFact)
	}
	p.metrics.IncrementTelegramMessagesSent()

	return outText, true
}
