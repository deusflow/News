package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/ai/gemini"
	"github.com/deusflow/News/internal/ai/groq"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/storage"
	"github.com/deusflow/News/internal/telegram"
	"github.com/deusflow/News/internal/website"
)

const publishPerRunLimit = 1

// Service interfaces for SRP
type NewsFetcher interface {
	Fetch(ctx context.Context) ([]*rss.FeedItem, error)
}

type NewsProcessor interface {
	Process(ctx context.Context, items []*rss.FeedItem) ([]news.News, error)
}

type NewsSender interface {
	// Send publishes exactly one news item — the first non-duplicate from newsList
	// (which is already sorted by relevance score, best first).
	Send(ctx context.Context, newsList []news.News)
}

type HealthChecker interface {
	CheckHealth(ctx context.Context) map[string]string
}

// RSSFetcher implements NewsFetcher
type RSSFetcher struct {
	feeds []rss.FeedSource
}

func NewRSSFetcher(feeds []rss.FeedSource) *RSSFetcher {
	return &RSSFetcher{feeds: feeds}
}

func (f *RSSFetcher) Fetch(ctx context.Context) ([]*rss.FeedItem, error) {
	return rss.FetchAllFeeds(ctx, f.feeds)
}

// NewsFilterProcessor implements NewsProcessor
type NewsFilterProcessor struct {
	cfg      *config.Config
	aiMgr    *ai.Manager
	metrics  *metrics.Metrics
	keywords *config.KeywordsConfig
}

func NewNewsFilterProcessor(cfg *config.Config, aiMgr *ai.Manager, m *metrics.Metrics, keywords *config.KeywordsConfig) *NewsFilterProcessor {
	return &NewsFilterProcessor{
		cfg:      cfg,
		aiMgr:    aiMgr,
		metrics:  m,
		keywords: keywords,
	}
}

func (p *NewsFilterProcessor) Process(ctx context.Context, items []*rss.FeedItem) ([]news.News, error) {
	return news.FilterAndTranslateWithOptions(ctx, items, news.Options{
		MaxAge:            p.cfg.RSS.NewsMaxAge,
		PerSource:         5, // Allow up to 5 best articles per RSS source
		ScrapeMaxArticles: p.cfg.Scraper.MaxArticles,
		ScrapeConcurrency: p.cfg.Scraper.Concurrency,
		Keywords:          p.keywords,
		Config:            p.cfg,
		AI:                p.aiMgr,
		Metrics:           p.metrics,
	})
}

// TelegramNewsSender implements NewsSender
type TelegramNewsSender struct {
	cfg              *config.Config
	cacheAdapter     CacheAdapter
	metrics          *metrics.Metrics
	websiteGenerator *website.Generator
	supabaseClient   *storage.SupabaseClient
}

func NewTelegramNewsSender(cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics, websiteGen *website.Generator, supabase *storage.SupabaseClient) *TelegramNewsSender {
	return &TelegramNewsSender{
		cfg:              cfg,
		cacheAdapter:     cacheAdapter,
		metrics:          m,
		websiteGenerator: websiteGen,
		supabaseClient:   supabase,
	}
}

func (s *TelegramNewsSender) Send(ctx context.Context, newsList []news.News) {
	sendBestNews(ctx, newsList, s.cfg, s.cacheAdapter, s.metrics, s.websiteGenerator, s.supabaseClient)
}

type App struct {
	cfg              *config.Config
	metrics          *metrics.Metrics
	cacheAdapter     CacheAdapter
	aiManager        *ai.Manager
	fetcher          NewsFetcher
	processor        NewsProcessor
	sender           NewsSender
	keywords         *config.KeywordsConfig
	websiteGenerator *website.Generator
	supabaseClient   *storage.SupabaseClient
}

