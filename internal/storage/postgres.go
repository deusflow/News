package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/deusflow/News/internal/ai/embedding"
	"github.com/deusflow/News/internal/logger"
	_ "github.com/lib/pq"
)

// PostgresCache manages sent news items in PostgreSQL database
type PostgresCache struct {
	db       *sql.DB
	ttlHours int
}

// FailedItem represents a news item that failed to send
type FailedItem struct {
	ID          int
	Title       string
	Link        string
	ImageURL    string
	MessageText string
	ErrorMsg    string
	Attempts    int
}

// NewPostgresCache creates a new PostgreSQL cache instance
func NewPostgresCache(connectionString string, ttlHours int) (*PostgresCache, error) {
	// Ensure driver DSN configures session timezone to UTC for all pooled connections
	if !strings.Contains(connectionString, "timezone=") {
		if strings.Contains(connectionString, "?") {
			connectionString += "&timezone=UTC"
		} else if strings.HasPrefix(connectionString, "postgres://") || strings.HasPrefix(connectionString, "postgresql://") {
			connectionString += "?timezone=UTC"
		} else {
			connectionString += " timezone=UTC"
		}
	}

	db, err := sql.Open("postgres", connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %v", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %v", err)
	}

	// Set connection pool limits for Neon free tier (max 5–10 connections)
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	cache := &PostgresCache{
		db:       db,
		ttlHours: ttlHours,
	}

	// Initialize schema
	if err := cache.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %v", err)
	}

	logger.Info("PostgreSQL cache connected successfully")
	return cache, nil
}

// Ping checks database connection
func (pc *PostgresCache) Ping() error {
	return pc.db.Ping()
}

