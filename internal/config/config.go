// Package config is kept for future configuration loading (currently unused).
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Telegram settings
	TelegramToken  string
	TelegramChatID string
	BotMode        string // "single" or "multiple"

	// Posting/formatting policy
	PostingPolicy           string // hybrid | photo-only | text-only | two-messages (reserved)
	PhotoCaptionMaxRunes    int    // target/max caption budget for photo mode (~900)
	PhotoMinPerLangRunes    int    // minimal budget per language in photo caption (≥120)
	PhotoSentencesPerLang   int    // sentences per language in photo mode (1 or 2)
	TextSentencesPerLangMin int    // 2 by default
	TextSentencesPerLangMax int    // 4 by default
	MinSummaryTotalRunes    int    // minimal informativeness threshold to consider content "full"
	LanguagePriority        string // "uk" | "da" | "auto" (future use)

	// Gemini settings
	GeminiAPIKey      string
	MaxGeminiRequests int // maximum Gemini requests per run (0 = unlimited)

	// AI Rate Limiting (NEW - saves tokens!)
	MaxGroqRequests    int  // maximum Groq requests per run (0 = unlimited)
	MaxCohereRequests  int  // maximum Cohere requests per run (0 = unlimited)
	MaxMistralRequests int  // maximum Mistral requests per run (0 = unlimited)
	MaxTotalAIRequests int  // maximum total AI requests per run (0 = unlimited)
	EnableBatching     bool // enable batch processing for AI requests (saves ~40% tokens)
	BatchSize          int  // number of news items to process in one AI request (2-3 recommended)

	// RSS settings
	FeedsConfigPath    string
	KeywordsConfigPath string
	MaxNewsLimit       int
	NewsMaxAge         time.Duration

	// Scraper settings
	ScrapeConcurrency int // parallel fetches for full article extraction
	ScrapeMaxArticles int // cap of articles to extract per run

	// App settings
	Debug          bool
	RequestTimeout time.Duration
	RetryAttempts  int
	RetryDelay     time.Duration

	// Cache settings
	CacheFilePath   string
	CacheTTLHours   int
	DuplicateWindow int // hours for duplicate detection

	// PostgreSQL settings
	DatabaseURL string
	UsePostgres bool // if true, use PostgreSQL instead of file cache
	DatabaseTTL int  // hours to keep records in database

	// Feature flags
	EnableThreadMode     bool
	EnableImportanceLine bool
	EnableVocabPost      bool
	EnableInlineButtons  bool
	VocabWordsPerDay     int
	InlineButtonMode     string // "callback" or "url"
	ChannelUsername      string // for building URL buttons (e.g. deusflow_news)

	// NEW: ImportanceTopN field
	ImportanceTopN int // show importance line only for first N news (0 = all)

	// Monitoring settings
	EnableHTTPMonitoring bool
	MonitoringPort       string
}

// KeywordsConfig holds the keywords for filtering
type KeywordsConfig struct {
	RefugeeBoost []string `yaml:"refugee_boost"`
	UkraineWar   []string `yaml:"ukraine_war"`
	Viborg       []string `yaml:"viborg"`
	Economy      []string `yaml:"economy"`
	Construction []string `yaml:"construction"`
	Leisure      []string `yaml:"leisure"`
	Exclude      []string `yaml:"exclude"`
}

func LoadKeywords(path string) (*KeywordsConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg map[string]KeywordsConfig
	dec := yaml.NewDecoder(f)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}

	// The yaml structure is:
	// keywords:
	//   refugee_boost: ...
	// So we need to extract "keywords" key
	kw, ok := cfg["keywords"]
	if !ok {
		return nil, fmt.Errorf("keywords key not found in config")
	}
	return &kw, nil
}

