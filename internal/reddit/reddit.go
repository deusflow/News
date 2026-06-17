package reddit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/telegram"
)

const redditUserAgent = "script:danish_news_bot_reddit:v1.0"

func Run(ctx context.Context, cfg *config.Config, aiManager *ai.Manager) error {
	logger.Info("Starting Reddit Weekly Digest Run")

	// 1. Сбор постов за неделю
	posts, err := fetchTopPosts()
	if err != nil {
		return fmt.Errorf("failed to fetch reddit posts: %w", err)
	}

	if len(posts) == 0 {
		logger.Info("No relevant Reddit posts found this week")
		return nil
	}

	// 2. Формирование контекста для LLM
	var sb strings.Builder
	for i, p := range posts {
		sb.WriteString(fmt.Sprintf("Post %d: %s\nContent: %s\nScore: %d\nComments: %d\n---\n", i+1, p.Title, truncateString(p.Selftext, 500), p.Score, p.NumComments))
	}
	
	prompt := fmt.Sprintf(`Ты - аналитик и журналист. Твоя задача сделать еженедельный дайджест "Что обсуждали датчане на Reddit на этой неделе". 
Проанализируй следующие популярные посты с r/Denmark. 
Игнорируй токсичные выбросы, троллинг и бессмысленные посты. 
Сделай выжимку только по темам: Украина/украинцы, политика, экономика, деньги, налоги, образование, важные социальные проблемы.

Твой ответ должен быть готовым постом для Telegram на украинском языке, с эмодзи. 
Структура:
1. Заголовок (например, 🇩🇰 <b>Глас народу: Що обговорювали в Данії цього тижня?</b>)
2. 2-3 главных темы, которые волновали людей, с кратким объяснением аргументов "за" и "против".
3. Короткий вывод о настроениях.

Используй HTML-теги <b> <i> для форматирования.

Вот данные:
%s`, sb.String())

	// 3. Генерация через Gemini
	logger.Info("Sending to AI", "context_length", len(sb.String()))
	aiResponse, err := aiManager.Generate(ctx, "Reddit Weekly Digest", sb.String(), prompt, "")
	if err != nil {
		return fmt.Errorf("failed to generate digest with AI: %w", err)
	}

	// 4. Публикация в Telegram
	_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, aiResponse.Ukrainian)
	if err != nil {
		return fmt.Errorf("failed to send telegram message: %w", err)
	}

	logger.Info("Successfully published Reddit Digest")
	return nil
}

type RedditPost struct {
	Title       string
	Selftext    string
	Score       int
	NumComments int
}

func fetchTopPosts() ([]RedditPost, error) {
	searchURL := "https://old.reddit.com/r/Denmark/top/.json?t=week&limit=100"

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", redditUserAgent)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reddit returned %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			Children []struct {
				Data struct {
					Title       string `json:"title"`
					Selftext    string `json:"selftext"`
					Score       int    `json:"score"`
					NumComments int    `json:"num_comments"`
					URL         string `json:"url"`
				} `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var relevantPosts []RedditPost
	keywords := []string{"ukrain", "politik", "økonomi", "penge", "uddannelse", "skat", "su"}

	for _, child := range result.Data.Children {
		post := child.Data
		text := strings.ToLower(post.Title + " " + post.Selftext)
		
		isRelevant := false
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				isRelevant = true
				break
			}
		}

		// Только если пост релевантен и имеет норм скор
		if isRelevant && post.Score > 50 {
			relevantPosts = append(relevantPosts, RedditPost{
				Title:       post.Title,
				Selftext:    post.Selftext,
				Score:       post.Score,
				NumComments: post.NumComments,
			})
		}
		
		// Ограничение, чтобы не перегрузить токенами
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
