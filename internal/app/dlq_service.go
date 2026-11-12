package app

import (
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/telegram"
)

// processFailedMessages processes failed messages from the Dead Letter Queue (DLQ).
// It accepts any CacheAdapter, fetches up to 5 failed messages, attempts to resend them to Telegram,
// and deletes or increments retry counts accordingly.
func processFailedMessages(adapter CacheAdapter, cfg *config.Config, m *metrics.Metrics) {
	items, err := adapter.GetFailedNews(5)
	if err != nil {
		logger.Error("Failed to get DLQ items", "error", err)
		return
	}

	if len(items) > 0 {
		logger.Info("Processing failed messages", "count", len(items))
	}

	for _, item := range items {
		// Guard: if the original send actually reached Telegram but we failed
		// to process the response, resending would create a duplicate post.
		hash := adapter.GenerateNewsHash(item.Title, item.Link)
		if adapter.IsAlreadySent(hash) || adapter.IsSourceURLSent(item.Link) {
			logger.Info("DLQ item already delivered, removing from queue", "id", item.ID, "title", item.Title)
			_ = adapter.DeleteFailedNews(item.ID)
			continue
		}

		var buttons [][]telegram.InlineButton
		if cfg.Feature.EnableInlineButtons && item.Link != "" {
			buttons = append(buttons, []telegram.InlineButton{
				{Text: "🔗 Читати оригінал / Læs mere", URL: item.Link},
			})
		}

		var err error
		if item.ImageURL != "" {
			if len(buttons) > 0 {
				_, err = telegram.SendPhotoWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, item.ImageURL, item.MessageText, buttons)
			} else {
				_, err = telegram.SendPhoto(cfg.Telegram.Token, cfg.Telegram.ChatID, item.ImageURL, item.MessageText)
			}
		} else {
			if len(buttons) > 0 {
				_, err = telegram.SendMessageWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, item.MessageText, buttons, true, 0)
			} else {
				_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, item.MessageText)
			}
		}

		if err != nil {
			logger.Error("Failed to resend DLQ item", "id", item.ID, "error", err)
			if updateErr := adapter.IncrementFailedAttempts(item.ID, err.Error()); updateErr != nil {
				logger.Error("Failed to update DLQ attempts", "error", updateErr)
			}
		} else {
			logger.Info("Successfully resent DLQ item", "id", item.ID)
			if delErr := adapter.DeleteFailedNews(item.ID); delErr != nil {
				logger.Error("Failed to delete DLQ item", "error", delErr)
			}

			// Также отмечаем как отправленное в основной таблице (с сохранением title_norm и content_hash)
			hash := adapter.GenerateNewsHash(item.Title, item.Link)
			_ = adapter.MarkAsSentWithContent(hash, item.Title, item.Link, item.MessageText, "DLQ", "DLQ")
			m.IncrementTelegramMessagesSent()
		}
	}
}