func New(cfg *config.Config, m *metrics.Metrics) (*App, error) {
	logger.Init()
	logger.Info("Initializing Danish News Bot")

	// 1. Инициализация кэша
	var cacheAdapter CacheAdapter

	if cfg.Database.UsePostgres {
		pgCache, err := storage.NewPostgresCache(cfg.Database.URL, cfg.Database.TTL)
		if err != nil {
			return nil, fmt.Errorf("postgres error: %v", err)
		}

		// Очистка старых записей (важно для экономии места)
		if err := pgCache.Cleanup(); err != nil {
			logger.Warn("Failed to cleanup postgres cache", "error", err)
		} else {
			logger.Info("Postgres cache cleanup completed", "ttl_hours", cfg.Database.TTL)
		}

		cacheAdapter = &PostgresCacheAdapter{cache: pgCache}
		logger.Info("Using PostgreSQL cache")
	} else {
		// Мы используем FileCache из storage, но нам нужен адаптер для работы в app
		fileCache := storage.NewFileCache(cfg.Cache.FilePath, cfg.Cache.TTLHours)
		if err := fileCache.Load(); err != nil {
			logger.Warn("Failed to load cache", "error", err)
		}
		cacheAdapter = &FileCacheAdapter{cache: fileCache}
		logger.Info("Using File cache")
	}

	// 2. Инициализация AI (НОВАЯ ЛОГИКА)
	var aiProviders []ai.Provider

	for _, pName := range cfg.AI.Providers {
		switch strings.TrimSpace(strings.ToLower(pName)) {
		case "gemini":
			client, err := gemini.NewClient(cfg.AI.GeminiAPIKey, cfg.AI.GeminiModel)
			if err != nil {
				logger.Error("Failed to init Gemini", "error", err)
				continue // Не падаем, пробуем следующего
			}
			aiProviders = append(aiProviders, client)
			logger.Info("AI Provider added", "name", "gemini")

		case "groq":
			if cfg.AI.GroqAPIKey != "" {
				client := groq.NewClient(cfg.AI.GroqAPIKey)
				aiProviders = append(aiProviders, client)
				logger.Info("AI Provider added", "name", "groq")
			} else {
				logger.Warn("Groq API Key is missing, skipping")
			}
		default:
			logger.Warn("Unknown AI provider in config, skipping", "provider", pName)
		}
	}

	if len(aiProviders) == 0 {
		return nil, fmt.Errorf("no AI providers initialized (check config and keys)")
	}

	aiManager := ai.NewManager(m, cfg.AI.MaxGeminiRequests, aiProviders...)
	logger.Info("AI Manager initialized", "providers_count", len(aiProviders))

	// 3. Загрузка RSS фидов и ключевых слов
	feeds, err := rss.LoadFeeds(cfg.RSS.FeedsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("feeds error: %v", err)
	}

	keywords, err := config.LoadKeywords(cfg.RSS.KeywordsConfigPath)
	if err != nil {
		logger.Warn("Failed to load keywords config, using defaults or empty", "error", err)
	}

	// Initialize website generator
	websiteGen := website.NewGenerator(cfg.Website.ContentDir, cfg.Website.Enable)
	if cfg.Website.Enable {
		logger.Info("Website generation enabled", "content_dir", cfg.Website.ContentDir)
	}

	// Initialize Supabase client for website archive
	var supabaseClient *storage.SupabaseClient
	if cfg.Supabase.Enable {
		supabaseClient, err = storage.NewSupabaseClientWithOptions(cfg.Supabase.URL, cfg.Supabase.ServiceKey, storage.SupabaseClientOptions{
			HTTPTimeout:           time.Duration(cfg.Supabase.HTTPTimeoutSeconds) * time.Second,
			DuplicateCheckTimeout: time.Duration(cfg.Supabase.DuplicateCheckTimeoutSeconds) * time.Second,
			MaxRetries:            cfg.Supabase.MaxRetries,
			RetryBaseDelay:        time.Duration(cfg.Supabase.RetryBaseDelaySeconds) * time.Second,
			RetryMaxDelay:         time.Duration(cfg.Supabase.RetryMaxDelaySeconds) * time.Second,
		})
		if err != nil {
			logger.Warn("Failed to initialize Supabase client", "error", err)
		} else {
			// Test connection
			if pingErr := supabaseClient.Ping(); pingErr != nil {
				logger.Warn("Supabase ping failed", "error", pingErr)
				supabaseClient = nil
			} else {
				logger.Info("Supabase client initialized for website archive")
				// Archive old news (older than 10 days)
				if archiveErr := supabaseClient.ArchiveOldNews(); archiveErr != nil {
					logger.Warn("Failed to archive old news", "error", archiveErr)
				}
			}
		}
	}

	app := &App{
		cfg:              cfg,
		metrics:          m,
		cacheAdapter:     cacheAdapter,
		aiManager:        aiManager,
		fetcher:          NewRSSFetcher(feeds),
		processor:        NewNewsFilterProcessor(cfg, aiManager, m, keywords),
		sender:           NewTelegramNewsSender(cfg, cacheAdapter, m, websiteGen, supabaseClient),
		keywords:         keywords,
		websiteGenerator: websiteGen,
		supabaseClient:   supabaseClient,
	}

	return app, nil
}

