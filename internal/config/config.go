// Package config is kept for future configuration loading (currently unused).
package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type TelegramConfig struct {
	Token  string
	ChatID string
}

type PostingConfig struct {
	Policy                  string // hybrid | photo-only | text-only | two-messages (reserved)
	PhotoCaptionMaxRunes    int    // target/max caption budget for photo mode (~900)
	PhotoMinPerLangRunes    int    // minimal budget per language in photo caption (≥120)
	PhotoSentencesPerLang   int    // sentences per language in photo mode (1 or 2)
	TextSentencesPerLangMin int    // 2 by default
	TextSentencesPerLangMax int    // 4 by default
	MinSummaryTotalRunes    int    // minimal informativeness threshold to consider content "full"
	PhotoTextLimit          int    // limit for photo text (1024)
}

type AIConfig struct {
	GeminiAPIKey      string
	GeminiModel       string // model name, e.g. "gemini-2.5-flash"
	GroqModel         string // model name, e.g. "llama-3.3-70b-versatile" (default if empty)
	MaxGeminiRequests int    // maximum Gemini requests per run (0 = unlimited)
	GroqAPIKey        string
	Providers         []string // List of AI providers, e.g. ["gemini", "groq"]
}

type RSSConfig struct {
	FeedsConfigPath    string
	KeywordsConfigPath string
	NewsMaxAge         time.Duration
}

type ScraperConfig struct {
	Concurrency int // parallel fetches for full article extraction
	MaxArticles int // cap of articles to extract per run
}

type AppConfig struct {
	Debug          bool
	RequestTimeout time.Duration
	RetryAttempts  int
	RetryDelay     time.Duration
}

type CacheConfig struct {
	FilePath        string
	TTLHours        int
	DuplicateWindow int // hours for duplicate detection
}

type DatabaseConfig struct {
	URL         string
	UsePostgres bool // if true, use PostgreSQL instead of file cache
	TTL         int  // hours to keep records in database
}

type FeatureConfig struct {
	EnableInlineButtons bool
	ChannelUsername     string // for building URL buttons (e.g. deusflow_news)
}

type MonitoringConfig struct {
	EnableHTTPMonitoring bool
	Port                 string
}

type WebsiteConfig struct {
	Enable     bool   // if true, generate Hugo posts for website
	ContentDir string // path to Hugo content directory (e.g., "website/content")
}

type SupabaseConfig struct {
	URL        string // Supabase project URL
	ServiceKey string // Supabase service_role key
	Enable     bool   // if true, save news to Supabase archive
}

type Config struct {
	Telegram   TelegramConfig
	Posting    PostingConfig
	AI         AIConfig
	RSS        RSSConfig
	Scraper    ScraperConfig
	App        AppConfig
	Cache      CacheConfig
	Database   DatabaseConfig
	Feature    FeatureConfig
	Monitoring MonitoringConfig
	Website    WebsiteConfig
	Supabase   SupabaseConfig
}

// Keyword represents a single keyword with its weight and category
type Keyword struct {
	Word     string `yaml:"word"`
	Category string `yaml:"category"`
	Weight   int    `yaml:"weight"`

	// boundaryRe is pre-compiled at load time for short words (≤4 runes).
	// Words like "ai"(2), "su"(2), "sl1"(3), "tog"(3), "data"(4) must match whole
	// words only — otherwise "ai" fires inside "socialordfører", "data" fires inside
	// "opdatering"/"database", etc. (BUG-1/BUG-2 from audit).
	// Longer words are matched with strings.Contains (faster, no false positives).
	boundaryRe *regexp.Regexp
}

// KeywordsConfig holds the keywords for filtering
type KeywordsConfig struct {
	Keywords []Keyword `yaml:"keywords"`
}