// initSchema creates the necessary tables if they don't exist
func (pc *PostgresCache) initSchema() error {
	// Step 1: Create base table without content_hash (for compatibility with existing DBs)
	baseSchema := `
	CREATE TABLE IF NOT EXISTS sent_news (
		id SERIAL PRIMARY KEY,
		hash VARCHAR(64) UNIQUE NOT NULL,
		title TEXT NOT NULL,
		link TEXT NOT NULL,
		category VARCHAR(50),
		source VARCHAR(100),
		reserved_at TIMESTAMP,
		sent_at TIMESTAMP NOT NULL DEFAULT NOW(),
		created_at TIMESTAMP NOT NULL DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_sent_news_hash ON sent_news(hash);
	CREATE INDEX IF NOT EXISTS idx_sent_news_sent_at ON sent_news(sent_at);
	CREATE INDEX IF NOT EXISTS idx_sent_news_link ON sent_news(link);
	`

	_, err := pc.db.Exec(baseSchema)
	if err != nil {
		return fmt.Errorf("failed to create base schema: %v", err)
	}

	// Step 2: Migration - Add content_hash column if it doesn't exist
	migration := `
	DO $$ 
	BEGIN 
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
			WHERE table_name = 'sent_news' AND column_name = 'content_hash') THEN
			ALTER TABLE sent_news ADD COLUMN content_hash VARCHAR(64);
		END IF;
	END $$;
	`

	_, err = pc.db.Exec(migration)
	if err != nil {
		return fmt.Errorf("failed to run migration: %v", err)
	}

	// Step 3: Create index on content_hash (column now definitely exists)
	indexSchema := `
	CREATE INDEX IF NOT EXISTS idx_sent_news_content_hash ON sent_news(content_hash);
	`

	_, err = pc.db.Exec(indexSchema)
	if err != nil {
		return fmt.Errorf("failed to create content_hash index: %v", err)
	}

	// Step 4: Migration — add source_url and supabase_synced columns to sent_news.
	// source_url is the canonical dedup key (replaces the Supabase REST duplicate check).
	// supabase_synced tracks whether this record has been successfully pushed to Supabase.
	migration2 := `
	DO $$
	BEGIN
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'sent_news' AND column_name = 'source_url') THEN
			ALTER TABLE sent_news ADD COLUMN source_url TEXT;
		END IF;
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'sent_news' AND column_name = 'supabase_synced') THEN
			ALTER TABLE sent_news ADD COLUMN supabase_synced BOOLEAN NOT NULL DEFAULT FALSE;
		END IF;
	END $$;
	`
	if _, err = pc.db.Exec(migration2); err != nil {
		return fmt.Errorf("failed to run migration2: %v", err)
	}

	// Step 4b: Index on source_url for fast dedup lookup.
	if _, err = pc.db.Exec(`CREATE INDEX IF NOT EXISTS idx_sent_news_source_url ON sent_news(source_url);`); err != nil {
		return fmt.Errorf("failed to create source_url index: %v", err)
	}
	// Index on supabase_synced to efficiently query pending sync rows.
	if _, err = pc.db.Exec(`CREATE INDEX IF NOT EXISTS idx_sent_news_supabase_synced ON sent_news(supabase_synced) WHERE supabase_synced = FALSE;`); err != nil {
		return fmt.Errorf("failed to create supabase_synced index: %v", err)
	}

	// Step 4c: Migration — add title_norm column for near-duplicate detection.
	// Stores first 5 significant words of title (lowercase, digits stripped).
	// Catches same story published by different sources (e.g. DSB delay from DR + TV Midtvest).
	migration3 := `
	DO $$
	BEGIN
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'sent_news' AND column_name = 'title_norm') THEN
			ALTER TABLE sent_news ADD COLUMN title_norm TEXT;
		END IF;
	END $$;
	`
	if _, err = pc.db.Exec(migration3); err != nil {
		return fmt.Errorf("failed to run migration3 (title_norm): %v", err)
	}
	if _, err = pc.db.Exec(`CREATE INDEX IF NOT EXISTS idx_sent_news_title_norm ON sent_news(title_norm);`); err != nil {
		return fmt.Errorf("failed to create title_norm index: %v", err)
	}

	// Step 4d: Migration — add story_cluster_key, title_ukrainian, embedding for two-tier semantic dedup.
	migration4 := `
	DO $$
	BEGIN
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'sent_news' AND column_name = 'story_cluster_key') THEN
			ALTER TABLE sent_news ADD COLUMN story_cluster_key VARCHAR(150);
		END IF;
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'sent_news' AND column_name = 'title_ukrainian') THEN
			ALTER TABLE sent_news ADD COLUMN title_ukrainian TEXT;
		END IF;
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'sent_news' AND column_name = 'embedding') THEN
			ALTER TABLE sent_news ADD COLUMN embedding JSONB;
		END IF;
	END $$;
	`
	if _, err = pc.db.Exec(migration4); err != nil {
		return fmt.Errorf("failed to run migration4 (semantic dedup columns): %v", err)
	}
	if _, err = pc.db.Exec(`CREATE INDEX IF NOT EXISTS idx_sent_news_story_cluster_key ON sent_news(story_cluster_key);`); err != nil {
		return fmt.Errorf("failed to create story_cluster_key index: %v", err)
	}

	// Step 4e: Migration - Add reserved_at column for publish lock leasing
	migrationReservedAt := `
	DO $$ 
	BEGIN 
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
			WHERE table_name = 'sent_news' AND column_name = 'reserved_at') THEN
			ALTER TABLE sent_news ADD COLUMN reserved_at TIMESTAMP;
		END IF;
	END $$;
	CREATE INDEX IF NOT EXISTS idx_sent_news_reserved_at ON sent_news(reserved_at) WHERE content_hash IS NULL;
	`
	if _, err = pc.db.Exec(migrationReservedAt); err != nil {
		return fmt.Errorf("failed to run reserved_at migration: %v", err)
	}

	// Step 5: Create supabase_sync_queue — stores full news payload for rows not yet synced.
	// Kept separate from sent_news to avoid bloating the dedup table with large JSON blobs.
	syncQueueSchema := `
	CREATE TABLE IF NOT EXISTS supabase_sync_queue (
		id SERIAL PRIMARY KEY,
		sent_news_hash VARCHAR(64) NOT NULL REFERENCES sent_news(hash) ON DELETE CASCADE,
		payload JSONB NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0,
		last_error TEXT,
		created_at TIMESTAMP NOT NULL DEFAULT NOW(),
		last_attempt_at TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_sync_queue_hash ON supabase_sync_queue(sent_news_hash);
	CREATE INDEX IF NOT EXISTS idx_sync_queue_attempts ON supabase_sync_queue(attempts);
	`
	if _, err = pc.db.Exec(syncQueueSchema); err != nil {
		return fmt.Errorf("failed to create supabase_sync_queue: %v", err)
	}

	// Step 6: Create additional tables
	additionalSchema := `
	-- Table for recently used fun facts (anti-repeat in Telegram posts)
	CREATE TABLE IF NOT EXISTS fun_fact_cache (
		id SERIAL PRIMARY KEY,
		fact_hash VARCHAR(64) UNIQUE NOT NULL,
		fact_text TEXT NOT NULL,
		used_at TIMESTAMP NOT NULL DEFAULT NOW()
	);
	CREATE INDEX IF NOT EXISTS idx_fun_fact_cache_used_at ON fun_fact_cache(used_at);

	-- Table for failed news items (Dead Letter Queue)
	CREATE TABLE IF NOT EXISTS failed_news (
		id SERIAL PRIMARY KEY,
		title TEXT NOT NULL,
		link TEXT NOT NULL,
		image_url TEXT,
		message_text TEXT,
		error_msg TEXT,
		attempts INTEGER DEFAULT 1,
		created_at TIMESTAMP NOT NULL DEFAULT NOW(),
		last_attempt_at TIMESTAMP NOT NULL DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_failed_news_attempts ON failed_news(attempts);
	CREATE INDEX IF NOT EXISTS idx_failed_news_created_at ON failed_news(created_at);

	-- Table for delayed high-impact posts
	CREATE TABLE IF NOT EXISTS delayed_posts (
		id SERIAL PRIMARY KEY,
		hash VARCHAR(64) UNIQUE NOT NULL,
		title TEXT NOT NULL,
		link TEXT NOT NULL,
		news_json TEXT NOT NULL,
		publish_after TIMESTAMP NOT NULL,
		status VARCHAR(20) NOT NULL DEFAULT 'pending',
		claimed_at TIMESTAMP,
		created_at TIMESTAMP NOT NULL DEFAULT NOW()
	);
	CREATE INDEX IF NOT EXISTS idx_delayed_posts_ready ON delayed_posts(publish_after, status);

	DO $$
	BEGIN
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'delayed_posts' AND column_name = 'claimed_at') THEN
			ALTER TABLE delayed_posts ADD COLUMN claimed_at TIMESTAMP;
		END IF;
	END $$;
	`

	_, err = pc.db.Exec(additionalSchema)
	if err != nil {
		return fmt.Errorf("failed to create additional tables: %v", err)
	}

	logger.Info("Database schema initialized")
	return nil
}

