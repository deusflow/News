package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/ai/embedding"
	"github.com/deusflow/News/internal/ai/gemini"
	"github.com/deusflow/News/internal/ai/groq"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/publisher"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/scraper"
	"github.com/deusflow/News/internal/storage"
	"github.com/deusflow/News/internal/telegram"
	"github.com/deusflow/News/internal/website"
)

// getPublishLimit returns the maximum number of news items to publish per run.
// Defaults to 1 (single post per run). Can be overridden via PUBLISH_LIMIT or MAX_NEWS_LIMIT env vars.
func getPublishLimit() int {
	if val := os.Getenv("PUBLISH_LIMIT"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
			return limit
		}
	}
	if val := os.Getenv("MAX_NEWS_LIMIT"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
			return limit
		}
	}
	return 1
}

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
	cfg          *config.Config
	aiMgr        *ai.Manager
	metrics      *metrics.Metrics
	keywords     *config.KeywordsConfig
	cacheAdapter CacheAdapter
}

func NewNewsFilterProcessor(cfg *config.Config, aiMgr *ai.Manager, m *metrics.Metrics, keywords *config.KeywordsConfig, cacheAdapter CacheAdapter) *NewsFilterProcessor {
	return &NewsFilterProcessor{
		cfg:          cfg,
		aiMgr:        aiMgr,
		metrics:      m,
		keywords:     keywords,
		cacheAdapter: cacheAdapter,
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
		Dedupe:            p.cacheAdapter,
	})
}

// TelegramNewsSender implements NewsSender
type TelegramNewsSender struct {
	cfg              *config.Config
	cacheAdapter     CacheAdapter
	metrics          *metrics.Metrics
	websiteGenerator *website.Generator
	supabaseClient   *storage.SupabaseClient
	embedder         embedding.Embedder
}

func NewTelegramNewsSender(cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics, websiteGen *website.Generator, supabase *storage.SupabaseClient, emb embedding.Embedder) *TelegramNewsSender {
	return &TelegramNewsSender{
		cfg:              cfg,
		cacheAdapter:     cacheAdapter,
		metrics:          m,
		websiteGenerator: websiteGen,
		supabaseClient:   supabase,
		embedder:         emb,
	}
}

func (s *TelegramNewsSender) Send(ctx context.Context, newsList []news.News) {
	sendBestNews(ctx, newsList, s.cfg, s.cacheAdapter, s.metrics, s.websiteGenerator, s.supabaseClient, s.embedder)
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
	embedder         embedding.Embedder
}

// GetAIManager returns the AI Manager instance.
func (a *App) GetAIManager() *ai.Manager {
	return a.aiManager
}

// GetSupabase returns the Supabase client instance.
func (a *App) GetSupabase() *storage.SupabaseClient {
	return a.supabaseClient
}

// GetCacheAdapter returns the CacheAdapter instance.
func (a *App) GetCacheAdapter() CacheAdapter {
	return a.cacheAdapter
}