// Run запускает приложение
func (a *App) Run(ctx context.Context) {
	logger.Info("Starting Danish News Bot Run")
	runStartedAt := time.Now()
	defer func() { a.metrics.RecordProcessingTime(time.Since(runStartedAt)) }()

	if maxEnv, ok := os.LookupEnv("MAX_NEWS_LIMIT"); ok {
		logger.Info("Ignoring legacy publish env in favor of hard architectural limit",
			"env", "MAX_NEWS_LIMIT",
			"value", maxEnv,
			"effective_publish_limit", publishPerRunLimit)
	}
	if modeEnv, ok := os.LookupEnv("BOT_MODE"); ok {
		logger.Info("Ignoring legacy publish env in favor of hard architectural mode",
			"env", "BOT_MODE",
			"value", modeEnv,
			"effective_mode", "single")
	}

	// Check for cancellation immediately
	if ctx.Err() != nil {
		logger.Info("Context cancelled before start")
		return
	}

	defer a.aiManager.Close()

	defer func() {
		switch adapter := a.cacheAdapter.(type) {
		case *FileCacheAdapter:
			if err := adapter.cache.Save(); err != nil {
				logger.Error("Failed to save file cache", "error", err)
			}
		case *PostgresCacheAdapter:
			// Явно закрываем connection pool — критично для Neon free tier
			// (ограниченное число одновременных подключений)
			if err := adapter.cache.Close(); err != nil {
				logger.Error("Failed to close postgres connection", "error", err)
			}
		}
	}()

	// 4.1 Обработка очереди неудачных сообщений (DLQ)
	// ИСПРАВЛЕНО: передаем интерфейс, а не конкретный тип
	processFailedMessages(a.cacheAdapter, a.cfg, a.metrics)

	// 4.2 Синхронизация незаписанных новостей в Supabase (sync queue)
	// Neon — source of truth. Если Supabase был недоступен в прошлый раз,
	// записи лежат в supabase_sync_queue и досинхронизируются здесь.
	if a.supabaseClient != nil {
		syncPendingToSupabase(ctx, a.cacheAdapter, a.supabaseClient)
	}

	// 5. Скачивание новостей
	items, err := a.fetcher.Fetch(ctx)
	if err != nil {
		a.metrics.SetError(err.Error())
		logger.Error("Fetch error", "err", err)
		return
	}

	// Обновляем метрики
	for range items {
		a.metrics.IncrementNewsProcessed()
	}

	// 6. Фильтрация и перевод
	filtered, err := a.processor.Process(ctx, items)
	if err != nil {
		a.metrics.SetError(err.Error())
		logger.Error("Filter error", "err", err)
		return
	}

	// 7. Публикация: ровно одна лучшая новость.
	// filtered уже отсортирован по score (best first) в news.FilterAndTranslateWithOptions.
	// Send берёт первую не-дубликат и публикует. Остальные игнорируются.
	logger.Info("Publish policy", "mode", "single", "publish_limit_per_run", publishPerRunLimit)
	a.sender.Send(ctx, filtered)
	a.metrics.SetLastRun()
}

