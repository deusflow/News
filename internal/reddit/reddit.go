package reddit

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/telegram"
	_ "github.com/lib/pq"
	"github.com/mmcdole/gofeed"
)

const redditUserAgent = "script:danish_news_bot_reddit:v1.0"

func Run(ctx context.Context, cfg *config.Config, aiManager *ai.Manager) error {
	logger.Info("Starting Reddit Weekly Digest Run")

	var sb strings.Builder
	var systemPrompt string
	var userPrompt string
	var sourceName string

	// 1. Сбор постов с Reddit (Основной план)
	posts, err := fetchTopPosts()
	if err == nil && len(posts) > 0 {
		sourceName = "Reddit Weekly Digest"
		for i, p := range posts {
			sb.WriteString(fmt.Sprintf("Post %d: %s\nContent: %s\nScore: %d\nComments: %d\n---\n", i+1, p.Title, truncateString(p.Selftext, 500), p.Score, p.NumComments))
		}

		systemPrompt = `Ты - аналитик и журналист. Твоя задача сделать еженедельный дайджест "Что обсуждали датчане на Reddit на этой неделе". 
Проанализируй популярные посты с r/Denmark. 
Игнорируй токсичные выбросы, троллинг и бессмысленные посты. 
Сделай выжимку только по темам: Украина/украинцы, политика, экономика, деньги, налоги, образование, важные социальные проблемы.

Твой ответ должен быть готовым постом для Telegram на украинском языке, с эмодзи. 
Структура:
1. Заголовок (например, 🇩🇰 <b>Глас народу: Що обговорювали в Данії цього тижня?</b>)
2. 2-3 главных темы, которые волновали людей, с кратким объяснением аргументов "за" и "против".
3. Короткий вывод о настроениях.

Используй HTML-теги <b> <i> для форматирования.

УВАГА: Ты должен вернуть ВАЛИДНЫЙ JSON! Текст готового поста на украинском языке помести в поле "ukrainian". Все остальные поля оставь пустыми.`
		userPrompt = "Вот данные с Reddit для анализа:\n\n" + sb.String()
	} else {
		if err != nil {
			logger.Warn("Reddit fetch failed, activating Plan B (Database news fallback)", "error", err)
		} else {
			logger.Warn("No relevant Reddit posts found, activating Plan B (Database news fallback)")
		}

		// План Б: Сбор новостей из базы данных
		var dbNews []struct {
			Title   string
			Link    string
			Content string
		}

		if cfg.Database.UsePostgres && cfg.Database.URL != "" {
			db, dbErr := sql.Open("postgres", cfg.Database.URL)
			if dbErr == nil {
				defer db.Close()
				query := `
					SELECT s.title, s.link, COALESCE(t.ukrainian_translation, t.summary, '') 
					FROM sent_news s 
					LEFT JOIN translation_cache t ON s.content_hash = t.content_hash 
					WHERE s.sent_at >= NOW() - INTERVAL '7 days' 
					ORDER BY s.sent_at DESC 
					LIMIT 15
				`
				rows, queryErr := db.Query(query)
				if queryErr == nil {
					defer rows.Close()
					for rows.Next() {
						var item struct {
							Title   string
							Link    string
							Content string
						}
						if scanErr := rows.Scan(&item.Title, &item.Link, &item.Content); scanErr == nil {
							dbNews = append(dbNews, item)
						}
					}
				} else {
					logger.Warn("Failed to query database news for fallback", "error", queryErr)
				}
			} else {
				logger.Warn("Failed to open database connection for fallback", "error", dbErr)
			}
		}

		if len(dbNews) > 0 {
			sourceName = "Database Weekly Digest (Plan B)"
			logger.Info("Plan B success: loaded news from DB", "count", len(dbNews))
			for i, n := range dbNews {
				sb.WriteString(fmt.Sprintf("News %d: %s\nSummary: %s\nURL: %s\n---\n", i+1, n.Title, truncateString(n.Content, 400), n.Link))
			}

			systemPrompt = `Ты - аналитик и журналист. Твоя задача сделать недільний дайджест головних новин Данії за тиждень. 
Проанализируй предоставленные новости, которые уже выходили в нашем канале на этой неделе.
Сделай красивую выжимку самых важных событий и трендов в Дании.

Твой ответ должен быть готовым постом для Telegram на украинском языке, с эмодзи. 
Структура:
1. Заголовок (например, 🇩🇰 <b>Дайджест новин Данії: Головне за тиждень</b>)
2. Короткий вступ (1 речення)
3. Список из 5 главных новостей. Каждая новость: эмодзи, заголовок (с ссылкой на оригинальный URL), и 1-2 предложения сути.
4. Пожелание хорошей недели.

Используй HTML-теги <b> <i> для форматирования.

УВАГА: Ты должен вернуть ВАЛИДНЫЙ JSON! Текст готового поста на украинском языке помести в поле "ukrainian". Все остальные поля оставь пустыми.`
			userPrompt = "Вот список новостей за неделю для анализа:\n\n" + sb.String()
		} else {
			logger.Warn("Plan B failed or empty, activating Plan C (dr.dk RSS fallback)")

			// План В: Сбор свежих новостей напрямую с сайта dr.dk по RSS
			drNews, drErr := fetchDRNews()
			if drErr == nil && len(drNews) > 0 {
				sourceName = "DR.dk Weekly Digest (Plan C)"
				logger.Info("Plan C success: loaded news from dr.dk", "count", len(drNews))
				for i, n := range drNews {
					sb.WriteString(fmt.Sprintf("News %d: %s\nDescription: %s\nURL: %s\n---\n", i+1, n.Title, truncateString(n.Selftext, 400), n.Link))
				}

				systemPrompt = `Ты - аналитик и журналист. Твоя задача сделать недільний дайджест головних новин Данії за тиждень. 
Проанализируй предоставленные свежие новости с главного датского новостного портала dr.dk.
Сделай красивую выжимку самых важных событий и трендов в Дании.

Твой ответ должен быть готовым постом для Telegram на украинском языке, с эмодзи. 
Структура:
1. Заголовок (например, 🇩🇰 <b>Дайджест новин Данії: Головне за тиждень (dr.dk)</b>)
2. Короткий вступ (1 речення)
3. Список из 5 главных новостей. Каждая новость: эмодзи, заголовок (с ссылкой на оригинальный URL), и 1-2 предложения сути.
4. Пожелание хорошей недели.

Используй HTML-теги <b> <i> для форматирования.

УВАГА: Ты должен вернуть ВАЛИДНЫЙ JSON! Текст готового поста на украинском языке помести в поле "ukrainian". Все остальные поля оставь пустыми.`
				userPrompt = "Вот свежие новости с портала dr.dk для анализа:\n\n" + sb.String()
			} else {
				if drErr != nil {
					return fmt.Errorf("all fallbacks failed (Reddit, DB, dr.dk): %w", drErr)
				}
				return fmt.Errorf("all fallbacks failed (Reddit, DB, dr.dk returned no news)")
			}
		}
	}

	// 3. Генерация через Gemini
	logger.Info("Sending to AI", "source", sourceName, "context_length", len(sb.String()))
	aiResponse, err := aiManager.Generate(ctx, sourceName, sb.String(), systemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("failed to generate digest with AI: %w", err)
	}

	// 4. Публикация в Telegram
	digestText := strings.TrimSpace(aiResponse.Ukrainian)
	if digestText == "" {
		digestText = strings.TrimSpace(aiResponse.Summary)
	}
	if digestText == "" {
		digestText = strings.TrimSpace(aiResponse.Danish)
	}

	if digestText == "" {
		return fmt.Errorf("AI returned empty digest content (both ukrainian and summary fields are empty)")
	}

	_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, digestText)
	if err != nil {
		return fmt.Errorf("failed to send telegram message: %w", err)
	}

	logger.Info("Successfully published Weekly Digest", "source", sourceName)
	return nil
}