// IsAlreadySent checks if news was already sent (or currently actively reserved by another runner)
func (pc *PostgresCache) IsAlreadySent(hash string) bool {
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)

	var count int
	query := `
		SELECT COUNT(*) FROM sent_news 
		WHERE hash = $1 
		  AND (
		    -- Case 1: Post was finalized or legacy row, and sent_at is within TTL
		    (sent_at > $2 AND (content_hash IS NOT NULL OR reserved_at IS NULL))
		    OR
		    -- Case 2: Fresh active reservation (< 15 min old) held by another worker
		    (content_hash IS NULL AND reserved_at IS NOT NULL AND reserved_at > (NOW() AT TIME ZONE 'UTC') - INTERVAL '15 minutes')
		  );
	`
	err := pc.db.QueryRow(query, hash, cutoffTime).Scan(&count)

	if err != nil {
		logger.Error("DB error checking duplicate by hash, assuming already sent to prevent duplicate spam", "hash", hash, "error", err)
		return true
	}

	return count > 0
}


// IsContentDuplicate checks if content with similar hash already exists
// Uses SimHash-like approach: generates hash from normalized content words
// Two articles about the same event will have very similar content regardless of title
func (pc *PostgresCache) IsContentDuplicate(content string) (bool, string) {
	if len(content) < 100 {
		return false, ""
	}

	contentHash := generateContentHash(content)
	legacyHash := generateLegacyContentHash(content)
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)

	var existingTitle string
	query := `SELECT title FROM sent_news WHERE content_hash IN ($1, $2) AND sent_at > $3 LIMIT 1`
	err := pc.db.QueryRow(query, contentHash, legacyHash, cutoffTime).Scan(&existingTitle)

	switch {
	case err == nil:
		logger.Info("Found duplicate content hash", "existing_title", existingTitle)
		return true, existingTitle
	case errors.Is(err, sql.ErrNoRows):
		// No match — normal "not duplicate" outcome
		return false, ""
	default:
		// DB failure — fail-open: allow publishing rather than blocking the feed.
		// hash/source_url gates above are fail-closed and keep this from spamming.
		logger.Warn("DB error checking content duplicate, assuming NOT duplicate (fail-open)", "error", err)
		return false, ""
	}
}

// generateContentHash creates a stronger, deterministic hash for content deduplication.
// It combines significant numbers and normalized text and hashes with SHA-256 (128-bit truncated hex).
func generateContentHash(content string) string {
	normalized := normalizeContentForHash(content)
	numbers := extractSignificantNumbers(normalized)
	sort.Strings(numbers)

	signature := "t:" + normalized
	if len(numbers) > 0 {
		signature = "n:" + strings.Join(numbers, ",") + "|" + signature
	}

	sum := sha256.Sum256([]byte(signature))
	return hex.EncodeToString(sum[:16])
}

// generateLegacyContentHash keeps the previous FNV-based logic for backward-compatible lookups.
func generateLegacyContentHash(content string) string {
	normalized := strings.ToLower(content)
	numbers := extractSignificantNumbers(normalized)
	if len(numbers) == 0 {
		var textOnly strings.Builder
		for _, r := range normalized {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				textOnly.WriteRune(r)
			}
		}
		text := textOnly.String()
		if len(text) > 200 {
			text = text[:200]
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(text))
		return fmt.Sprintf("%016x", h.Sum64())
	}
	sort.Strings(numbers)
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.Join(numbers, ",")))
	return fmt.Sprintf("%016x", h.Sum64())
}

// normalizeTitleForDedup returns the first 5 significant words of a title,
// lowercased and stripped of punctuation/digits.
// "Forsinkede tog kostede rekordbeløb" → "forsinkede tog kostede rekordbeløb"
// "Forsinkede tog har kostet DSB rekordbeløb i kompensation" → "forsinkede tog har kostet dsb"
// Overlap ≥ 3 words → near-duplicate.
func normalizeTitleForDedup(title string) string {
	title = strings.ToLower(title)
	// Remove punctuation and digits, keep letters and spaces
	var buf strings.Builder
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsSpace(r) {
			buf.WriteRune(r)
		} else {
			buf.WriteRune(' ')
		}
	}
	// Split into words, skip stop-words and very short tokens
	stopWords := map[string]bool{
		// Danish
		"i": true, "er": true, "og": true, "en": true, "et": true,
		"af": true, "til": true, "med": true, "på": true, "for": true,
		"at": true, "de": true, "den": true, "det": true, "har": true,
		"fra": true, "om": true, "som": true, "efter": true, "ikke": true,
		// English
		"the": true, "a": true, "an": true, "in": true, "of": true,
		"to": true, "and": true, "is": true, "are": true,
	}
	var words []string
	for _, w := range strings.Fields(buf.String()) {
		if len([]rune(w)) < 3 {
			continue
		}
		if stopWords[w] {
			continue
		}
		words = append(words, w)
		if len(words) == 5 {
			break
		}
	}
	return strings.Join(words, " ")
}