// CheckHealth performs health checks on components
func (a *App) CheckHealth(_ context.Context) map[string]string {
	status := make(map[string]string)
	status["app"] = "ok"

	// ИСПРАВЛЕНО: Убрал ошибочный код.
	// Так как у нас общий интерфейс, мы просто проверяем наличие адаптера.
	if a.cacheAdapter != nil {
		status["db"] = "connected"
	} else {
		status["db"] = "error"
	}

	// Check AI manager
	if a.aiManager != nil {
		status["ai"] = "ready"
	} else {
		status["ai"] = "not_initialized"
	}

	return status
}

// ReloadConfig reloads configuration (Hot Reload)
func (a *App) ReloadConfig() error {
	logger.Info("Reloading configuration...")

	// Reload feeds
	feeds, err := rss.LoadFeeds(a.cfg.RSS.FeedsConfigPath)
	if err != nil {
		return fmt.Errorf("failed to reload feeds: %v", err)
	}
	if rssFetcher, ok := a.fetcher.(*RSSFetcher); ok {
		rssFetcher.feeds = feeds
	}

	// Reload keywords
	keywords, err := config.LoadKeywords(a.cfg.RSS.KeywordsConfigPath)
	if err != nil {
		logger.Warn("Failed to reload keywords, keeping old ones", "error", err)
	} else {
		a.keywords = keywords
		if filterProcessor, ok := a.processor.(*NewsFilterProcessor); ok {
			filterProcessor.keywords = keywords
		}
	}

	logger.Info("Configuration reloaded successfully")
	return nil
}