type RedditPost struct {
	Title       string
	Selftext    string
	Link        string
	Score       int
	NumComments int
}

func fetchTopPosts() ([]RedditPost, error) {
	searchURL := "https://www.reddit.com/r/Denmark/top.rss?t=week"

	fp := gofeed.NewParser()
	fp.Client = &http.Client{
		Timeout: 15 * time.Second,
	}

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", redditUserAgent)

	resp, err := fp.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reddit returned %d", resp.StatusCode)
	}

	feed, err := fp.Parse(resp.Body)
	if err != nil {
		return nil, err
	}

	var relevantPosts []RedditPost
	keywords := []string{"ukrain", "politik", "økonomi", "penge", "uddannelse", "skat", "su"}

	for _, item := range feed.Items {
		title := item.Title
		content := item.Content
		if content == "" {
			content = item.Description
		}
		text := strings.ToLower(title + " " + content)
		
		isRelevant := false
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				isRelevant = true
				break
			}
		}

		if isRelevant {
			relevantPosts = append(relevantPosts, RedditPost{
				Title:       title,
				Selftext:    content,
				Link:        item.Link,
				Score:       100, // RSS feeds do not contain exact scores, setting generic high value
				NumComments: 20,  // Setting generic comments count placeholder
			})
		}
		
		if len(relevantPosts) >= 15 {
			break
		}
	}

	return relevantPosts, nil
}

func truncateString(s string, max int) string {
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max]) + "..."
	}
	return s
}

func fetchDRNews() ([]RedditPost, error) {
	searchURL := "https://www.dr.dk/nyheder/service/feeds/allenyheder"

	fp := gofeed.NewParser()
	fp.Client = &http.Client{
		Timeout: 15 * time.Second,
	}

	feed, err := fp.ParseURL(searchURL)
	if err != nil {
		return nil, err
	}

	var relevantPosts []RedditPost
	for _, item := range feed.Items {
		title := item.Title
		content := item.Content
		if content == "" {
			content = item.Description
		}
		
		relevantPosts = append(relevantPosts, RedditPost{
			Title:       title,
			Selftext:    content,
			Link:        item.Link,
			Score:       100,
			NumComments: 20,
		})

		if len(relevantPosts) >= 15 {
			break
		}
	}

	return relevantPosts, nil
}
