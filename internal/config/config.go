// Package config is kept for future configuration loading (currently unused).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
	"sort"
)

const (
	// DefaultGeminiModel is the standard primary model for news analysis and translation
	DefaultGeminiModel = "gemini-3.5-flash"
)

type TelegramConfig struct {
	Token       string
	ChatID      string
	AdminChatID string
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
	VideoURLMaxBytes        int64  // max size for SendVideoURL (bytes)
	VideoMaxSeconds         int    // max duration for native video upload (seconds)
}

type AIConfig struct {
	GeminiAPIKey      string
	GeminiAPIKey2     string
	GeminiAPIKey3     string
	GeminiModel       string // model name, defaults to DefaultGeminiModel
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
	EnableInlineButtons    bool
	ChannelUsername        string // for building URL buttons (e.g. deusflow_news)
	EnablePublicImpactGate bool   // if true, publish stage prefers impactful/systemic news
	EnableDecisionLog      bool   // if true, logs full candidate decision blocks
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

	HTTPTimeoutSeconds           int // HTTP client timeout for Supabase requests
	DuplicateCheckTimeoutSeconds int // timeout for duplicate-check requests
	MaxRetries                   int // retry attempts for transient Supabase failures
	RetryBaseDelaySeconds        int // exponential backoff base delay
	RetryMaxDelaySeconds         int // exponential backoff max delay
}

type SemanticDedupConfig struct {
	Enable              bool    // if true, semantic deduplication is active
	ShadowMode          bool    // if true, log similarity and would_reject without dropping on embedding similarity
	Threshold           float64 // cosine similarity threshold for duplicate (default: 0.85)
	ClusterKeyThreshold float64 // token jaccard threshold for cluster key (default: 0.60)
	LookbackDays        int     // days to look back for duplicates (default: 7)
	EmbeddingModel      string  // default: "text-embedding-004"
}

type Config struct {
	Telegram      TelegramConfig
	Posting       PostingConfig
	AI            AIConfig
	RSS           RSSConfig
	Scraper       ScraperConfig
	App           AppConfig
	Cache         CacheConfig
	Database      DatabaseConfig
	Feature       FeatureConfig
	Monitoring    MonitoringConfig
	Website       WebsiteConfig
	Supabase      SupabaseConfig
	SemanticDedup SemanticDedupConfig
}

// Keyword represents a single keyword with its weight and category
type Keyword struct {
	Word     string `yaml:"word"`
	Category string `yaml:"category"`
	Weight   int    `yaml:"weight"`

	// normalizedWord stores lowercase/trimmed keyword for fast matching.
	normalizedWord string
	// wholeWordMatch is enabled for short keywords (<=4 runes) to avoid substring false positives.
	wholeWordMatch bool
}

// KeywordMatch describes one keyword hit for explainable ranking logs.
type KeywordMatch struct {
	Word      string
	Category  string
	Weight    int
	WholeWord bool
}

// KeywordsConfig holds the keywords for filtering
type KeywordsConfig struct {
	Keywords []Keyword `yaml:"keywords"`
}

// CalculateScore calculates the total score for a given text based on keywords.
//
// Backward-compatible wrapper around CalculateScoreDetailed.
func (kc *KeywordsConfig) CalculateScore(text string) (int, string) {
	total, topCategory, _ := kc.CalculateScoreDetailed(text)
	return total, topCategory
}

// CalculateScoreDetailed calculates keyword score and returns:
//  1. total score
//  2. top category by accumulated weight (excluding spam)
//  3. per-category accumulated weights (for impact-aware ranking)
func (kc *KeywordsConfig) CalculateScoreDetailed(text string) (int, string, map[string]int) {
	total, topCategory, categoryWeights, _ := kc.CalculateScoreDetailedWithMatches(text)
	return total, topCategory, categoryWeights
}