func Load() (*Config, error) {
	cfg := &Config{
		// Default values
		FeedsConfigPath:         "configs/feeds.yaml",
		KeywordsConfigPath:      "configs/keywords.yaml",
		MaxGeminiRequests:       2,    // lowered to 2 to avoid hitting limits, override via env
		MaxGroqRequests:         10,   // Groq is fast and free, allow more
		MaxCohereRequests:       5,    // Cohere has 100/month free limit
		MaxMistralRequests:      5,    // Mistral free tier
		MaxTotalAIRequests:      15,   // Total AI requests limit per run
		EnableBatching:          true, // Enable batching by default (saves 40% tokens)
		BatchSize:               2,    // Process 2 news items per AI request
		MaxNewsLimit:            8,
		NewsMaxAge:              24 * time.Hour,
		RequestTimeout:          30 * time.Second,
		RetryAttempts:           3,
		RetryDelay:              5 * time.Second,
		BotMode:                 "multiple",
		PostingPolicy:           "hybrid",
		PhotoCaptionMaxRunes:    900,
		PhotoMinPerLangRunes:    120,
		PhotoSentencesPerLang:   2,
		TextSentencesPerLangMin: 2,
		TextSentencesPerLangMax: 4,
		MinSummaryTotalRunes:    180,
		LanguagePriority:        "auto",
		ScrapeConcurrency:       8,
		ScrapeMaxArticles:       10,
		DatabaseTTL:             48, // default TTL for database records
		EnableThreadMode:        false,
		EnableImportanceLine:    true,
		EnableVocabPost:         true,
		EnableInlineButtons:     true,
		VocabWordsPerDay:        5,
		InlineButtonMode:        "callback",
		ChannelUsername:         "",
		ImportanceTopN:          0, // default to 0 (show importance line for all news)
		EnableHTTPMonitoring:    false,
		MonitoringPort:          "8080",
	}

	// Load from environment
	cfg.TelegramToken = os.Getenv("TELEGRAM_TOKEN")
	cfg.TelegramChatID = os.Getenv("TELEGRAM_CHAT_ID")
	cfg.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
	cfg.DatabaseURL = os.Getenv("DATABASE_URL")

	// Cache settings
	cfg.CacheFilePath = getEnvOrDefault("CACHE_FILE_PATH", "sent_news.json")
	cfg.CacheTTLHours = getEnvIntOrDefault("CACHE_TTL_HOURS", 48)
	cfg.DuplicateWindow = getEnvIntOrDefault("DUPLICATE_WINDOW_HOURS", 24)

	// Sync DatabaseTTL with CacheTTLHours if not explicitly set
	cfg.DatabaseTTL = getEnvIntOrDefault("DATABASE_TTL_HOURS", cfg.CacheTTLHours)

	if mode := os.Getenv("BOT_MODE"); mode != "" {
		cfg.BotMode = mode
	}

	if policy := os.Getenv("POSTING_POLICY"); policy != "" {
		cfg.PostingPolicy = policy
	}
	if v := os.Getenv("PHOTO_CAPTION_MAX_RUNES"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			cfg.PhotoCaptionMaxRunes = val
		}
	}
	if v := os.Getenv("PHOTO_MIN_PER_LANG_RUNES"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= 60 {
			cfg.PhotoMinPerLangRunes = val
		}
	}
	if v := os.Getenv("PHOTO_SENTENCES_PER_LANG"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && (val == 1 || val == 2) {
			cfg.PhotoSentencesPerLang = val
		}
	}
	if v := os.Getenv("TEXT_SENTENCES_PER_LANG_MIN"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= 1 {
			cfg.TextSentencesPerLangMin = val
		}
	}
	if v := os.Getenv("TEXT_SENTENCES_PER_LANG_MAX"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= cfg.TextSentencesPerLangMin {
			cfg.TextSentencesPerLangMax = val
		}
	}
	if v := os.Getenv("MIN_SUMMARY_TOTAL_RUNES"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			cfg.MinSummaryTotalRunes = val
		}
	}
	if v := os.Getenv("LANGUAGE_PRIORITY"); v != "" {
		cfg.LanguagePriority = v
	}

	if v := os.Getenv("SCRAPE_CONCURRENCY"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			cfg.ScrapeConcurrency = val
		}
	}
	if v := os.Getenv("SCRAPE_MAX_ARTICLES"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			cfg.ScrapeMaxArticles = val
		}
	}

	if debug := os.Getenv("DEBUG"); debug == "true" {
		cfg.Debug = true
	}

	if limit := os.Getenv("MAX_NEWS_LIMIT"); limit != "" {
		if val, err := strconv.Atoi(limit); err == nil && val > 0 {
			cfg.MaxNewsLimit = val
		}
	}

	// NEW: Read MAX_GEMINI_REQUESTS from env
	if gr := os.Getenv("MAX_GEMINI_REQUESTS"); gr != "" {
		if val, err := strconv.Atoi(gr); err == nil && val > 0 {
			cfg.MaxGeminiRequests = val
		}
	}

	// NEW: AI Rate Limiting configuration
	if v := os.Getenv("MAX_GROQ_REQUESTS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= 0 {
			cfg.MaxGroqRequests = val
		}
	}
	if v := os.Getenv("MAX_COHERE_REQUESTS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= 0 {
			cfg.MaxCohereRequests = val
		}
	}
	if v := os.Getenv("MAX_MISTRAL_REQUESTS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= 0 {
			cfg.MaxMistralRequests = val
		}
	}
	if v := os.Getenv("MAX_TOTAL_AI_REQUESTS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= 0 {
			cfg.MaxTotalAIRequests = val
		}
	}
	if v := os.Getenv("ENABLE_BATCHING"); v != "" {
		cfg.EnableBatching = v == "true"
	}
	if v := os.Getenv("BATCH_SIZE"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 && val <= 5 {
			cfg.BatchSize = val
		}
	}

	// NEW: Check if PostgreSQL should be used
	if usePg := os.Getenv("USE_POSTGRES"); usePg == "true" {
		cfg.UsePostgres = true
	}

	// NEW: Feature flags
	if v := os.Getenv("ENABLE_THREAD_MODE"); v == "true" {
		cfg.EnableThreadMode = true
	}
	if v := os.Getenv("ENABLE_IMPORTANCE_LINE"); v != "" {
		cfg.EnableImportanceLine = v == "true"
	}
	if v := os.Getenv("ENABLE_VOCAB_POST"); v != "" {
		cfg.EnableVocabPost = v == "true"
	}
	if v := os.Getenv("ENABLE_INLINE_BUTTONS"); v != "" {
		cfg.EnableInlineButtons = v == "true"
	}
	if v := os.Getenv("INLINE_BUTTON_MODE"); v != "" {
		if v == "url" || v == "callback" {
			cfg.InlineButtonMode = v
		}
	}
	if v := os.Getenv("CHANNEL_USERNAME"); v != "" {
		cfg.ChannelUsername = v
	}
	if v := os.Getenv("VOCAB_WORDS_PER_DAY"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 && val <= 12 {
			cfg.VocabWordsPerDay = val
		}
	}
	if v := os.Getenv("IMPORTANCE_TOP_N"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= 0 {
			cfg.ImportanceTopN = val
		}
	}

	if v := os.Getenv("ENABLE_HTTP_MONITORING"); v == "true" {
		cfg.EnableHTTPMonitoring = true
	}
	if v := os.Getenv("MONITORING_PORT"); v != "" {
		cfg.MonitoringPort = v
	}

	return cfg, cfg.Validate()
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvIntOrDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func (c *Config) Validate() error {
	if c.TelegramToken == "" {
		return fmt.Errorf("TELEGRAM_TOKEN is required")
	}
	if c.TelegramChatID == "" {
		return fmt.Errorf("TELEGRAM_CHAT_ID is required")
	}
	if c.GeminiAPIKey == "" {
		return fmt.Errorf("GEMINI_API_KEY is required")
	}
	if c.BotMode != "single" && c.BotMode != "multiple" {
		return fmt.Errorf("BOT_MODE must be 'single' or 'multiple'")
	}
	return nil
}
