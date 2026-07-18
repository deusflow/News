package digest

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/storage"
	"github.com/deusflow/News/internal/telegram"
	_ "github.com/lib/pq"
)

type DigestNewsItem struct {
	Title   string
	Content string
	URL     string
}

func hasTextContent(s string) bool {
	// Remove HTML tags
	re := regexp.MustCompile(`<[^>]*>`)
	stripped := re.ReplaceAllString(s, "")
	// Also remove HTML space entities
	stripped = strings.ReplaceAll(stripped, "&nbsp;", "")
	stripped = strings.ReplaceAll(stripped, "&#8203;", "")
	return strings.TrimSpace(stripped) != ""
}

func RunDigest(ctx context.Context, cfg *config.Config, aiManager *ai.Manager, supabase *storage.SupabaseClient) error {
	logger.Info("Starting Sunday Digest generation")

	// 1. Сбор новостей из доступных источников (с бэкапами)
	newsItems, err := fetchNewsForDigest(ctx, cfg, supabase)
	if err != nil {
		return fmt.Errorf("failed to fetch news for digest: %w", err)
	}

	// 2. Формируем контекст для AI
	var sb strings.Builder
	for i, n := range newsItems {
		sb.WriteString(fmt.Sprintf("%d. %s\nContent: %s\nURL: %s\n\n", i+1, n.Title, truncateString(n.Content, 400), n.URL))
	}

	content := sb.String()

	// 3. Промпт для AI
	systemPrompt := `Ти - головний редактор провідного новинного каналу для українців у Данії.
Твоя задача: проаналізувати список новин за тиждень і скласти стильний, професійний та візуально привабливий недільний дайджест.
Вибери ТОП-5 найважливіших новин тижня. Пріоритет віддавай змінам у законах, візах, роботі, податках, виплатах та освіті.
Якщо деякі новини в списку написані данською мовою, переклади та адаптуй їх українською.

Вимоги до аналізу та відбору новин:
- УНИКАЙ ДУБЛЮВАННЯ ТЕМАТИКИ: Якщо вхідний список містить схожі новини або кілька етапів розвитку однієї події (наприклад, спочатку заява про намір обмежити права призовників, а потім новина про законопроєкт на ту саму тему), ти повинен:
  а) ОБ'ЄДНАТИ їх в один пункт дайджесту, об'єднавши всі важливі деталі та передісторію в одне містке пояснення (наприклад, згадати і заяву уряду, і поданий законопроєкт).
  б) АБО, якщо новини суттєво відрізняються деталями, чітко розпиши в чому саме полягає ця різниця (наприклад, "Спочатку планували обмеження для всіх чоловіків, але у поданому законопроєкті вікові рамки звузили до 23-60 років").
- Категорично заборонено виводити два різних пункти дайджесту про одне й те саме питання, написані різними словами.

Вимоги до оформлення та дизайну поста:
1. Заголовок повинен бути яскравим та помітним, наприклад:
   ✨ <b>ТИЖНЕВИЙ ДАЙДЖЕСТ: Головне в Данії</b>
   (використовуй емодзі та жирний шрифт).
2. Після заголовка додай короткий стильний лід-абзац (1 речення), що підсумовує загальний настрій тижня, наприклад:
   <i>Цей тиждень приніс важливі новини про правила перебування та нові економічні зміни. Зібрали для вас найголовніше:</i>
3. Кожна новина у списку повинна бути відокремлена подвійним переносом рядка та оформлена наступним чином:
   👉 <b><a href="URL">Заголовок новини</a></b>
   💬 <i>Коротке, містке пояснення (1-2 речення). Чітко, без води, чому це важливо для українців.</i>
4. Важливо: посилання на оригінал новини (URL) має бути вшито безпосередньо в заголовок через тег <a href="URL">...</a>. Не виводь сирі URL-адреси окремим текстом. Если ты объединяешь несколько новостей, используй ссылку на самую важную/свежую из них.
5. У кінці поста додай стильне побажання гарного тижня та заклик до дії, наприклад:
   ---
   🌅 <i>Бажаємо вам спокійної неділі та продуктивного нового тижня!</i>
6. Допускаються Тільки дозволены HTML-теги: <b>, <i>, <a>. Усі теги мають бути правильно закриті. Не використовуй непідтримувані Телеграмом теги.

УВАГА: Ти повинен повернути ВАЛІДНИЙ JSON! Весь текст дайджесту помісти у поле "summary". 
Інші поля залиш порожніми.`

	userPrompt := "Ось список новин за тиждень:\n\n" + content

	// 4. Вызов AI
	resp, err := aiManager.Generate(ctx, "Sunday Digest", content, systemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("failed to generate digest via AI: %w", err)
	}

	// Ищем текст в полях ответа
	digestText := strings.TrimSpace(resp.Summary)
	if !hasTextContent(digestText) {
		digestText = strings.TrimSpace(resp.Ukrainian)
	}
	if !hasTextContent(digestText) {
		digestText = strings.TrimSpace(resp.Danish)
	}

	if !hasTextContent(digestText) {
		return fmt.Errorf("AI returned empty digest content (both summary and ukrainian fields are empty/invalid)")
	}

	logger.Info("Digest generated successfully", "length", len(digestText))

	// 5. Публикация в Telegram
	_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, digestText)
	if err != nil {
		return fmt.Errorf("failed to send digest to telegram: %w", err)
	}

	logger.Info("Sunday Digest published to Telegram successfully")
	return nil
}