func New(cfg *config.Config, m *metrics.Metrics) (*App, error) {
	logger.Init()
	logger.Info("Initializing Danish News Bot")
	logger.Info("Video config",
		"video_url_max_bytes", cfg.Posting.VideoURLMaxBytes,
		"video_max_seconds", cfg.Posting.VideoMaxSeconds)

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
			return nil, fmt.Errorf("failed to load cache: %w", err)
		}
		cacheAdapter = &FileCacheAdapter{cache: fileCache}
		logger.Info("Using File cache")
	}

	// 2. Инициализация AI (НОВАЯ ЛОГИКА)
	var aiProviders []ai.Provider

	for _, pName := range cfg.AI.Providers {
		switch strings.TrimSpace(strings.ToLower(pName)) {
		case "gemini":
			keys := []string{cfg.AI.GeminiAPIKey, cfg.AI.GeminiAPIKey2, cfg.AI.GeminiAPIKey3}
			client, err := gemini.NewClient(keys, cfg.AI.GeminiModel)
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
		return nil, fmt.Errorf("failed to load keywords config: %w", err)
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
				if archiveErr := supabaseClient.ArchiveOldNews(context.Background()); archiveErr != nil {
					logger.Warn("Failed to archive old news", "error", archiveErr)
				}
			}
		}
	}

	var embedder embedding.Embedder
	if cfg.SemanticDedup.Enable {
		keys := []string{cfg.AI.GeminiAPIKey, cfg.AI.GeminiAPIKey2, cfg.AI.GeminiAPIKey3}
		embClient, err := embedding.NewGeminiEmbedder(keys, cfg.SemanticDedup.EmbeddingModel)
		if err != nil {
			logger.Warn("Semantic dedup enabled but embedder failed to init (Tier 1 cluster key will be used)", "error", err)
		} else {
			embedder = embClient
			logger.Info("Semantic Dedup Embedder initialized",
				"model", cfg.SemanticDedup.EmbeddingModel,
				"shadow_mode", cfg.SemanticDedup.ShadowMode,
				"threshold", cfg.SemanticDedup.Threshold,
				"cluster_threshold", cfg.SemanticDedup.ClusterKeyThreshold,
				"lookback_days", cfg.SemanticDedup.LookbackDays)
		}
	}

	app := &App{
		cfg:              cfg,
		metrics:          m,
		cacheAdapter:     cacheAdapter,
		aiManager:        aiManager,
		fetcher:          NewRSSFetcher(feeds),
		processor:        NewNewsFilterProcessor(cfg, aiManager, m, keywords, cacheAdapter),
		sender:           NewTelegramNewsSender(cfg, cacheAdapter, m, websiteGen, supabaseClient, embedder),
		keywords:         keywords,
		websiteGenerator: websiteGen,
		supabaseClient:   supabaseClient,
		embedder:         embedder,
	}

	return app, nil
}

