package breaking

import (
	"context"
	"fmt"
	"os"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/telegram"
)

func Run(ctx context.Context, cfg *config.Config, aiManager *ai.Manager) error {
	logger.Info("Starting Breaking News mode")

	url := os.Getenv("BREAKING_URL")
	title := os.Getenv("BREAKING_TITLE")

	if url == "" {
		return fmt.Errorf("BREAKING_URL is empty")
	}

	logger.Info("Processing breaking news", "url", url, "title", title)

	// В идеале мы могли бы парсить саму статью по URL (goquery), но для Молнии
	// часто достаточно заголовка и краткого описания из RSS, либо AI вытащит из заголовка суть.
	// Так как это Breaking News, мы передаем AI сам заголовок и URL.

	prompt := fmt.Sprintf(`Ты - срочный редактор новостей. Получена молния (Breaking News) из Дании.
Заголовок оригинала: "%s"
Ссылка: %s

Твоя задача: Написать супер-короткий и цепляющий пост для Telegram на украинском языке.
Формат:
🚨 <b>МОЛНИЯ: [Суть одним предложением]</b>

[Краткое объяснение на 1-2 предложения, если понятно из заголовка, иначе просто суть]

🔗 <a href="%s">Джерело</a>`, title, url, url)

	aiResponse, err := aiManager.Generate(ctx, "Breaking News", title, prompt, "")
	if err != nil {
		return fmt.Errorf("failed to generate breaking news with AI: %w", err)
	}

	_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, aiResponse.Ukrainian)
	if err != nil {
		return fmt.Errorf("failed to send breaking telegram message: %w", err)
	}

	logger.Info("Successfully published Breaking News")
	return nil
}