// CalculateScore calculates the total score for a given text based on keywords.
//
// Two fixes vs the old implementation:
//
//  1. WORD BOUNDARY: short keywords (≤4 runes) use a pre-compiled \b regex so
//     "ai" never fires inside "socialordfører", "data" never fires inside
//     "opdatering"/"database", "su" never fires inside "resultat", etc.
//     Longer keywords still use strings.Contains (fast path, no false positives
//     at that length in Danish).
//
//  2. CATEGORY BY SUM: the returned category is the one whose keywords contributed
//     the most *total* weight, not the one with the single heaviest keyword.
//     Example: 5 "local" keywords × weight 10 = 50 beats 1 "visas" keyword × weight 20.
func (kc *KeywordsConfig) CalculateScore(text string) (int, string) {
	if kc == nil || len(kc.Keywords) == 0 {
		return 0, ""
	}

	lowerText := strings.ToLower(text)
	totalScore := 0
	categoryWeights := make(map[string]int, 16) // category → accumulated weight

	for i := range kc.Keywords {
		kw := &kc.Keywords[i]
		matched := false

		if kw.boundaryRe != nil {
			// Short word — must match at word boundary
			matched = kw.boundaryRe.MatchString(lowerText)
		} else {
			matched = strings.Contains(lowerText, strings.ToLower(kw.Word))
		}

		if matched {
			totalScore += kw.Weight
			categoryWeights[kw.Category] += kw.Weight
		}
	}

	// Pick category with highest accumulated weight (ignore "spam" — it is a filter, not a label)
	topCategory := ""
	maxCatWeight := 0
	for cat, w := range categoryWeights {
		if cat == "spam" {
			continue
		}
		if w > maxCatWeight {
			maxCatWeight = w
			topCategory = cat
		}
	}

	return totalScore, topCategory
}

func LoadKeywords(path string) (*KeywordsConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			fmt.Printf("Warning: failed to close config file %s: %v\n", path, closeErr)
		}
	}()

	var cfg KeywordsConfig
	dec := yaml.NewDecoder(f)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse keywords config: %w", err)
	}

	if len(cfg.Keywords) == 0 {
		return nil, fmt.Errorf("keywords array is empty in config")
	}

	// Pre-compile word-boundary regexes for short keywords (≤4 runes).
	// \b in Go's RE2 works correctly for ASCII word chars ([0-9A-Za-z_]).
	// Danish letters (æøå) are NOT word chars in RE2 — for Danish short words
	// we fall back to space/punctuation boundary check which is Unicode-safe.
	for i := range cfg.Keywords {
		word := cfg.Keywords[i].Word
		if len([]rune(word)) <= 4 {
			lower := strings.ToLower(word)
			// Use \b if word is pure ASCII (works for "ai", "su", "sl1", "tog" etc.)
			// Use Unicode boundary for words with non-ASCII runes.
			var pattern string
			if isASCII(lower) {
				pattern = `(?i)\b` + regexp.QuoteMeta(lower) + `\b`
			} else {
				pattern = `(?i)(?:^|[\s[:punct:]])` + regexp.QuoteMeta(lower) + `(?:[\s[:punct:]]|$)`
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				// Compilation failure is a bug in the pattern logic — fail fast at startup.
				return nil, fmt.Errorf("failed to compile boundary regex for keyword %q: %w", word, err)
			}
			cfg.Keywords[i].boundaryRe = re
		}
	}

	return &KeywordsConfig{
		Keywords: cfg.Keywords,
	}, nil
}

// isASCII reports whether s contains only ASCII characters.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

