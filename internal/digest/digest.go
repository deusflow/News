package digest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/storage"
	"github.com/deusflow/News/internal/telegram"
)

func RunDigest(ctx context.Context, cfg *config.Config, aiManager *ai.Manager, supabase *storage.SupabaseClient) error {
	if supabase == nil {
		return fmt.Errorf("supabase is required for digest generation")
	}

	logger.Info("Starting Sunday Digest generation")

	// 1. Получаем последние новости (берем с запасом)
	newsList, err := supabase.GetActiveNews(100)
	if err != nil {
		return fmt.Errorf("failed to get active news: %w", err)
	}

	// 2. Оставляем только те, что за последние 7 дней
	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour)
	var recentNews []storage.NewsArchive

	for _, n := range newsList {
		if n.PublishedAt.After(sevenDaysAgo) {
			recentNews = append(recentNews, n)
		}
	}

	if len(recentNews) == 0 {
		logger.Info("No news from the last 7 days, skipping digest")
		return nil
	}

	// 3. Формируем контекст для AI
	var sb strings.Builder
	for i, n := range recentNews {
		sb.WriteString(fmt.Sprintf("%d. %s\nTLDR: %s\nURL: %s\n\n", i+1, n.TitleUkrainian, n.TLDR, n.SourceURL))
	}

	content := sb.String()

	// 4. Промпт для AI
	systemPrompt := `Ти - головний редактор новинного каналу для українців у Данії.
Твоя задача: проаналізувати список новин за тиждень і скласти короткий, красивий недільний дайджест.
Вибери ТОП-5 найважливіших новин (особливо ті, що стосуються роботи, віз, грошей та законів).

Формат дайджесту:
- Привітайся (наприклад "🗓 Недільний дайджест: головне за тиждень")
- Короткий вступ (1 речення)
- Список з 5 новин. Кожна новина: емодзі, заголовок (з посиланням на оригінал), та 1-2 речення суті.
- Побажання гарного тижня.

УВАГА: Ти повинен повернути ВАЛІДНИЙ JSON! Весь текст дайджесту помісти у поле "summary". 
Інші поля залиш порожніми.`

	userPrompt := "Ось список новин за тиждень:\n\n" + content

	// 5. Вызов AI
	resp, err := aiManager.Generate(ctx, "Sunday Digest", content, systemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("failed to generate digest via AI: %w", err)
	}

	digestText := strings.TrimSpace(resp.Summary)
	if digestText == "" {
		return fmt.Errorf("AI returned empty digest summary")
	}

	logger.Info("Digest generated successfully", "length", len(digestText))

	// 6. Публикация в Telegram
	_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, digestText)
	if err != nil {
		return fmt.Errorf("failed to send digest to telegram: %w", err)
	}

	logger.Info("Sunday Digest published to Telegram successfully")
	return nil
}
