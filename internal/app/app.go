package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/ai/embedding"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/scraper"
	"github.com/deusflow/News/internal/storage"
	"github.com/deusflow/News/internal/telegram"
)

// Service interfaces for SRP
type NewsFetcher interface {
	Fetch(ctx context.Context) ([]*rss.FeedItem, error)
	FetchDetailed(ctx context.Context) ([]*rss.FeedItem, map[string]rss.FeedStatus, error)
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

func (f *RSSFetcher) FetchDetailed(ctx context.Context) ([]*rss.FeedItem, map[string]rss.FeedStatus, error) {
	return rss.FetchAllFeedsDetailed(ctx, f.feeds)
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
		AICache:           p.cacheAdapter,
	})
}

// TelegramNewsSender implements NewsSender
type TelegramNewsSender struct {
	cfg            *config.Config
	cacheAdapter   CacheAdapter
	metrics        *metrics.Metrics
	supabaseClient *storage.SupabaseClient
	embedder       embedding.Embedder
}

func NewTelegramNewsSender(cfg *config.Config, cacheAdapter CacheAdapter, m *metrics.Metrics, supabase *storage.SupabaseClient, emb embedding.Embedder) *TelegramNewsSender {
	return &TelegramNewsSender{
		cfg:            cfg,
		cacheAdapter:   cacheAdapter,
		metrics:        m,
		supabaseClient: supabase,
		embedder:       emb,
	}
}

func (s *TelegramNewsSender) Send(ctx context.Context, newsList []news.News) {
	sendBestNews(ctx, newsList, s.cfg, s.cacheAdapter, s.metrics, s.supabaseClient, s.embedder)
}

type App struct {
	cfg            *config.Config
	metrics        *metrics.Metrics
	cacheAdapter   CacheAdapter
	aiManager      *ai.Manager
	fetcher        NewsFetcher
	processor      NewsProcessor
	sender         NewsSender
	keywords       *config.KeywordsConfig
	supabaseClient *storage.SupabaseClient
	embedder       embedding.Embedder
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

	// 2. Инициализация AI
	aiProviders := initAIProviders(cfg)
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
		cfg:            cfg,
		metrics:        m,
		cacheAdapter:   cacheAdapter,
		aiManager:      aiManager,
		fetcher:        NewRSSFetcher(feeds),
		processor:      NewNewsFilterProcessor(cfg, aiManager, m, keywords, cacheAdapter),
		sender:         NewTelegramNewsSender(cfg, cacheAdapter, m, supabaseClient, embedder),
		keywords:       keywords,
		supabaseClient: supabaseClient,
		embedder:       embedder,
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

	// 4.3 Публикация отложенных важных новостей (Delayed Posts Queue)
	processReadyDelayedPosts(ctx, a.cfg, a.cacheAdapter, a.metrics, a.supabaseClient)

	// 5. Скачивание новостей RSS
	items, feedStatuses, err := a.fetcher.FetchDetailed(ctx)
	monitorFeedHealth(a.cacheAdapter, a.cfg, feedStatuses)
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
	sentBefore := getSentMessagesCount(a.metrics)
	a.sender.Send(ctx, filtered)
	monitorEmptyRuns(a.cacheAdapter, a.cfg, a.metrics, sentBefore)
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