// sendOneNews formats and sends a single news item to Telegram.
// On failure the item is saved to DLQ for retry on next run.
func sendOneNews(ctx context.Context, n news.News, hash string, cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics, websiteGen *website.Generator, supabase *storage.SupabaseClient) {
	funFactOriginal := strings.TrimSpace(n.FunFact)
	if funFactOriginal == "" {
		logger.Info("fun_fact missing from AI response", "title", n.Title)
	} else {
		logger.Info("fun_fact received from AI", "title", n.Title, "length", len([]rune(funFactOriginal)))
	}

	if funFactOriginal != "" && cacheAdapter.IsFunFactRecentlyUsed(funFactOriginal) {
		logger.Info("dropping repeated fun_fact for this run", "title", n.Title, "fun_fact_preview", truncateForLog(funFactOriginal, 80))
		n.FunFact = ""
	}

	videoURL := news.ExtractVideoURL(n)
	canPhoto := news.ShouldUsePhoto(n, cfg.Posting.PhotoTextLimit)
	if videoURL != "" {
		// Telegram does not render web link preview cards in photo captions.
		// For video-linked stories, force text mode so YouTube preview is visible.
		canPhoto = false
	}
	logger.Info("telegram render mode decision",
		"title", n.Title,
		"has_image", n.ImageURL != "",
		"has_video_url", videoURL != "",
		"use_photo", canPhoto,
		"photo_text_limit", cfg.Posting.PhotoTextLimit,
		"has_fun_fact", strings.TrimSpace(n.FunFact) != "",
		"has_why_it_matters", strings.TrimSpace(n.WhyItMatters) != "")
	if n.ImageURL != "" && !canPhoto {
		if videoURL != "" {
			logger.Info("📝 Photo skipped — video preview requires text mode", "title", n.Title, "video_url", videoURL)
		} else {
			logger.Info("📝 Photo skipped — content too long for caption, using text mode", "title", n.Title)
		}
	}

	var outText string
	var err error

	var buttons [][]telegram.InlineButton
	if cfg.Feature.EnableInlineButtons && n.Link != "" {
		buttons = append(buttons, []telegram.InlineButton{
			{Text: "🔗 Читати оригінал / Læs mere", URL: n.Link},
		})
		if videoURL != "" && videoURL != n.Link {
			buttons = append(buttons, []telegram.InlineButton{
				{Text: "🎬 Дивитись відео", URL: videoURL},
			})
			logger.Info("video link detected for telegram post", "title", n.Title, "video_url", videoURL)
		}
	}

	const maxVideoSeconds = 180

	// ── Video pipeline ──────────────────────────────────────────
	videoSent := false

	if n.VideoURL != "" {
		duration, err := telegram.GetVideoDurationSeconds(n.VideoURL)
		if err != nil {
			logger.Warn("[VIDEO] Duration unknown, skipping video", "url", n.VideoURL, "err", err)
		} else if duration > maxVideoSeconds {
			logger.Info("[VIDEO] Too long, skipping", "duration_sec", duration)
		} else {
			logger.Info("[VIDEO] Short video detected", "duration_sec", duration)

			// Determine caption
			videoCaption := ""
			if canPhoto { // Or we can format as caption explicitly since it's a media
				videoCaption = news.FormatCaptionForPhoto(n, cfg.Posting.PhotoTextLimit)
			} else {
				videoCaption = news.FormatCaptionForPhoto(n, 1024)
			}

			// Level 1: native stream upload
			if telegram.IsYouTubeURL(n.VideoURL) {
				reader, size, streamErr := telegram.GetYouTubeStream(n.VideoURL)
				if streamErr != nil {
					logger.Warn("[VIDEO L1] YouTube stream failed", "err", streamErr)
				} else {
					defer reader.Close()
					err = telegram.SendVideoStream(
						cfg.Telegram.Token, cfg.Telegram.ChatID,
						reader, size, "video.mp4",
						videoCaption, buttons,
					)
					if err != nil {
						logger.Warn("[VIDEO L1] Upload failed", "err", err)
					} else {
						logger.Info("[VIDEO L1] Sent natively")
						videoSent = true
					}
				}
			} else if telegram.IsDRDirectVideo(n.VideoURL) {
				// DR direct .mp4 — try as URL first (Telegram fetches it)
				err = telegram.SendVideoURL(
					cfg.Telegram.Token, cfg.Telegram.ChatID,
					n.VideoURL, videoCaption, buttons,
				)
				if err != nil {
					logger.Warn("[VIDEO L1] DR direct URL failed", "err", err)
				} else {
					logger.Info("[VIDEO L1] DR video sent via URL")
					videoSent = true
				}
			}

			// Level 2: embed preview (if Level 1 failed or not applicable)
			if !videoSent {
				videoCaption = news.FormatNewsWithImage(n) // embed context usually expects full text
				textWithLink := videoCaption + "\n\n🎥 " + n.VideoURL
				_, err = telegram.SendVideoEmbed(
					cfg.Telegram.Token, cfg.Telegram.ChatID,
					n.VideoURL, textWithLink, buttons,
				)
				if err != nil {
					logger.Warn("[VIDEO L2] Embed failed, falling to photo", "err", err)
				} else {
					logger.Info("[VIDEO L2] Sent as embed preview")
					videoSent = true
				}
			}
		}
	}

	if !videoSent {
		if canPhoto {
			outText = news.FormatCaptionForPhoto(n, cfg.Posting.PhotoTextLimit)
			if len(buttons) > 0 {
				err = telegram.SendPhotoWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, n.ImageURL, outText, buttons)
			} else {
				err = telegram.SendPhoto(cfg.Telegram.Token, cfg.Telegram.ChatID, n.ImageURL, outText)
			}
		} else {
			outText = news.FormatNewsWithImage(n)
			if len(buttons) > 0 {
				_, err = telegram.SendMessageWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, outText, buttons, true, 0)
			} else {
				_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, outText)
			}
		}
	}

	if err != nil {
		logger.Error("Failed to send telegram message", "title", n.Title, "error", err)
		if saveErr := cacheAdapter.SaveFailedNews(n.Title, n.Link, n.ImageURL, outText, err.Error()); saveErr != nil {
			logger.Error("Failed to save to DLQ", "error", saveErr)
		}
		return
	}

	// Telegram send succeeded — mark in Neon, then push to Supabase async-safe.
	_ = cacheAdapter.MarkAsSentWithContent(hash, n.Title, n.Link, n.Content, n.Category, n.SourceName)
	if strings.TrimSpace(n.FunFact) != "" {
		if err := cacheAdapter.MarkFunFactUsed(n.FunFact); err != nil {
			logger.Warn("failed to mark fun_fact usage", "title", n.Title, "error", err)
		}
	}
	m.IncrementTelegramMessagesSent()

	if supabase != nil {
		saveToSupabase(ctx, cacheAdapter, supabase, hash, n)
	}
	if websiteGen != nil && websiteGen.IsEnabled() {
		generateWebsitePost(websiteGen, n)
	}
}

