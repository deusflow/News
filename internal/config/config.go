// Package config is kept for future configuration loading (currently unused).
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
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

	// RSS settings
	FeedsConfigPath string
	MaxNewsLimit    int
	NewsMaxAge      time.Duration

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

}

func Load() (*Config, error) {
	cfg := &Config{
		// Default values
		FeedsConfigPath:         "configs/feeds.yaml",
		MaxGeminiRequests:       3, // default limit, change as needed
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
	}

	// Load from environment
	cfg.TelegramToken = os.Getenv("TELEGRAM_TOKEN")
	cfg.TelegramChatID = os.Getenv("TELEGRAM_CHAT_ID")
	cfg.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")

	// Cache settings
	cfg.CacheFilePath = getEnvOrDefault("CACHE_FILE_PATH", "sent_news.json")
	cfg.CacheTTLHours = getEnvIntOrDefault("CACHE_TTL_HOURS", 48)
	cfg.DuplicateWindow = getEnvIntOrDefault("DUPLICATE_WINDOW_HOURS", 24)

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