func fetchNewsForDigest(ctx context.Context, cfg *config.Config, supabase *storage.SupabaseClient) ([]DigestNewsItem, error) {
	var items []DigestNewsItem

	// 1. Попытка получить из Supabase
	if supabase != nil {
		logger.Info("Attempting to fetch news for digest from Supabase")
		activeNews, err := supabase.GetActiveNews(ctx, 100)
		if err == nil && len(activeNews) > 0 {
			sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour)
			for _, n := range activeNews {
				if n.PublishedAt.After(sevenDaysAgo) {
					items = append(items, DigestNewsItem{
						Title:   n.TitleUkrainian,
						Content: n.TLDR,
						URL:     n.SourceURL,
					})
				}
			}
			if len(items) > 0 {
				logger.Info("Successfully fetched news from Supabase", "count", len(items))
				return items, nil
			}
		}
		if err != nil {
			logger.Warn("Failed to fetch news from Supabase for digest", "error", err)
		}
	}

	// 2. Попытка получить из PostgreSQL (sent_news + translation_cache)
	if cfg.Database.UsePostgres && cfg.Database.URL != "" {
		logger.Info("Attempting to fetch news for digest from PostgreSQL")
		db, err := sql.Open("postgres", cfg.Database.URL)
		if err == nil {
			defer db.Close()
			query := `
				SELECT s.title, s.link, COALESCE(t.ukrainian_translation, t.summary, '') 
				FROM sent_news s 
				LEFT JOIN translation_cache t ON s.content_hash = t.content_hash 
				WHERE s.sent_at >= NOW() - INTERVAL '7 days' 
				ORDER BY s.sent_at DESC 
				LIMIT 30
			`
			rows, queryErr := db.QueryContext(ctx, query)
			if queryErr == nil {
				defer rows.Close()
				for rows.Next() {
					var title, link, content string
					if scanErr := rows.Scan(&title, &link, &content); scanErr == nil {
						items = append(items, DigestNewsItem{
							Title:   title,
							Content: content,
							URL:     link,
						})
					}
				}
				if len(items) > 0 {
					logger.Info("Successfully fetched news from PostgreSQL", "count", len(items))
					return items, nil
				}
			} else {
				logger.Warn("PostgreSQL query failed for digest", "error", queryErr)
			}
		} else {
			logger.Warn("Failed to open PostgreSQL connection for digest", "error", err)
		}
	}

	// 3. Попытка получить свежие новости из RSS фидов напрямую
	logger.Info("Attempting to fetch news for digest from live RSS feeds")
	if cfg.RSS.FeedsConfigPath != "" {
		feeds, err := rss.LoadFeeds(cfg.RSS.FeedsConfigPath)
		if err == nil && len(feeds) > 0 {
			feedItems, fetchErr := rss.FetchAllFeeds(ctx, feeds)
			if fetchErr == nil && len(feedItems) > 0 {
				// Берем последние 15 новостей
				limit := 15
				if len(feedItems) < limit {
					limit = len(feedItems)
				}
				for i := 0; i < limit; i++ {
					item := feedItems[i]
					items = append(items, DigestNewsItem{
						Title:   item.Title,
						Content: item.Description,
						URL:     item.Link,
					})
				}
				logger.Info("Successfully fetched news from live RSS feeds", "count", len(items))
				return items, nil
			} else if fetchErr != nil {
				logger.Warn("Failed to fetch RSS feeds for digest", "error", fetchErr)
			}
		} else if err != nil {
			logger.Warn("Failed to load RSS feeds config for digest", "error", err)
		}
	}

	return nil, fmt.Errorf("all news sources failed or returned empty list")
}

func truncateString(s string, max int) string {
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max]) + "..."
	}
	return s
}