// IsTitleNearDuplicate checks if a title is semantically similar to a recently sent title.
// Uses the first 5 significant words overlap — catches same story from different sources.
func (pc *PostgresCache) IsTitleNearDuplicate(title string) (bool, string) {
	norm := normalizeTitleForDedup(title)
	if norm == "" {
		return false, ""
	}
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)

	// Exact match on normalized key
	var existingTitle string
	err := pc.db.QueryRow(
		`SELECT title FROM sent_news WHERE title_norm = $1 AND sent_at > $2 LIMIT 1`,
		norm, cutoffTime,
	).Scan(&existingTitle)
	switch {
	case err == nil:
		return true, existingTitle
	case errors.Is(err, sql.ErrNoRows):
		// No match — normal "not duplicate" outcome
		return false, ""
	default:
		// DB failure — fail-open: allow publishing rather than blocking the feed.
		// hash/source_url gates above are fail-closed and keep this from spamming.
		logger.Warn("DB error checking near-duplicate title, assuming NOT duplicate (fail-open)", "title", title, "error", err)
		return false, ""
	}
}

func extractSignificantNumbers(normalized string) []string {
	var numbers []string
	var currentNum strings.Builder
	prevWasDigit := false

	for _, r := range normalized {
		if r >= '0' && r <= '9' {
			currentNum.WriteRune(r)
			prevWasDigit = true
		} else if (r == '.' || r == ',') && prevWasDigit {
			// keep separators inside number (e.g. 55.000 => 55000)
		} else {
			if currentNum.Len() > 0 {
				num := currentNum.String()
				if len(num) >= 4 {
					numbers = append(numbers, num)
				}
				currentNum.Reset()
			}
			prevWasDigit = false
		}
	}
	if currentNum.Len() > 0 {
		num := currentNum.String()
		if len(num) >= 4 {
			numbers = append(numbers, num)
		}
	}
	return numbers
}

func normalizeContentForHash(content string) string {
	lower := strings.ToLower(stripNumericSeparators(content))
	var b strings.Builder
	b.Grow(len(lower))
	lastSpace := false
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
}