// sendBestNews publishes EXACTLY ONE news item — the highest-scored non-duplicate.
// newsList MUST already be sorted by score descending (done by FilterAndTranslateWithOptions).
// This is a hard architectural rule, not a config knob: one run = one publication.
func sendBestNews(ctx context.Context, newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics, websiteGen *website.Generator, supabase *storage.SupabaseClient) {
	candidates := newsList
	if cfg.Feature.EnablePublicImpactGate {
		impactCandidates := make([]news.News, 0, len(newsList))
		for _, n := range newsList {
			if news.PassesAudienceRelevanceGate(n) {
				impactCandidates = append(impactCandidates, n)
			}
		}
		if len(impactCandidates) > 0 {
			logger.Info("Public impact gate enabled: using only relevance-checked candidates",
				"before", len(newsList),
				"after", len(impactCandidates))
			candidates = impactCandidates
		} else {
			logger.Info("Public impact gate enabled: NO candidates passed the audience relevance gate, returning to avoid publishing noise",
				"candidates", len(newsList))
			return
		}
	}

	// Iterate through candidates (ranked highest to lowest)
	// Stop at the first valid, non-duplicate candidate to publish it.
	var published *news.News
	var publishedRank int
	var publishedHash string

	// We'll collect the skipped ones just to compute next best for the diff log
	var nextValid *news.News
	foundValidCount := 0

	for i, n := range candidates {
		hash := cacheAdapter.GenerateNewsHash(n.Title, n.Link)
		logger.Info("evaluating publish candidate",
			"rank", i+1,
			"title", n.Title,
			"score", n.Score,
			"impact_score", n.ImpactScore,
			"keyword_score", n.KeywordScore,
			"category", n.Category,
			"source", n.SourceName,
			"hash", hash,
			"link", n.Link,
			"content_len", len([]rune(strings.TrimSpace(n.Content))),
			"core_impact_score", n.CoreImpactScore,
			"soft_score", n.SoftScore,
			"editorial_adjustment", n.EditorialAdjustment,
			"passes_public_impact_gate", news.PassesPublicImpactGate(n),
			"passes_audience_gate", news.PassesAudienceRelevanceGate(n))

		if cacheAdapter.IsAlreadySent(hash) {
			logger.Info("⏭️ Skipping already-sent hash", "title", n.Title, "hash", hash)
			continue
		}
		if cacheAdapter.IsSourceURLSent(n.Link) {
			logger.Info("⏭️ Skipping already-sent source_url (Neon dedup)", "title", n.Title, "source_url", n.Link)
			if published == nil {
				m.IncrementDuplicatesFiltered()
			}
			continue
		}
		if isDuplicate, existingTitle := cacheAdapter.IsTitleNearDuplicate(n.Title); isDuplicate {
			logger.Info("⏭️ Skipping near-duplicate title (same story, different source)",
				"new_title", n.Title,
				"existing_title", existingTitle)
			if published == nil {
				m.IncrementDuplicatesFiltered()
			}
			continue
		}
		if isDuplicate, existingTitle := cacheAdapter.IsContentDuplicate(n.Content); isDuplicate {
			logger.Info("⏭️ Skipping duplicate news (same content found)",
				"new_title", n.Title,
				"existing_title", existingTitle,
				"content_len", len([]rune(strings.TrimSpace(n.Content))))
			if published == nil {
				m.IncrementDuplicatesFiltered()
			}
			continue
		}

		// It's a valid candidate!
		foundValidCount++

		if published == nil {
			// This is our winner!
			nCopy := n
			published = &nCopy
			publishedRank = i + 1
			publishedHash = hash

			// We continue the loop ONLY ONE more valid time to find "nextValid" for diff log
		} else if nextValid == nil {
			// This is the second valid candidate
			nCopy := n
			nextValid = &nCopy
			break // We have the winner and the runner-up, we can stop evaluating
		}
	}

	if published == nil {
		logger.Info("No publishable news found in this run")
		return
	}

	if foundValidCount == 1 && published.Score < 70 {
		logger.Info("Only one low-score candidate, skipping run to avoid publishing noise", "score", published.Score, "title", published.Title)
		return
	}

	// P3 QUALITY METRICS: Track dedup quality drops
	if publishedRank > 1 {
		logger.Warn("Quality drop detected: publication went to rank > 1 due to dedup of top candidates",
			"published_rank", publishedRank,
			"skipped_count", publishedRank-1,
			"published_title", published.Title)
	}

	// P3 OBSERVABILITY: Log diff to next best candidate to explain "why this winner won"
	diffLog := []interface{}{
		"winner_title", published.Title,
		"winner_score", published.Score,
		"winner_category", published.Category,
		"winner_kw_score", published.KeywordScore,
		"winner_original_rank", publishedRank,
	}
	if nextValid != nil {
		diffLog = append(diffLog,
			"next_title", nextValid.Title,
			"next_score", nextValid.Score,
			"next_category", nextValid.Category,
			"score_diff", published.Score-nextValid.Score)
	} else {
		diffLog = append(diffLog, "next_title", "none")
	}
	logger.Info("Winner reason & evaluation result", diffLog...)

	logger.Info("Publishing best news", "title", published.Title, "score", published.Score, "category", published.Category, "rank", publishedRank)
	sendOneNews(ctx, *published, publishedHash, cfg, cacheAdapter, m, websiteGen, supabase)
}

