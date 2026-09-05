package breaking

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/scraper"
	"github.com/deusflow/News/internal/storage"
	"github.com/deusflow/News/internal/telegram"
)

// DedupeChecker provides duplicate check, cache recording, and delayed post delivery.
type DedupeChecker interface {
	GenerateNewsHash(title, link string) string
	IsAlreadySent(hash string) bool
	IsSourceURLSent(sourceURL string) bool
	MarkAsSentWithContent(hash, title, link, content, category, source string) error
	MarkAsSentWithSemanticData(hash, title, link, content, category, source, titleUA, clusterKey string, emb []float32) error
	GetReadyDelayedPosts(ctx context.Context) ([]storage.DelayedPost, error)
	MarkDelayedPostSent(id int) error
	MarkDelayedPostFailed(id int, errMsg string) error
}

// Run processes a breaking news item received via repository_dispatch, manual trigger,
// or auto-discovered via official Danish emergency feeds (DMI MeteoAlarm / major infrastructure alerts).
func Run(ctx context.Context, cfg *config.Config, aiManager *ai.Manager, cache DedupeChecker) error {
	logger.Info("Starting Breaking News mode")

	url := strings.TrimSpace(os.Getenv("BREAKING_URL"))
	title := strings.TrimSpace(os.Getenv("BREAKING_TITLE"))
	sourceName := "Breaking"
	var alertDescription string

	if url != "" && title != "" {
		logger.Info("Manual breaking news input provided", "url", url, "title", title)
	} else {
		logger.Info("No manual breaking input; scanning DMI and emergency feeds in Denmark")
		detector := NewEmergencyDetector(nil)
		alert, err := detector.ScanForEmergencies(ctx)
		if err != nil {
			logger.Warn("Emergency scan encountered an error", "error", err)
		}
		if alert == nil {
			logger.Info("No active emergency alerts in Denmark, checking delayed posts queue")
			if cache != nil {
				deliverReadyDelayedPosts(ctx, cfg, cache)
			}
			return nil
		}

		url = alert.URL
		title = alert.Title
		sourceName = alert.Source
		alertDescription = alert.Description
		logger.Info("🚨 Emergency alert detected!",
			"title", title,
			"url", url,
			"severity", alert.Severity,
			"source", alert.Source,
			"category", alert.Category)
	}

	// Pre-send deduplication check to prevent double-posting on workflow retries
	if cache != nil {
		if cache.IsSourceURLSent(url) {
			logger.Warn("Breaking news URL was already sent previously, skipping to prevent duplicate", "url", url)
			return nil
		}
		hash := cache.GenerateNewsHash(title, url)
		if cache.IsAlreadySent(hash) {
			logger.Warn("Breaking news hash was already sent previously, skipping to prevent duplicate", "hash", hash)
			return nil
		}
	}

	// Scrape the full article for context (skip if alert URL is generic portal)
	content := ""
	var ac *scraper.ArticleContent
	if url != "" && !strings.HasPrefix(url, "https://www.dmi.dk/varsler/") {
		scrapeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var err error
		ac, err = scraper.ExtractFullArticle(scrapeCtx, url)
		cancel()
		if err == nil && ac != nil && ac.Content != "" {
			content = ac.Content
			logger.Info("Scraped breaking article", "content_len", len(content))
		} else {
			logger.Warn("Could not scrape breaking article, using description/title", "error", err)
		}
	}
	if content == "" && alertDescription != "" {
		content = alertDescription
	}

	var n news.News
	var generated bool

	if aiManager != nil {
		systemPrompt := news.GenerateNewsSystemPrompt()
		userPrompt := news.GenerateNewsUserContent(title, content)

		resp, err := aiManager.Generate(ctx, title, content, systemPrompt, userPrompt)
		if err == nil && resp.Validate() == nil {
			resp.Mood = "urgent"
			n = news.News{
				Title:            title,
				Link:             url,
				Published:        time.Now(),
				Category:         resp.Category,
				SourceName:       sourceName,
				SourceLang:       "da",
				Content:          content,
				Summary:          resp.Summary,
				SummaryDanish:    resp.Danish,
				SummaryUkrainian: resp.Ukrainian,
				TitleDanish:      resp.TitleDanish,
				TitleUkrainian:   resp.TitleUkrainian,
				Mood:             resp.Mood,
				Tags:             resp.Tags,
				TLDR:             resp.TLDR,
				FunFact:          resp.FunFact,
				WhyItMatters:     resp.WhyItMatters,
				IsExclusive:      true,
				AudienceScore:    resp.AudienceScore,
			}
			generated = true
		} else {
			logger.Warn("AI generation failed for breaking news, falling back to emergency template", "error", err)
		}
	}

	// Fallback to deterministic emergency template if AI generation failed or aiManager is unavailable
	if !generated {
		n = buildEmergencyFallbackNews(title, url, sourceName, content)
	}

	// Try to get image from scraper
	if ac != nil && ac.ImageURL != "" {
		n.ImageURL = ac.ImageURL
	}
	if n.ImageURL == "" {
		cat := news.ValidateCategory(n.Category)
		n.ImageURL = news.GetCategoryImage(cat)
	}

	// Build inline button
	var buttons [][]telegram.InlineButton
	if cfg.Feature.EnableInlineButtons && n.Link != "" {
		buttons = append(buttons, []telegram.InlineButton{
			{Text: "🔗 Читати оригінал / Læs mere", URL: n.Link},
		})
	}

	// Format using standard formatter (same look as main channel)
	canPhoto := news.ShouldUsePhoto(n, cfg.Posting.PhotoTextLimit)
	var err error

	if canPhoto {
		caption := news.FormatCaptionForPhoto(n, cfg.Posting.PhotoTextLimit)
		if len(buttons) > 0 {
			_, err = telegram.SendPhotoWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, n.ImageURL, caption, buttons)
		} else {
			_, err = telegram.SendPhoto(cfg.Telegram.Token, cfg.Telegram.ChatID, n.ImageURL, caption)
		}
	} else {
		text := news.FormatNewsWithImage(n)
		if len(buttons) > 0 {
			_, err = telegram.SendMessageWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, text, buttons, true, 0)
		} else {
			_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, text)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to send breaking telegram message: %w", err)
	}

	// Mark as sent in DB cache to guarantee no duplicates
	if cache != nil {
		hash := cache.GenerateNewsHash(title, url)
		if markErr := cache.MarkAsSentWithContent(hash, n.Title, n.Link, n.Content, n.Category, n.SourceName); markErr != nil {
			logger.Error("Failed to mark breaking news as sent in cache", "hash", hash, "error", markErr)
		}
	}

	// Send admin notification about breaking news
	adminMsg := fmt.Sprintf("🚨 Breaking News отправлена:\n%s\n\nURL: %s\nCategory: %s\nAudience Score: %d",
		strings.TrimSpace(n.TitleUkrainian), url, n.Category, n.AudienceScore)
	_ = telegram.SendAdminAlert(cfg.Telegram.Token, cfg.Telegram.AdminChatID, adminMsg)

	logger.Info("Successfully published Breaking News",
		"title", title,
		"category", n.Category,
		"audience_score", n.AudienceScore)
	return nil
}

