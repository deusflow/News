package breaking

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/scraper"
	"github.com/deusflow/News/internal/telegram"
)

// Run processes a breaking news item received via repository_dispatch or manual trigger.
// It scrapes the article, generates AI summary using the standard news prompt,
// and publishes to Telegram using the standard format with a BREAKING header.
func Run(ctx context.Context, cfg *config.Config, aiManager *ai.Manager) error {
	logger.Info("Starting Breaking News mode")

	url := os.Getenv("BREAKING_URL")
	title := os.Getenv("BREAKING_TITLE")

	if url == "" {
		return fmt.Errorf("BREAKING_URL is empty")
	}
	if title == "" {
		return fmt.Errorf("BREAKING_TITLE is empty")
	}

	logger.Info("Processing breaking news", "url", url, "title", title)

	// Scrape the full article for AI context
	content := ""
	scrapeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ac, err := scraper.ExtractFullArticle(scrapeCtx, url)
	cancel()
	if err == nil && ac != nil && ac.Content != "" {
		content = ac.Content
		logger.Info("Scraped breaking article", "content_len", len(content))
	} else {
		logger.Warn("Could not scrape breaking article, using title only", "error", err)
	}

	// Use the SAME prompt as the main news pipeline for consistent output format
	systemPrompt := news.GenerateNewsSystemPrompt()
	userPrompt := news.GenerateNewsUserContent(title, content)

	resp, err := aiManager.Generate(ctx, title, content, systemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("AI generation failed for breaking news: %w", err)
	}

	// Validate AI response (same validation as main pipeline)
	if err := resp.Validate(); err != nil {
		return fmt.Errorf("AI response validation failed: %w", err)
	}

	// Override mood to urgent (this IS breaking news)
	resp.Mood = "urgent"

	// Build a news.News struct to use standard formatting
	n := news.News{
		Title:            title,
		Link:             url,
		Published:        time.Now(),
		Category:         resp.Category,
		SourceName:       "Breaking",
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
		IsExclusive:      true, // Breaking news is always exclusive
		AudienceScore:    resp.AudienceScore,
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