func truncateForLog(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "..."
}

// generateWebsitePost converts news.News to website.NewsPost and generates the post with timeout.
func generateWebsitePost(gen *website.Generator, n news.News) {
	post := website.NewsPost{
		Title:            n.Title,
		TitleUkrainian:   n.TitleUkrainian,
		Content:          n.Content,
		ContentUkrainian: n.SummaryUkrainian,
		ContentDanish:    n.SummaryDanish,
		SummaryUkrainian: n.SummaryUkrainian,
		SummaryDanish:    n.SummaryDanish,
		Link:             n.Link,
		ImageURL:         n.ImageURL,
		SourceName:       n.SourceName,
		Category:         n.Category,
		Tags:             n.Tags,
		Mood:             n.Mood,
		TLDR:             n.TLDR,
		FunFact:          n.FunFact,
		WhyItMatters:     n.WhyItMatters,
		PublishedAt:      n.Published,
	}

	ctx := context.Background()
	if err := gen.GeneratePostWithTimeout(ctx, post, 10*time.Second); err != nil {
		logger.Warn("Failed to generate website post", "title", n.Title, "error", err)
	} else {
		logger.Info("Generated website post", "title", n.Title)
	}
}

// saveToSupabase saves news to Supabase.
// On success → marks the row in Neon as supabase_synced=TRUE.
// On failure → enqueues payload in supabase_sync_queue for retry next run.
// The duplicate check against Supabase has been removed: Neon IsSourceURLSent is called
// before Telegram send, so by the time we reach here the news is guaranteed unique.
func saveToSupabase(ctx context.Context, cacheAdapter CacheAdapter, client *storage.SupabaseClient, hash string, n news.News) {
	archive := storage.NewsArchive{
		Slug:             storage.GenerateSlugWithDate(n.Title, n.Published),
		Title:            n.Title,
		TitleUkrainian:   n.TitleUkrainian,
		SummaryUkrainian: n.SummaryUkrainian,
		SummaryDanish:    n.SummaryDanish,
		TLDR:             n.TLDR,
		FunFact:          n.FunFact,
		WhyItMatters:     n.WhyItMatters,
		ImageURL:         n.ImageURL,
		SourceURL:        n.Link,
		SourceName:       n.SourceName,
		Category:         n.Category,
		Tags:             n.Tags,
		Mood:             n.Mood,
		PublishedAt:      n.Published,
	}

	if err := client.SaveNews(ctx, archive); err != nil {
		logger.Warn("Failed to save to Supabase, enqueuing for retry",
			"title", n.Title, "error", err)
		// Serialise payload so we can retry without AI/scraping again.
		if payload, jsonErr := marshalSyncPayload(archive); jsonErr == nil {
			if qErr := cacheAdapter.EnqueueSupabaseSync(hash, payload); qErr != nil {
				logger.Error("Failed to enqueue Supabase sync", "hash", hash, "error", qErr)
			}
		}
	} else {
		logger.Info("Saved to Supabase archive", "title", n.Title)
		if sErr := cacheAdapter.MarkSupabaseSynced(hash); sErr != nil {
			logger.Warn("Failed to mark supabase_synced in Neon", "hash", hash, "error", sErr)
		}
	}
}