// stripNumericSeparators removes dots/commas only when they are inside a number.
func stripNumericSeparators(content string) string {
	runes := []rune(content)
	var b strings.Builder
	b.Grow(len(content))
	for i, r := range runes {
		if (r == '.' || r == ',') && i > 0 && i+1 < len(runes) {
			if unicode.IsDigit(runes[i-1]) && unicode.IsDigit(runes[i+1]) {
				continue
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// MarkAsSent marks news as sent with transaction to prevent race conditions
func (pc *PostgresCache) MarkAsSent(hash, title, link, category, source string) error {
	// Use INSERT ON CONFLICT to handle race conditions
	query := `
		INSERT INTO sent_news (hash, title, link, source_url, category, source, sent_at, reserved_at)
		VALUES ($1, $2, $3, $3, $4, $5, NOW(), NULL)
		ON CONFLICT (hash) DO UPDATE SET
			sent_at = NOW(),
			category = EXCLUDED.category,
			source = EXCLUDED.source,
			reserved_at = NULL
	`

	_, err := pc.db.Exec(query, hash, title, link, category, source)
	if err != nil {
		return fmt.Errorf("failed to mark as sent: %v", err)
	}

	return nil
}

// MarkAsSentWithContent marks news as sent and stores content hash + source_url for dedup.
// supabase_synced is set to FALSE — caller must call MarkSupabaseSynced after successful push.
func (pc *PostgresCache) MarkAsSentWithContent(hash, title, link, content, category, source string) error {
	return pc.MarkAsSentWithSemanticData(hash, title, link, content, category, source, "", "", nil)
}

// MarkAsSentWithSemanticData marks news as sent with semantic metadata (Ukrainian title, cluster key, embedding).
func (pc *PostgresCache) MarkAsSentWithSemanticData(hash, title, link, content, category, source, titleUA, clusterKey string, emb []float32) error {
	contentHash := ""
	if len(content) >= 100 {
		contentHash = generateContentHash(content)
	} else if strings.TrimSpace(content) != "" {
		contentHash = generateContentHash(content)
	} else {
		contentHash = "finalized_" + hash
	}
	titleNorm := normalizeTitleForDedup(title)

	var embeddingJSON interface{} = nil
	if len(emb) > 0 {
		if bytes, err := json.Marshal(emb); err == nil {
			embeddingJSON = string(bytes)
		}
	}

	query := `
		INSERT INTO sent_news (hash, title, link, source_url, content_hash, title_norm, category, source, sent_at, supabase_synced, title_ukrainian, story_cluster_key, embedding, reserved_at)
		VALUES ($1, $2, $3, $3, $4, $5, $6, $7, NOW(), FALSE, $8, $9, $10, NULL)
		ON CONFLICT (hash) DO UPDATE SET
			sent_at = NOW(),
			content_hash = EXCLUDED.content_hash,
			category = EXCLUDED.category,
			source = EXCLUDED.source,
			source_url = EXCLUDED.source_url,
			title_norm = EXCLUDED.title_norm,
			title_ukrainian = COALESCE(EXCLUDED.title_ukrainian, sent_news.title_ukrainian),
			story_cluster_key = COALESCE(EXCLUDED.story_cluster_key, sent_news.story_cluster_key),
			embedding = COALESCE(EXCLUDED.embedding, sent_news.embedding),
			reserved_at = NULL
	`

	_, err := pc.db.Exec(query, hash, title, link, contentHash, titleNorm, category, source, titleUA, clusterKey, embeddingJSON)
	if err != nil {
		return fmt.Errorf("failed to mark as sent with semantic data: %v", err)
	}

	return nil
}

// SentStoryRecord represents a published story with semantic metadata for deduplication.
type SentStoryRecord struct {
	Title           string
	TitleUkrainian  string
	StoryClusterKey string
	Embedding       []float32
	SentAt          time.Time
}

// GetRecentStories returns sent news items from the last lookback duration for semantic duplicate checking.
func (pc *PostgresCache) GetRecentStories(lookback time.Duration) ([]SentStoryRecord, error) {
	if lookback <= 0 {
		lookback = 7 * 24 * time.Hour
	}
	cutoffTime := time.Now().Add(-lookback)

	query := `
		SELECT title, COALESCE(title_ukrainian, ''), COALESCE(story_cluster_key, ''), embedding, sent_at
		FROM sent_news
		WHERE sent_at > $1
		ORDER BY sent_at DESC
	`

	rows, err := pc.db.Query(query, cutoffTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent stories: %w", err)
	}
	defer rows.Close()

	var records []SentStoryRecord
	for rows.Next() {
		var rec SentStoryRecord
		var embeddingRaw []byte
		if err := rows.Scan(&rec.Title, &rec.TitleUkrainian, &rec.StoryClusterKey, &embeddingRaw, &rec.SentAt); err != nil {
			logger.Warn("Failed to scan sent story record", "error", err)
			continue
		}
		if len(embeddingRaw) > 0 {
			var vec []float32
			if err := json.Unmarshal(embeddingRaw, &vec); err == nil {
				rec.Embedding = vec
			}
		}
		records = append(records, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating recent stories: %w", err)
	}

	return records, nil
}

// SemanticCheckResult contains details of two-tier deduplication check.
type SemanticCheckResult struct {
	IsDuplicate       bool    `json:"is_duplicate"`
	WouldReject       bool    `json:"would_reject"`
	Trigger           string  `json:"trigger"` // "tier1_cluster_key", "tier2_embedding", "tier1_and_tier2", "none"
	MatchedTitle      string  `json:"matched_title"`
	ClusterSimilarity float64 `json:"cluster_similarity"`
	CosineSimilarity  float64 `json:"cosine_similarity"`
	ShadowMode        bool    `json:"shadow_mode"`
}

// CheckSemanticDuplicate performs two-tier deduplication check against stories published in the lookback window.
// Detection logic uses LOGICAL OR: triggers if Tier 1 (Cluster Key overlap >= keyThreshold) OR Tier 2 (Cosine Similarity >= cosineThreshold).
// In Shadow Mode, Tier 2 logs similarity and would_reject=true without rejecting publication, allowing observation of live numbers.
func (pc *PostgresCache) CheckSemanticDuplicate(clusterKey string, candidateEmbedding []float32, titleUA string, lookback time.Duration, keyThreshold, cosineThreshold float64, shadowMode bool) (SemanticCheckResult, error) {
	result := SemanticCheckResult{
		ShadowMode: shadowMode,
		Trigger:    "none",
	}

	recentStories, err := pc.GetRecentStories(lookback)
	if err != nil {
		logger.Warn("Failed to get recent stories for semantic dedup, degrading gracefully", "error", err)
		return result, err
	}

	for _, rec := range recentStories {
		// Tier 1: Cluster Key Jaccard Token Check
		if clusterKey != "" && rec.StoryClusterKey != "" {
			simKey := embedding.ClusterKeySimilarity(clusterKey, rec.StoryClusterKey)
			if simKey > result.ClusterSimilarity {
				result.ClusterSimilarity = simKey
				if simKey >= keyThreshold && result.MatchedTitle == "" {
					result.MatchedTitle = rec.TitleUkrainian
					if result.MatchedTitle == "" {
						result.MatchedTitle = rec.Title
					}
				}
			}
		}

		// Tier 2: Embedding Cosine Similarity Check
		if len(candidateEmbedding) > 0 && len(rec.Embedding) > 0 {
			simCos := embedding.CosineSimilarity(candidateEmbedding, rec.Embedding)
			if simCos > result.CosineSimilarity {
				result.CosineSimilarity = simCos
				if simCos >= cosineThreshold {
					matched := rec.TitleUkrainian
					if matched == "" {
						matched = rec.Title
					}
					result.MatchedTitle = matched
				}
			}
		}
	}

	tier1Fired := result.ClusterSimilarity >= keyThreshold && keyThreshold > 0
	tier2Fired := result.CosineSimilarity >= cosineThreshold && cosineThreshold > 0

	// LOGICAL OR: triggers if either Tier 1 or Tier 2 matches
	result.WouldReject = tier1Fired || tier2Fired

	switch {
	case tier1Fired && tier2Fired:
		result.Trigger = "tier1_and_tier2"
	case tier1Fired:
		result.Trigger = "tier1_cluster_key"
	case tier2Fired:
		result.Trigger = "tier2_embedding"
	default:
		result.Trigger = "none"
	}

	// In Shadow Mode, Tier 1 is enforced, while Tier 2 only observes and logs
	if shadowMode {
		result.IsDuplicate = tier1Fired
	} else {
		result.IsDuplicate = result.WouldReject
	}

	return result, nil
}

// IsSourceURLSent checks whether a news item with this source_url was already sent.
// Replaces the expensive Supabase REST duplicate check — this is a direct SQL lookup.
func (pc *PostgresCache) IsSourceURLSent(sourceURL string) bool {
	if sourceURL == "" {
		return false
	}
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)
	var count int
	err := pc.db.QueryRow(
		`SELECT COUNT(*) FROM sent_news WHERE source_url = $1 AND sent_at > $2`,
		sourceURL, cutoffTime,
	).Scan(&count)
	if err != nil {
		logger.Error("DB error checking duplicate by source_url, assuming already sent to prevent duplicate spam", "source_url", sourceURL, "error", err)
		return true
	}
	return count > 0
}

// MarkSupabaseSynced marks a sent_news row as successfully pushed to Supabase.
func (pc *PostgresCache) MarkSupabaseSynced(hash string) error {
	_, err := pc.db.Exec(
		`UPDATE sent_news SET supabase_synced = TRUE WHERE hash = $1`,
		hash,
	)
	return err
}

// SyncQueueItem is a pending Supabase push stored in Neon.
type SyncQueueItem struct {
	ID           int
	SentNewsHash string
	Payload      []byte // raw JSON matching NewsArchive
	Attempts     int
}

// EnqueueSupabaseSync stores the full news payload for later sync to Supabase.
// Called immediately after a successful Telegram send when Supabase is unavailable or slow.
func (pc *PostgresCache) EnqueueSupabaseSync(hash string, payload []byte) error {
	_, err := pc.db.Exec(`
		INSERT INTO supabase_sync_queue (sent_news_hash, payload, attempts, created_at)
		VALUES ($1, $2, 0, NOW())
		ON CONFLICT DO NOTHING
	`, hash, payload)
	return err
}

// GetPendingSupabaseSync returns up to limit items not yet pushed to Supabase (max 5 attempts).
func (pc *PostgresCache) GetPendingSupabaseSync(limit int) ([]SyncQueueItem, error) {
	rows, err := pc.db.Query(`
		SELECT id, sent_news_hash, payload, attempts
		FROM supabase_sync_queue
		WHERE attempts < 5
		ORDER BY created_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []SyncQueueItem
	for rows.Next() {
		var it SyncQueueItem
		if err := rows.Scan(&it.ID, &it.SentNewsHash, &it.Payload, &it.Attempts); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// DeleteSyncQueueItem removes a successfully synced item from the queue.
func (pc *PostgresCache) DeleteSyncQueueItem(id int) error {
	_, err := pc.db.Exec(`DELETE FROM supabase_sync_queue WHERE id = $1`, id)
	return err
}

// IncrementSyncQueueAttempts bumps attempt counter + records error on failure.
func (pc *PostgresCache) IncrementSyncQueueAttempts(id int, errMsg string) error {
	_, err := pc.db.Exec(`
		UPDATE supabase_sync_queue
		SET attempts = attempts + 1, last_attempt_at = NOW(), last_error = $2
		WHERE id = $1
	`, id, errMsg)
	return err
}

// Cleanup removes expired items from database
func (pc *PostgresCache) Cleanup() error {
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)

	// Clean sent_news
	query := `DELETE FROM sent_news WHERE sent_at < $1`
	result, err := pc.db.Exec(query, cutoffTime)
	if err != nil {
		return fmt.Errorf("failed to cleanup sent_news: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows > 0 {
		logger.Info("Cleaned old sent_news records", "rows", rows)
	}

	// Clean old failed_news (DLQ) — items past TTL are never retried again
	queryFailed := `DELETE FROM failed_news WHERE last_attempt_at < $1`
	resultFailed, err := pc.db.Exec(queryFailed, cutoffTime)
	if err != nil {
		logger.Warn("Failed to cleanup failed_news", "error", err)
	} else {
		rowsFailed, _ := resultFailed.RowsAffected()
		if rowsFailed > 0 {
			logger.Info("Cleaned old failed_news records", "rows", rowsFailed)
		}
	}

	// Clean finished delayed_posts ('sent'/'failed'); 'pending' are kept until delivered
	queryDelayed := `DELETE FROM delayed_posts WHERE status <> 'pending' AND created_at < $1`
	resultDelayed, err := pc.db.Exec(queryDelayed, cutoffTime)
	if err != nil {
		logger.Warn("Failed to cleanup delayed_posts", "error", err)
	} else {
		rowsDelayed, _ := resultDelayed.RowsAffected()
		if rowsDelayed > 0 {
			logger.Info("Cleaned old delayed_posts records", "rows", rowsDelayed)
		}
	}

	queryFunFacts := `DELETE FROM fun_fact_cache WHERE used_at < $1`
	resultFunFacts, err := pc.db.Exec(queryFunFacts, cutoffTime)
	if err != nil {
		logger.Warn("Failed to cleanup fun_fact_cache", "error", err)
	} else {
		rowsFunFacts, _ := resultFunFacts.RowsAffected()
		if rowsFunFacts > 0 {
			logger.Info("Cleaned old fun_fact_cache records", "rows", rowsFunFacts)
		}
	}

	return nil
}

// Close closes the database connection
func (pc *PostgresCache) Close() error {
	if pc.db != nil {
		return pc.db.Close()
	}
	return nil
}

// GenerateNewsHash creates a stable hash for news item (same as FileCache for consistency)
func (pc *PostgresCache) GenerateNewsHash(title, link string) string {
	// Use the same logic as FileCache
	fc := &FileCache{}
	return fc.GenerateNewsHash(title, link)
}

// SaveFailedNews saves a failed news item to the DLQ
func (pc *PostgresCache) SaveFailedNews(title, link, imageURL, messageText, errorMsg string) error {
	query := `
		INSERT INTO failed_news (title, link, image_url, message_text, error_msg, attempts, created_at, last_attempt_at)
		VALUES ($1, $2, $3, $4, $5, 1, NOW(), NOW())
	`
	_, err := pc.db.Exec(query, title, link, imageURL, messageText, errorMsg)
	return err
}

// GetFailedNews retrieves failed news items for retry
func (pc *PostgresCache) GetFailedNews(limit int) ([]FailedItem, error) {
	query := `
		SELECT id, title, link, image_url, message_text, error_msg, attempts 
		FROM failed_news 
		WHERE attempts < 5 
		ORDER BY created_at ASC 
		LIMIT $1
	`
	rows, err := pc.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []FailedItem
	for rows.Next() {
		var i FailedItem
		if err := rows.Scan(&i.ID, &i.Title, &i.Link, &i.ImageURL, &i.MessageText, &i.ErrorMsg, &i.Attempts); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating failed news: %w", err)
	}
	return items, nil
}

// DeleteFailedNews removes a failed item after successful send
func (pc *PostgresCache) DeleteFailedNews(id int) error {
	_, err := pc.db.Exec(`DELETE FROM failed_news WHERE id = $1`, id)
	return err
}

// IncrementFailedAttempts updates the attempt counter
func (pc *PostgresCache) IncrementFailedAttempts(id int, errorMsg string) error {
	_, err := pc.db.Exec(`UPDATE failed_news SET attempts = attempts + 1, last_attempt_at = NOW(), error_msg = $2 WHERE id = $1`, id, errorMsg)
	return err
}

// normalizeFunFactForHash normalizes fun fact text for consistent hashing.
func normalizeFunFactForHash(fact string) string {
	fact = strings.ToLower(strings.TrimSpace(fact))
	if fact == "" {
		return ""
	}

	// Remove leading emoji/symbol prefix and collapse whitespace.
	fact = strings.TrimLeftFunc(fact, func(r rune) bool {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
		switch r {
		case ',', '.', ':', ';', '!', '?', '-', '—', '–':
			return true
		default:
			return unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r)
		}
	})
	return strings.Join(strings.Fields(fact), " ")
}

// generateFunFactHash creates a hash for the fun fact text.
func generateFunFactHash(fact string) string {
	normalized := normalizeFunFactForHash(fact)
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:16])
}

// IsFunFactRecentlyUsed checks whether a similar fact was posted within TTL window.
func (pc *PostgresCache) IsFunFactRecentlyUsed(funFact string) bool {
	hash := generateFunFactHash(funFact)
	if hash == "" {
		return false
	}
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)
	var count int
	err := pc.db.QueryRow(
		`SELECT COUNT(*) FROM fun_fact_cache WHERE fact_hash = $1 AND used_at > $2`,
		hash, cutoffTime,
	).Scan(&count)
	if err != nil {
		logger.Warn("Error checking fun_fact repeat", "error", err)
		return false
	}
	return count > 0
}

// MarkFunFactUsed stores/refreshes usage time for a fact hash.
func (pc *PostgresCache) MarkFunFactUsed(funFact string) error {
	hash := generateFunFactHash(funFact)
	if hash == "" {
		return nil
	}
	_, err := pc.db.Exec(`
		INSERT INTO fun_fact_cache (fact_hash, fact_text, used_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (fact_hash) DO UPDATE SET
			fact_text = EXCLUDED.fact_text,
			used_at = NOW()
	`, hash, strings.TrimSpace(funFact))
	return err
}

// DelayedPost represents a high-impact news item queued for deferred publication.
type DelayedPost struct {
	ID           int        `json:"id"`
	Hash         string     `json:"hash"`
	Title        string     `json:"title"`
	Link         string     `json:"link"`
	NewsJSON     string     `json:"news_json"`
	PublishAfter time.Time  `json:"publish_after"`
	Status       string     `json:"status"` // 'pending', 'processing', 'sent', 'failed'
	ClaimedAt    *time.Time `json:"claimed_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// EnqueueDelayedPost schedules a high-impact secondary post for publication after delay.
func (pc *PostgresCache) EnqueueDelayedPost(hash, title, link, newsJSON string, delay time.Duration) error {
	query := `
		INSERT INTO delayed_posts (hash, title, link, news_json, publish_after, status, created_at)
		VALUES ($1, $2, $3, $4, NOW() + $5 * INTERVAL '1 second', 'pending', NOW())
		ON CONFLICT (hash) DO UPDATE 
		SET publish_after = EXCLUDED.publish_after, status = 'pending'
		WHERE delayed_posts.status != 'sent';
	`
	seconds := int64(delay.Seconds())
	_, err := pc.db.Exec(query, hash, title, link, newsJSON, seconds)
	if err != nil {
		return fmt.Errorf("failed to enqueue delayed post: %w", err)
	}
	logger.Info("Postgres: enqueued delayed post", "title", title, "hash", hash, "delay", delay)
	return nil
}

// GetReadyDelayedPosts retrieves ready delayed posts whose publish_after <= NOW() and claims them atomically.
func (pc *PostgresCache) GetReadyDelayedPosts(ctx context.Context) ([]DelayedPost, error) {
	query := `
		WITH ready AS (
			SELECT id
			FROM delayed_posts
			WHERE publish_after <= (NOW() AT TIME ZONE 'UTC')
			  AND (status = 'pending' OR (status = 'processing' AND COALESCE(claimed_at, '-infinity'::timestamp) < (NOW() AT TIME ZONE 'UTC') - INTERVAL '30 minutes'))
			ORDER BY publish_after ASC
			LIMIT 5
			FOR UPDATE SKIP LOCKED
		)
		UPDATE delayed_posts
		SET status = 'processing', claimed_at = (NOW() AT TIME ZONE 'UTC')
		FROM ready
		WHERE delayed_posts.id = ready.id
		RETURNING delayed_posts.id, delayed_posts.hash, delayed_posts.title, delayed_posts.link, delayed_posts.news_json, delayed_posts.publish_after, delayed_posts.status, delayed_posts.created_at;
	`
	rows, err := pc.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query ready delayed posts: %w", err)
	}
	defer rows.Close()

	var posts []DelayedPost
	for rows.Next() {
		var dp DelayedPost
		if err := rows.Scan(&dp.ID, &dp.Hash, &dp.Title, &dp.Link, &dp.NewsJSON, &dp.PublishAfter, &dp.Status, &dp.CreatedAt); err != nil {
			logger.Warn("failed to scan delayed post", "error", err)
			continue
		}
		posts = append(posts, dp)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating ready delayed posts: %w", err)
	}

	return posts, nil
}

// MarkDelayedPostSent marks a delayed post as sent.
func (pc *PostgresCache) MarkDelayedPostSent(id int) error {
	query := `UPDATE delayed_posts SET status = 'sent' WHERE id = $1;`
	_, err := pc.db.Exec(query, id)
	return err
}

// MarkDelayedPostFailed marks a delayed post as failed with an error message.
func (pc *PostgresCache) MarkDelayedPostFailed(id int, errMsg string) error {
	query := `UPDATE delayed_posts SET status = 'failed' WHERE id = $1;`
	_, err := pc.db.Exec(query, id)
	return err
}

// TryReservePublish atomically claims a news hash prior to publishing to Telegram.
// Returns (true, nil) if the reservation was claimed (or if a stale reservation > 15m was reclaimed),
// or (false, nil) if another runner currently holds an active reservation, already finalized the item, or it's a legacy unreserved row.
func (pc *PostgresCache) TryReservePublish(hash, title, url string) (bool, error) {
	query := `
		INSERT INTO sent_news (hash, title, link, source_url, sent_at, reserved_at)
		VALUES ($1, $2, $3, $3, (NOW() AT TIME ZONE 'UTC'), (NOW() AT TIME ZONE 'UTC'))
		ON CONFLICT (hash) DO UPDATE SET
			title = EXCLUDED.title,
			link = EXCLUDED.link,
			source_url = EXCLUDED.source_url,
			reserved_at = (NOW() AT TIME ZONE 'UTC')
		WHERE sent_news.content_hash IS NULL
		  AND sent_news.reserved_at IS NOT NULL
		  AND sent_news.reserved_at < (NOW() AT TIME ZONE 'UTC') - INTERVAL '15 minutes'
		RETURNING id;
	`
	var id int
	err := pc.db.QueryRow(query, hash, title, url).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Conflict: another runner has already finalized this item, an active reservation (< 15m) is in progress, or legacy row with reserved_at IS NULL
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}


// CancelPublishReservation releases the unfulfilled reservation if Telegram sending fails.
func (pc *PostgresCache) CancelPublishReservation(hash string) error {
	query := `DELETE FROM sent_news WHERE hash = $1 AND content_hash IS NULL;`
	_, err := pc.db.Exec(query, hash)
	return err
}