func Load() (*Config, error) {
	cfg := &Config{
		RSS: RSSConfig{
			FeedsConfigPath:    "configs/feeds.yaml",
			KeywordsConfigPath: "configs/keywords.yaml",
			NewsMaxAge:         24 * time.Hour,
		},
		AI: AIConfig{
			MaxGeminiRequests: 10, // Gemini is PRIMARY - handles all translation
		},
		App: AppConfig{
			RequestTimeout: 30 * time.Second,
			RetryAttempts:  3,
			RetryDelay:     5 * time.Second,
		},
		Telegram: TelegramConfig{},
		Posting: PostingConfig{
			Policy:                  "hybrid",
			PhotoCaptionMaxRunes:    900,
			PhotoMinPerLangRunes:    120,
			PhotoSentencesPerLang:   2,
			TextSentencesPerLangMin: 2,
			TextSentencesPerLangMax: 4,
			MinSummaryTotalRunes:    180,
			PhotoTextLimit:          1024,
		},
		Scraper: ScraperConfig{
			Concurrency: 1,
			MaxArticles: 10,
		},
		Database: DatabaseConfig{
			TTL: 48, // default TTL for database records
		},
		Feature: FeatureConfig{
			EnableInlineButtons: true,
			ChannelUsername:     "",
		},
		Monitoring: MonitoringConfig{
			EnableHTTPMonitoring: false,
			Port:                 "8080",
		},
		Website: WebsiteConfig{
			ContentDir: "website/content",
		},
	}

	// Load from environment
	cfg.Telegram.Token = os.Getenv("TELEGRAM_TOKEN")
	cfg.Telegram.ChatID = os.Getenv("TELEGRAM_CHAT_ID")
	cfg.AI.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
	cfg.AI.GeminiModel = getEnvOrDefault("GEMINI_MODEL", "gemini-2.5-flash")
	cfg.Database.URL = os.Getenv("DATABASE_URL")
	cfg.AI.GroqAPIKey = os.Getenv("GROQ_API_KEY")

	// Cache settings
	cfg.Cache.FilePath = getEnvOrDefault("CACHE_FILE_PATH", "sent_news.json")
	cfg.Cache.TTLHours = getEnvIntOrDefault("CACHE_TTL_HOURS", 48)
	cfg.Cache.DuplicateWindow = getEnvIntOrDefault("DUPLICATE_WINDOW_HOURS", 24)

	// Sync DatabaseTTL with CacheTTLHours if not explicitly set
	cfg.Database.TTL = getEnvIntOrDefault("DATABASE_TTL_HOURS", cfg.Cache.TTLHours)

	if policy := os.Getenv("POSTING_POLICY"); policy != "" {
		cfg.Posting.Policy = policy
	}
	if v := os.Getenv("PHOTO_CAPTION_MAX_RUNES"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			cfg.Posting.PhotoCaptionMaxRunes = val
		}
	}
	if v := os.Getenv("PHOTO_MIN_PER_LANG_RUNES"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= 60 {
			cfg.Posting.PhotoMinPerLangRunes = val
		}
	}
	if v := os.Getenv("PHOTO_SENTENCES_PER_LANG"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && (val == 1 || val == 2) {
			cfg.Posting.PhotoSentencesPerLang = val
		}
	}
	if v := os.Getenv("TEXT_SENTENCES_PER_LANG_MIN"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= 1 {
			cfg.Posting.TextSentencesPerLangMin = val
		}
	}
	if v := os.Getenv("TEXT_SENTENCES_PER_LANG_MAX"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= cfg.Posting.TextSentencesPerLangMin {
			cfg.Posting.TextSentencesPerLangMax = val
		}
	}
	if v := os.Getenv("MIN_SUMMARY_TOTAL_RUNES"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			cfg.Posting.MinSummaryTotalRunes = val
		}
	}

	if v := os.Getenv("SCRAPE_CONCURRENCY"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			cfg.Scraper.Concurrency = val
		}
	}
	if v := os.Getenv("SCRAPE_MAX_ARTICLES"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			cfg.Scraper.MaxArticles = val
		}
	}

	if debug := os.Getenv("DEBUG"); debug == "true" {
		cfg.App.Debug = true
	}

	// Read MAX_GEMINI_REQUESTS from env
	if gr := os.Getenv("MAX_GEMINI_REQUESTS"); gr != "" {
		if val, err := strconv.Atoi(gr); err == nil && val > 0 {
			cfg.AI.MaxGeminiRequests = val
		}
	}

	// Check if PostgreSQL should be used
	if usePg := os.Getenv("USE_POSTGRES"); usePg == "true" {
		cfg.Database.UsePostgres = true
	}

	// Feature flags
	if v := os.Getenv("ENABLE_INLINE_BUTTONS"); v != "" {
		cfg.Feature.EnableInlineButtons = v == "true"
	}
	if v := os.Getenv("CHANNEL_USERNAME"); v != "" {
		cfg.Feature.ChannelUsername = v
	}

	// AI Providers (default and env override)
	cfg.AI.Providers = []string{"gemini", "groq"}
	if providers := os.Getenv("AI_PROVIDERS"); providers != "" {
		// split by comma and trim spaces
		parts := strings.Split(providers, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		cfg.AI.Providers = parts
	}

	if v := os.Getenv("ENABLE_HTTP_MONITORING"); v == "true" {
		cfg.Monitoring.EnableHTTPMonitoring = true
	}
	if v := os.Getenv("MONITORING_PORT"); v != "" {
		cfg.Monitoring.Port = v
	}

	// Website generation settings
	if v := os.Getenv("ENABLE_WEBSITE"); v == "true" {
		cfg.Website.Enable = true
	}
	cfg.Website.ContentDir = getEnvOrDefault("WEBSITE_CONTENT_DIR", "website/content")

	// Supabase settings
	cfg.Supabase.URL = os.Getenv("SUPABASE_URL")
	cfg.Supabase.ServiceKey = os.Getenv("SUPABASE_SERVICE_KEY")
	if cfg.Supabase.URL != "" && cfg.Supabase.ServiceKey != "" {
		cfg.Supabase.Enable = true
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
	if c.Telegram.Token == "" {
		return fmt.Errorf("TELEGRAM_TOKEN is required")
	}
	if c.Telegram.ChatID == "" {
		return fmt.Errorf("TELEGRAM_CHAT_ID is required")
	}
	if c.AI.GeminiAPIKey == "" {
		return fmt.Errorf("GEMINI_API_KEY is required")
	}
	return nil
}
