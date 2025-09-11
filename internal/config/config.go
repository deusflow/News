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

	// Gemini settings
	GeminiAPIKey string

	// RSS settings
	FeedsConfigPath string
	MaxNewsLimit    int
	NewsMaxAge      time.Duration

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
		FeedsConfigPath: "configs/feeds.yaml",
		MaxNewsLimit:    8,
		NewsMaxAge:      24 * time.Hour,
		RequestTimeout:  30 * time.Second,
		RetryAttempts:   3,
		RetryDelay:      5 * time.Second,
		BotMode:         "multiple",
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

	if debug := os.Getenv("DEBUG"); debug == "true" {
		cfg.Debug = true
	}

	if limit := os.Getenv("MAX_NEWS_LIMIT"); limit != "" {
		if val, err := strconv.Atoi(limit); err == nil && val > 0 {
			cfg.MaxNewsLimit = val
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