// buildEmergencyFallbackNews builds a structured deterministic emergency news item
// when AI is unavailable or fails, ensuring critical alerts are never lost.
func buildEmergencyFallbackNews(title, url, sourceName, content string) news.News {
	cleanTitle := strings.TrimSpace(title)
	desc := strings.TrimSpace(content)
	if len([]rune(desc)) > 350 {
		desc = string([]rune(desc)[:350]) + "..."
	}
	if desc == "" {
		desc = "Офіційне екстрене сповіщення служб Данії. Слідкуйте за оновленнями та вказівками влади."
	}

	return news.News{
		Title:            cleanTitle,
		Link:             url,
		Published:        time.Now(),
		Category:         "emergency",
		SourceName:       sourceName,
		SourceLang:       "da",
		Content:          content,
		TitleDanish:      cleanTitle,
		TitleUkrainian:   "🚨 ЕКСТРЕНЕ ПОВІДОМЛЕННЯ: " + cleanTitle,
		SummaryDanish:    desc,
		SummaryUkrainian: "Офіційне екстрене сповіщення екстрених служб Данії:\n\n" + desc,
		TLDR:             "Офіційне термінове сповіщення екстрених служб Данії.",
		WhyItMatters:     "Безпека жителів Данії та рух транспорту.",
		Mood:             "urgent",
		IsExclusive:      true,
		AudienceScore:    12,
	}
}

func deliverReadyDelayedPosts(ctx context.Context, cfg *config.Config, cache DedupeChecker) {
	ready, err := cache.GetReadyDelayedPosts(ctx)
	if err != nil {
		logger.Warn("Failed to check delayed posts queue in breaking runner", "error", err)
		return
	}
	if len(ready) == 0 {
		return
	}

	logger.Info("Breaking runner found ready delayed posts to deliver", "count", len(ready))
	for _, dp := range ready {
		var n news.News
		if err := json.Unmarshal([]byte(dp.NewsJSON), &n); err != nil {
			logger.Error("Failed to parse delayed post news json", "id", dp.ID, "error", err)
			_ = cache.MarkDelayedPostFailed(dp.ID, err.Error())
			continue
		}

		if cache.IsAlreadySent(dp.Hash) || (dp.Link != "" && cache.IsSourceURLSent(dp.Link)) {
			logger.Info("Delayed post already sent, marking sent", "title", dp.Title, "hash", dp.Hash)
			_ = cache.MarkDelayedPostSent(dp.ID)
			continue
		}

		var buttons [][]telegram.InlineButton
		if cfg.Feature.EnableInlineButtons && n.Link != "" {
			buttons = append(buttons, []telegram.InlineButton{
				{Text: "🔗 Читати оригінал / Læs mere", URL: n.Link},
			})
		}

		if n.ImageURL == "" {
			cat := news.ValidateCategory(n.Category)
			n.ImageURL = news.GetCategoryImage(cat)
		}

		canPhoto := news.ShouldUsePhoto(n, cfg.Posting.PhotoTextLimit)
		var err error
		if canPhoto {
			caption := news.FormatCaptionForPhoto(n, cfg.Posting.PhotoTextLimit)
			if len(buttons) > 0 {
				_, err = telegram.SendPhotoWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, n.ImageURL, caption, buttons)
			} else {
				_, err = telegram.SendPhoto(cfg.Telegram.Token, cfg.Telegram.ChatID, n.ImageURL, caption)
			}
		} else {
			text := news.FormatNewsWithImage(n)
			if len(buttons) > 0 {
				_, err = telegram.SendMessageWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, text, buttons, true, 0)
			} else {
				_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, text)
			}
		}

		if err != nil {
			logger.Error("Failed to deliver delayed post to Telegram", "title", dp.Title, "error", err)
			_ = cache.MarkDelayedPostFailed(dp.ID, err.Error())
		} else {
			_ = cache.MarkDelayedPostSent(dp.ID)
			_ = cache.MarkAsSentWithSemanticData(dp.Hash, dp.Title, dp.Link, n.Content, n.Category, n.SourceName, n.TitleUkrainian, n.StoryClusterKey, n.Embedding)
			logger.Info("Delayed post delivered to Telegram successfully via breaking check", "title", dp.Title)
		}
	}
}