// CalculateScoreDetailedWithMatches returns score details and concrete keyword hits
// for explainable ranking logs.
func (kc *KeywordsConfig) CalculateScoreDetailedWithMatches(text string) (int, string, map[string]int, []KeywordMatch) {
	if kc == nil || len(kc.Keywords) == 0 {
		return 0, "", map[string]int{}, nil
	}

	lowerText := strings.ToLower(text)
	matches := make([]KeywordMatch, 0, 12)

	for i := range kc.Keywords {
		kw := &kc.Keywords[i]
		if kw.normalizedWord == "" {
			continue
		}

		matched := false
		if kw.wholeWordMatch {
			matched = containsWholeWord(lowerText, kw.normalizedWord)
		} else {
			matched = strings.Contains(lowerText, kw.normalizedWord)
		}

		if matched {
			matches = append(matches, KeywordMatch{
				Word:      kw.Word,
				Category:  kw.Category,
				Weight:    kw.Weight,
				WholeWord: kw.wholeWordMatch,
			})
		}
	}

	// Sort matches by weight descending
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Weight > matches[j].Weight
	})

	// Take only top 10 matches
	if len(matches) > 10 {
		matches = matches[:10]
	}

	totalScore := 0
	categoryWeights := make(map[string]int, 16) // category -> accumulated weight

	for _, m := range matches {
		totalScore += m.Weight
		categoryWeights[m.Category] += m.Weight
	}

	// Pick category with highest accumulated weight (ignore "spam" - it is a filter, not a label)
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

	return totalScore, topCategory, categoryWeights, matches
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

	wholeWordMaxRunes := getEnvIntOrDefault("KEYWORD_WHOLE_WORD_MAX_RUNES", 4)
	if wholeWordMaxRunes < 1 {
		wholeWordMaxRunes = 4
	}
	strictWordBoundaries := strings.EqualFold(os.Getenv("KEYWORDS_STRICT_WORD_MATCH"), "true")

	for i := range cfg.Keywords {
		word := strings.TrimSpace(cfg.Keywords[i].Word)
		if word == "" {
			continue
		}
		cfg.Keywords[i].Word = word
		cfg.Keywords[i].normalizedWord = strings.ToLower(word)
		cfg.Keywords[i].Category = strings.ToLower(strings.TrimSpace(cfg.Keywords[i].Category))
		cfg.Keywords[i].wholeWordMatch = strictWordBoundaries || len([]rune(cfg.Keywords[i].normalizedWord)) <= wholeWordMaxRunes
	}

	return &KeywordsConfig{
		Keywords: cfg.Keywords,
	}, nil
}