// marshalSyncPayload serialises a NewsArchive to JSON for the sync queue.
func marshalSyncPayload(archive storage.NewsArchive) ([]byte, error) {
	return json.Marshal(archive)
}

// unmarshalSyncPayload deserialises a NewsArchive from the sync queue payload.
func unmarshalSyncPayload(data []byte, out *storage.NewsArchive) error {
	return json.Unmarshal(data, out)
}

// syncPendingToSupabase flushes supabase_sync_queue entries from previous failed runs.
// Runs once per bot invocation, before the main fetch cycle.
func syncPendingToSupabase(ctx context.Context, cacheAdapter CacheAdapter, client *storage.SupabaseClient) {
	items, err := cacheAdapter.GetPendingSupabaseSync(10)
	if err != nil {
		logger.Error("Failed to get pending Supabase sync items", "error", err)
		return
	}
	if len(items) == 0 {
		return
	}
	logger.Info("Syncing pending items to Supabase", "count", len(items))

	for _, item := range items {
		var archive storage.NewsArchive
		if err := unmarshalSyncPayload(item.Payload, &archive); err != nil {
			logger.Error("Failed to unmarshal sync payload", "id", item.ID, "error", err)
			_ = cacheAdapter.IncrementSyncQueueAttempts(item.ID, err.Error())
			continue
		}

		if err := client.SaveNews(ctx, archive); err != nil {
			logger.Warn("Supabase sync retry failed", "id", item.ID, "error", err)
			_ = cacheAdapter.IncrementSyncQueueAttempts(item.ID, err.Error())
		} else {
			logger.Info("Supabase sync retry succeeded", "id", item.ID, "slug", archive.Slug)
			_ = cacheAdapter.DeleteSyncQueueItem(item.ID)
			_ = cacheAdapter.MarkSupabaseSynced(item.SentNewsHash)
		}
	}
}

// processFailedMessages принимает теперь любой CacheAdapter (Interface), а не конкретный тип
func processFailedMessages(adapter CacheAdapter, cfg *config.Config, m *metrics.Metrics) {
	// Мы просим адаптер: "Дай мне список ошибок".
	// Нам плевать, откуда он их возьмет.
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
				err = telegram.SendPhotoWithButtons(cfg.Telegram.Token, cfg.Telegram.ChatID, item.ImageURL, item.MessageText, buttons)
			} else {
				err = telegram.SendPhoto(cfg.Telegram.Token, cfg.Telegram.ChatID, item.ImageURL, item.MessageText)
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
			// Вызываем метод интерфейса
			if updateErr := adapter.IncrementFailedAttempts(item.ID, err.Error()); updateErr != nil {
				logger.Error("Failed to update DLQ attempts", "error", updateErr)
			}
		} else {
			logger.Info("Successfully resent DLQ item", "id", item.ID)
			// Вызываем метод интерфейса
			if delErr := adapter.DeleteFailedNews(item.ID); delErr != nil {
				logger.Error("Failed to delete DLQ item", "error", delErr)
			}

			// Также отмечаем как отправленное в основной таблице
			hash := adapter.GenerateNewsHash(item.Title, item.Link)
			_ = adapter.MarkAsSent(hash, item.Title, item.Link, "DLQ", "DLQ")
			m.IncrementTelegramMessagesSent()
		}
	}
}