// Run запускает приложение
func (a *App) Run(ctx context.Context) {
	logger.Info("Starting Danish News Bot Run")
	runStartedAt := time.Now()
	defer func() { a.metrics.RecordProcessingTime(time.Since(runStartedAt)) }()

	// Fallback timeout: 20 minutes to allow thorough AI processing and retries.
	// GitHub Actions handles top-level job termination via timeout-minutes.
	const runTimeout = 20 * time.Minute
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	publishLimit := getPublishLimit()
	if maxEnv, ok := os.LookupEnv("MAX_NEWS_LIMIT"); ok {
		logger.Info("Using publish limit from env",
			"env", "MAX_NEWS_LIMIT",
			"value", maxEnv,
			"effective_publish_limit", publishLimit)
	}
	if modeEnv, ok := os.LookupEnv("BOT_MODE"); ok {
		logger.Info("Publishing mode",
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

	// 5. Скачивание новостей RSS
	items, err := a.fetcher.Fetch(ctx)
	if err != nil {
		a.metrics.SetError(err.Error())
		logger.Error("Fetch error", "err", err)
		_ = telegram.SendAdminAlert(a.cfg.Telegram.Token, a.cfg.Telegram.AdminChatID, fmt.Sprintf("❌ Failed to fetch RSS feeds:\n%v", err))
		return
	}

	// 5.5. Скачивание новостей Nyidanmark
	nyScraper := scraper.NewNyidanmarkScraper()
	nyLinks, err := nyScraper.ScrapeFrontpage(ctx)
	if err == nil {
		for _, link := range nyLinks {
			// Проверяем, не отправляли ли мы уже этот URL!
			if !a.cacheAdapter.IsSourceURLSent(link) {
				nyItem, err := nyScraper.ScrapeArticle(ctx, link)
				if err == nil && nyItem != nil {
					items = append(items, nyItem)
				}
			} else {
				logger.Info("Nyidanmark URL already sent, skipping scrape", "url", link)
			}
		}
	} else {
		logger.Warn("Failed to scrape Nyidanmark frontpage", "err", err)
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
		_ = telegram.SendAdminAlert(a.cfg.Telegram.Token, a.cfg.Telegram.AdminChatID, fmt.Sprintf("❌ Failed to filter/process news (AI API issue?):\n%v", err))
		return
	}

	// 7. Публикация: ровно одна лучшая новость.
	// filtered уже отсортирован по score (best first) в news.FilterAndTranslateWithOptions.
	// Send берёт первую не-дубликат и публикует. Остальные игнорируются.
	logger.Info("Publish policy", "mode", "single", "publish_limit_per_run", getPublishLimit())
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
		return fmt.Errorf("failed to reload keywords config: %w", err)
	}
	a.keywords = keywords
	if filterProcessor, ok := a.processor.(*NewsFilterProcessor); ok {
		filterProcessor.keywords = keywords
	}

	logger.Info("Configuration reloaded successfully")
	return nil
}

// sendBestNews publishes up to publishPerRunLimit news items — the highest-scored non-duplicates.
// sendBestNews publishes up to publishPerRunLimit news items — the highest-scored non-duplicates.
// newsList MUST already be sorted by score descending (done by FilterAndTranslateWithOptions).
func sendBestNews(ctx context.Context, newsList []news.News, cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics, websiteGen *website.Generator, supabase *storage.SupabaseClient, embedder embedding.Embedder) {
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

	publishLimit := getPublishLimit()

	// In-memory session deduplication guard for this run
	sessionHashes := make(map[string]bool)
	sessionUrls := make(map[string]bool)

	// Collect valid (non-duplicate) candidates up to publishLimit + 1 (for diff log)
	type validCandidate struct {
		news news.News
		hash string
		rank int // 1-based original rank in candidates list
	}
	var valid []validCandidate

	for i, n := range candidates {
		if len(valid) > publishLimit {
			break // We have enough: publishLimit to send + 1 for diff log
		}

		hash := cacheAdapter.GenerateNewsHash(n.Title, n.Link)
		if sessionHashes[hash] || (n.Link != "" && sessionUrls[n.Link]) {
			logger.Info("⏭️ Skipping session in-memory duplicate", "title", n.Title, "link", n.Link)
			continue
		}
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
			m.IncrementDuplicatesFiltered()
			continue
		}
		if isDuplicate, existingTitle := cacheAdapter.IsTitleNearDuplicate(n.Title); isDuplicate {
			logger.Info("⏭️ Skipping near-duplicate title (same story, different source)",
				"new_title", n.Title,
				"existing_title", existingTitle)
			m.IncrementDuplicatesFiltered()
			continue
		}
		if isDuplicate, existingTitle := cacheAdapter.IsContentDuplicate(n.Content); isDuplicate {
			logger.Info("⏭️ Skipping duplicate news (same content found)",
				"new_title", n.Title,
				"existing_title", existingTitle,
				"content_len", len([]rune(strings.TrimSpace(n.Content))))
			m.IncrementDuplicatesFiltered()
			continue
		}

		// Two-Tier Semantic Deduplication (Tier 1: story_cluster_key, Tier 2: text-embedding-004)
		if cfg.SemanticDedup.Enable {
			lookback := time.Duration(cfg.SemanticDedup.LookbackDays) * 24 * time.Hour
			if lookback <= 0 {
				lookback = 7 * 24 * time.Hour
			}

			// Generate embedding for candidate if embedder is available and vector not yet computed
			if embedder != nil && len(n.Embedding) == 0 {
				textToEmbed := n.TitleUkrainian
				if n.TLDR != "" {
					textToEmbed += ". " + n.TLDR
				}
				emb, err := embedder.Embed(ctx, textToEmbed)
				if err != nil {
					logger.Warn("Failed to generate embedding for candidate (falling back to Tier 1 cluster key)",
						"title", n.TitleUkrainian,
						"error", err)
				} else {
					n.Embedding = emb
				}
			}

			semRes, err := cacheAdapter.CheckSemanticDuplicate(
				n.StoryClusterKey,
				n.Embedding,
				n.TitleUkrainian,
				lookback,
				cfg.SemanticDedup.ClusterKeyThreshold,
				cfg.SemanticDedup.Threshold,
				cfg.SemanticDedup.ShadowMode,
			)
			if err == nil {
				if semRes.ShadowMode && semRes.WouldReject && !semRes.IsDuplicate {
					logger.Info("🔍 [SHADOW DEDUP] High semantic similarity detected (would reject if shadow mode was false)",
						"new_title", n.TitleUkrainian,
						"cluster_key", n.StoryClusterKey,
						"matched_title", semRes.MatchedTitle,
						"cosine_similarity", semRes.CosineSimilarity,
						"cluster_similarity", semRes.ClusterSimilarity,
						"trigger", semRes.Trigger,
						"threshold", cfg.SemanticDedup.Threshold)
				}

				if semRes.IsDuplicate {
					logger.Info("⏭️ Skipping semantic duplicate story",
						"new_title", n.TitleUkrainian,
						"cluster_key", n.StoryClusterKey,
						"matched_title", semRes.MatchedTitle,
						"trigger", semRes.Trigger,
						"cluster_similarity", semRes.ClusterSimilarity,
						"cosine_similarity", semRes.CosineSimilarity)
					m.IncrementDuplicatesFiltered()
					continue
				}
			}
		}

		sessionHashes[hash] = true
		if n.Link != "" {
			sessionUrls[n.Link] = true
		}
		valid = append(valid, validCandidate{news: n, hash: hash, rank: i + 1})
	}

	if len(valid) == 0 {
		logger.Info("No publishable news found in this run")
		return
	}

	// Safety: if only one low-score candidate, skip to avoid noise
	if len(valid) == 1 && valid[0].news.Score < 70 {
		logger.Info("Only one low-score candidate, skipping run to avoid publishing noise",
			"score", valid[0].news.Score, "title", valid[0].news.Title)
		return
	}

	// Determine how many to actually publish
	toPublish := min(publishLimit, len(valid))

	// Log winner vs runner-up diff
	diffLog := []interface{}{
		"winner_title", valid[0].news.Title,
		"winner_score", valid[0].news.Score,
		"winner_category", valid[0].news.Category,
		"winner_kw_score", valid[0].news.KeywordScore,
		"winner_original_rank", valid[0].rank,
		"total_to_publish", toPublish,
	}
	nextIdx := toPublish // first item NOT being published
	if nextIdx < len(valid) {
		diffLog = append(diffLog,
			"next_title", valid[nextIdx].news.Title,
			"next_score", valid[nextIdx].news.Score,
			"next_category", valid[nextIdx].news.Category,
			"score_diff", valid[0].news.Score-valid[nextIdx].news.Score)
	} else {
		diffLog = append(diffLog, "next_title", "none")
	}
	logger.Info("Winner reason & evaluation result", diffLog...)

	// Initialize publisher
	tgPublisher := publisher.NewTelegramPublisher(cfg, cacheAdapter, m)

	// Publish each item
	for idx := 0; idx < toPublish; idx++ {
		vc := valid[idx]

		if vc.rank > 1 && idx == 0 {
			logger.Warn("Quality drop detected: first publication went to rank > 1 due to dedup of top candidates",
				"published_rank", vc.rank,
				"skipped_count", vc.rank-1,
				"published_title", vc.news.Title)
		}

		logger.Info("Publishing news",
			"publish_index", idx+1,
			"of", toPublish,
			"title", vc.news.Title,
			"score", vc.news.Score,
			"category", vc.news.Category,
			"rank", vc.rank)

		_, success := tgPublisher.Publish(ctx, vc.news, vc.hash)
		if success {
			if supabase != nil {
				saveToSupabase(ctx, cacheAdapter, supabase, vc.hash, vc.news)
			}
			if websiteGen != nil && websiteGen.IsEnabled() {
				generateWebsitePost(websiteGen, vc.news)
			}
		}

		// Brief pause between sends to avoid Telegram flood limits
		if idx < toPublish-1 {
			time.Sleep(5 * time.Second)
		}
	}
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

	// Detach from parent cancellation so post-publication DB/Supabase sync is non-cancelable.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	if err := client.SaveNews(saveCtx, archive); err != nil {
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

			// Также отмечаем как отправленное в основной таблице (с сохранением title_norm и content_hash)
			hash := adapter.GenerateNewsHash(item.Title, item.Link)
			_ = adapter.MarkAsSentWithContent(hash, item.Title, item.Link, item.MessageText, "DLQ", "DLQ")
			m.IncrementTelegramMessagesSent()
		}
	}
}