// containsWholeWord checks whether token appears in text with non-word boundaries.
// Word characters are letters/digits (Unicode-aware), so "ai" won't match
// "socialordfører" or "said", but will match "AI" as a standalone token.
func containsWholeWord(text, token string) bool {
	if token == "" {
		return false
	}
	for start := strings.Index(text, token); start >= 0; {
		end := start + len(token)
		leftOK := start == 0 || !isWordRune(lastRune(text[:start]))
		rightOK := end == len(text) || !isWordRune(firstRune(text[end:]))
		if leftOK && rightOK {
			return true
		}
		nextStart := start + len(token)
		if nextStart >= len(text) {
			break
		}
		offset := strings.Index(text[nextStart:], token)
		if offset < 0 {
			break
		}
		start = nextStart + offset
	}
	return false
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	last := rune(0)
	for _, r := range s {
		last = r
	}
	return last
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
			VideoURLMaxBytes:        40 * 1024 * 1024,
			VideoMaxSeconds:         180,
		},
		Scraper: ScraperConfig{
			Concurrency: 2,
			MaxArticles: 15,
		},
		Database: DatabaseConfig{
			TTL: 48, // default TTL for database records
		},
		Feature: FeatureConfig{
			EnableInlineButtons:    true,
			ChannelUsername:        "",
			EnablePublicImpactGate: true,
			EnableDecisionLog:      true,
		},
		Monitoring: MonitoringConfig{
			EnableHTTPMonitoring: false,
			Port:                 "8080",
		},
		Website: WebsiteConfig{
			ContentDir: "website/content",
		},
		Supabase: SupabaseConfig{
			HTTPTimeoutSeconds:           30,
			DuplicateCheckTimeoutSeconds: 2,
			MaxRetries:                   3,
			RetryBaseDelaySeconds:        2,
			RetryMaxDelaySeconds:         10,
		},
	}

	// Load from environment
	cfg.Telegram.Token = os.Getenv("TELEGRAM_TOKEN")
	cfg.Telegram.ChatID = os.Getenv("TELEGRAM_CHAT_ID")
	cfg.Telegram.AdminChatID = os.Getenv("TELEGRAM_ADMIN_ID")
	cfg.AI.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
	cfg.AI.GeminiAPIKey2 = os.Getenv("GEMINI_API_KEY_2")
	cfg.AI.GeminiAPIKey3 = os.Getenv("GEMINI_API_KEY_3")
	cfg.AI.GeminiModel = getEnvOrDefault("GEMINI_MODEL", DefaultGeminiModel)
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
	cfg.Posting.VideoURLMaxBytes = getEnvInt64OrDefault("VIDEO_URL_MAX_BYTES", cfg.Posting.VideoURLMaxBytes)
	cfg.Posting.VideoMaxSeconds = getEnvIntOrDefault("VIDEO_MAX_SECONDS", cfg.Posting.VideoMaxSeconds)

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
	if v := os.Getenv("ENABLE_PUBLIC_IMPACT_GATE"); v != "" {
		cfg.Feature.EnablePublicImpactGate = v == "true"
	}
	if v := os.Getenv("ENABLE_DECISION_LOG"); v != "" {
		cfg.Feature.EnableDecisionLog = v == "true"
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
	cfg.AI.Providers = normalizeProviders(cfg.AI.Providers)
	if len(cfg.AI.Providers) == 0 {
		return nil, fmt.Errorf("AI_PROVIDERS is empty after normalization")
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
	cfg.Supabase.HTTPTimeoutSeconds = getEnvIntOrDefault("SUPABASE_HTTP_TIMEOUT_SECONDS", cfg.Supabase.HTTPTimeoutSeconds)
	cfg.Supabase.DuplicateCheckTimeoutSeconds = getEnvIntOrDefault("SUPABASE_DUPLICATE_CHECK_TIMEOUT_SECONDS", cfg.Supabase.DuplicateCheckTimeoutSeconds)
	cfg.Supabase.MaxRetries = getEnvIntOrDefault("SUPABASE_MAX_RETRIES", cfg.Supabase.MaxRetries)
	cfg.Supabase.RetryBaseDelaySeconds = getEnvIntOrDefault("SUPABASE_RETRY_BASE_DELAY_SECONDS", cfg.Supabase.RetryBaseDelaySeconds)
	cfg.Supabase.RetryMaxDelaySeconds = getEnvIntOrDefault("SUPABASE_RETRY_MAX_DELAY_SECONDS", cfg.Supabase.RetryMaxDelaySeconds)

	if cfg.Supabase.MaxRetries < 1 {
		cfg.Supabase.MaxRetries = 1
	}
	if cfg.Supabase.RetryBaseDelaySeconds < 1 {
		cfg.Supabase.RetryBaseDelaySeconds = 1
	}
	if cfg.Supabase.RetryMaxDelaySeconds < cfg.Supabase.RetryBaseDelaySeconds {
		cfg.Supabase.RetryMaxDelaySeconds = cfg.Supabase.RetryBaseDelaySeconds
	}
	if cfg.Supabase.HTTPTimeoutSeconds < 1 {
		cfg.Supabase.HTTPTimeoutSeconds = 30
	}
	if cfg.Supabase.DuplicateCheckTimeoutSeconds < 1 {
		cfg.Supabase.DuplicateCheckTimeoutSeconds = 2
	}

	if cfg.Supabase.URL != "" && cfg.Supabase.ServiceKey != "" {
		cfg.Supabase.Enable = true
	}

	// Semantic deduplication settings
	cfg.SemanticDedup.Enable = getEnvBoolOrDefault("SEMANTIC_DEDUP_ENABLE", true)
	cfg.SemanticDedup.ShadowMode = getEnvBoolOrDefault("SEMANTIC_DEDUP_SHADOW_MODE", true)
	cfg.SemanticDedup.Threshold = getEnvFloatOrDefault("SEMANTIC_DEDUP_THRESHOLD", 0.85)
	cfg.SemanticDedup.ClusterKeyThreshold = getEnvFloatOrDefault("SEMANTIC_DEDUP_CLUSTER_KEY_THRESHOLD", 0.60)
	cfg.SemanticDedup.LookbackDays = getEnvIntOrDefault("SEMANTIC_DEDUP_LOOKBACK_DAYS", 7)
	cfg.SemanticDedup.EmbeddingModel = getEnvOrDefault("SEMANTIC_DEDUP_EMBEDDING_MODEL", "text-embedding-004")

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

func getEnvInt64OrDefault(key string, defaultValue int64) int64 {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.ParseInt(value, 10, 64); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvBoolOrDefault(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolVal, err := strconv.ParseBool(value); err == nil {
			return boolVal
		}
	}
	return defaultValue
}

func getEnvFloatOrDefault(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatVal, err := strconv.ParseFloat(value, 64); err == nil {
			return floatVal
		}
	}
	return defaultValue
}

func normalizeProviders(providers []string) []string {
	allowed := map[string]bool{"gemini": true, "groq": true}
	seen := map[string]bool{}
	ordered := make([]string, 0, len(providers))

	for _, p := range providers {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" || !allowed[p] || seen[p] {
			continue
		}
		ordered = append(ordered, p)
		seen[p] = true
	}

	if seen["gemini"] && seen["groq"] {
		return []string{"gemini", "groq"}
	}
	if len(ordered) == 0 {
		return []string{"gemini", "groq"}
	}
	return ordered
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
