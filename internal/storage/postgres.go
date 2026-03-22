package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/deusflow/News/internal/logger"
	_ "github.com/lib/pq"
)

// PostgresCache manages sent news items in PostgreSQL database
type PostgresCache struct {
	db       *sql.DB
	ttlHours int
}

// TranslationCacheItem represents cached AI translation
type TranslationCacheItem struct {
	ContentHash          string
	Title                string
	Content              string
	Summary              string
	DanishTranslation    string
	UkrainianTranslation string
	AIProvider           string
	CreatedAt            time.Time
	LastUsedAt           time.Time
	UseCount             int
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

// DigestNewsItem is a compact row used to build weekly digest prompts.
type DigestNewsItem struct {
	Title         string
	Category      string
	Source        string
	WhyItMatters  string
	PublishedTime time.Time
}

// NewPostgresCache creates a new PostgreSQL cache instance
func NewPostgresCache(connectionString string, ttlHours int) (*PostgresCache, error) {
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

	// Step 4d: Migration — add editorial consequence text for digest quality.
	migration4 := `
	DO $$
	BEGIN
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'sent_news' AND column_name = 'why_it_matters') THEN
			ALTER TABLE sent_news ADD COLUMN why_it_matters TEXT;
		END IF;
	END $$;
	`
	if _, err = pc.db.Exec(migration4); err != nil {
		return fmt.Errorf("failed to run migration4 (why_it_matters): %v", err)
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
	-- Table for caching AI translations (saves tokens!)
	CREATE TABLE IF NOT EXISTS translation_cache (
		id SERIAL PRIMARY KEY,
		content_hash VARCHAR(64) UNIQUE NOT NULL,
		title TEXT NOT NULL,
		content TEXT NOT NULL,
		summary TEXT,
		danish_translation TEXT,
		ukrainian_translation TEXT,
		ai_provider VARCHAR(50),
		created_at TIMESTAMP NOT NULL DEFAULT NOW(),
		last_used_at TIMESTAMP NOT NULL DEFAULT NOW(),
		use_count INTEGER DEFAULT 1
	);

	CREATE INDEX IF NOT EXISTS idx_translation_cache_hash ON translation_cache(content_hash);
	CREATE INDEX IF NOT EXISTS idx_translation_cache_created_at ON translation_cache(created_at);

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

	-- Telegram bot state (e.g. last processed update_id for getUpdates polling)
	CREATE TABLE IF NOT EXISTS telegram_bot_state (
		state_key TEXT PRIMARY KEY,
		state_value TEXT NOT NULL,
		updated_at TIMESTAMP NOT NULL DEFAULT NOW()
	);

	-- Mapping from short callback token to canonical sent_news hash.
	CREATE TABLE IF NOT EXISTS feedback_button_tokens (
		token VARCHAR(32) PRIMARY KEY,
		news_hash VARCHAR(64) NOT NULL REFERENCES sent_news(hash) ON DELETE CASCADE,
		created_at TIMESTAMP NOT NULL DEFAULT NOW()
	);
	CREATE INDEX IF NOT EXISTS idx_feedback_button_tokens_news_hash ON feedback_button_tokens(news_hash);

	-- Idempotent raw callback events (dedup by update_id).
	CREATE TABLE IF NOT EXISTS telegram_feedback_events (
		update_id BIGINT PRIMARY KEY,
		news_hash VARCHAR(64) NOT NULL REFERENCES sent_news(hash) ON DELETE CASCADE,
		user_id BIGINT NOT NULL,
		reaction SMALLINT NOT NULL CHECK (reaction IN (-1, 1)),
		created_at TIMESTAMP NOT NULL DEFAULT NOW()
	);
	CREATE INDEX IF NOT EXISTS idx_telegram_feedback_events_news_hash ON telegram_feedback_events(news_hash);

	-- Current vote per user per news (latest reaction wins).
	CREATE TABLE IF NOT EXISTS telegram_feedback_votes (
		news_hash VARCHAR(64) NOT NULL REFERENCES sent_news(hash) ON DELETE CASCADE,
		user_id BIGINT NOT NULL,
		reaction SMALLINT NOT NULL CHECK (reaction IN (-1, 1)),
		updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
		PRIMARY KEY (news_hash, user_id)
	);
	CREATE INDEX IF NOT EXISTS idx_telegram_feedback_votes_news_hash ON telegram_feedback_votes(news_hash);
	`

	_, err = pc.db.Exec(additionalSchema)
	if err != nil {
		return fmt.Errorf("failed to create additional tables: %v", err)
	}

	logger.Info("Database schema initialized")
	return nil
}

// IsAlreadySent checks if news was already sent (within TTL window)
func (pc *PostgresCache) IsAlreadySent(hash string) bool {
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)

	var count int
	query := `SELECT COUNT(*) FROM sent_news WHERE hash = $1 AND sent_at > $2`
	err := pc.db.QueryRow(query, hash, cutoffTime).Scan(&count)

	if err != nil {
		logger.Warn("Error checking duplicate by hash", "error", err)
		return false
	}

	return count > 0
}

// IsLinkAlreadySent checks if a specific link was already sent (additional safety check)
func (pc *PostgresCache) IsLinkAlreadySent(link string) bool {
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)

	var count int
	query := `SELECT COUNT(*) FROM sent_news WHERE link = $1 AND sent_at > $2`
	err := pc.db.QueryRow(query, link, cutoffTime).Scan(&count)

	if err != nil {
		logger.Warn("Error checking duplicate by link", "error", err)
		return false
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

	if err == nil {
		logger.Info("Found duplicate content hash", "existing_title", existingTitle)
		return true, existingTitle
	}
	return false, ""
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
	if err == nil {
		return true, existingTitle
	}
	return false, ""
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
		INSERT INTO sent_news (hash, title, link, source_url, category, source, sent_at)
		VALUES ($1, $2, $3, $3, $4, $5, NOW())
		ON CONFLICT (hash) DO UPDATE SET sent_at = NOW()
	`

	_, err := pc.db.Exec(query, hash, title, link, category, source)
	if err != nil {
		return fmt.Errorf("failed to mark as sent: %v", err)
	}

	return nil
}

// MarkAsSentWithContent marks news as sent and stores content hash + source_url for dedup.
// supabase_synced is set to FALSE — caller must call MarkSupabaseSynced after successful push.
func (pc *PostgresCache) MarkAsSentWithContent(hash, title, link, content, whyItMatters, category, source string) error {
	contentHash := ""
	if len(content) >= 100 {
		contentHash = generateContentHash(content)
	}
	titleNorm := normalizeTitleForDedup(title)
	whyItMatters = strings.TrimSpace(whyItMatters)

	query := `
		INSERT INTO sent_news (hash, title, link, source_url, content_hash, title_norm, category, source, why_it_matters, sent_at, supabase_synced)
		VALUES ($1, $2, $3, $3, $4, $5, $6, $7, $8, NOW(), FALSE)
		ON CONFLICT (hash) DO UPDATE SET
			sent_at = NOW(),
			content_hash = EXCLUDED.content_hash,
			source_url = EXCLUDED.source_url,
			title_norm = EXCLUDED.title_norm,
			why_it_matters = EXCLUDED.why_it_matters
	`

	_, err := pc.db.Exec(query, hash, title, link, contentHash, titleNorm, category, source, whyItMatters)
	if err != nil {
		return fmt.Errorf("failed to mark as sent: %v", err)
	}

	return nil
}

// GetSentNewsInRange returns sent_news rows for digest generation.
func (pc *PostgresCache) GetSentNewsInRange(since, until time.Time, limit int) ([]DigestNewsItem, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := pc.db.Query(`
		SELECT title, category, source, COALESCE(why_it_matters, ''), sent_at
		FROM sent_news
		WHERE sent_at >= $1 AND sent_at < $2
		ORDER BY sent_at DESC
		LIMIT $3
	`, since, until, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]DigestNewsItem, 0, limit)
	for rows.Next() {
		var item DigestNewsItem
		if err := rows.Scan(&item.Title, &item.Category, &item.Source, &item.WhyItMatters, &item.PublishedTime); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	return items, rows.Err()
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
		logger.Warn("Error checking duplicate by source_url", "error", err)
		return false
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

// GetUnsyncedCount returns the number of rows not yet pushed to Supabase.
// Useful for health checks / metrics.
func (pc *PostgresCache) GetUnsyncedCount() int {
	var count int
	_ = pc.db.QueryRow(`SELECT COUNT(*) FROM sent_news WHERE supabase_synced = FALSE`).Scan(&count)
	return count
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

	// Clean translation_cache (using last_used_at)
	// We can use the same TTL or a longer one. For now, using the same TTL to save space.
	queryTrans := `DELETE FROM translation_cache WHERE last_used_at < $1`
	resultTrans, err := pc.db.Exec(queryTrans, cutoffTime)
	if err != nil {
		logger.Warn("Failed to cleanup translation_cache", "error", err)
		// Don't fail the whole cleanup if this fails
	} else {
		rowsTrans, _ := resultTrans.RowsAffected()
		if rowsTrans > 0 {
			logger.Info("Cleaned old translation_cache records", "rows", rowsTrans)
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

// GetStats returns cache statistics
func (pc *PostgresCache) GetStats() (map[string]int, error) {
	stats := make(map[string]int)

	// Total items
	var total int
	err := pc.db.QueryRow(`SELECT COUNT(*) FROM sent_news`).Scan(&total)
	if err != nil {
		return nil, err
	}
	stats["total_items"] = total

	// Items within TTL
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)
	var active int
	err = pc.db.QueryRow(`SELECT COUNT(*) FROM sent_news WHERE sent_at > $1`, cutoffTime).Scan(&active)
	if err != nil {
		return nil, err
	}
	stats["active_items"] = active

	// Items by category
	rows, err := pc.db.Query(`
		SELECT category, COUNT(*) 
		FROM sent_news 
		WHERE sent_at > $1 
		GROUP BY category
	`, cutoffTime)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var category string
			var count int
			if err := rows.Scan(&category, &count); err == nil {
				stats["category_"+category] = count
			}
		}
	}

	return stats, nil
}

// GetRecentNews returns recently sent news for debugging
func (pc *PostgresCache) GetRecentNews(limit int) ([]SentNewsItem, error) {
	if limit <= 0 {
		limit = 10
	}

	query := `
		SELECT hash, title, link, category, source, sent_at
		FROM sent_news
		ORDER BY sent_at DESC
		LIMIT $1
	`

	rows, err := pc.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []SentNewsItem
	for rows.Next() {
		var item SentNewsItem
		err := rows.Scan(&item.Hash, &item.Title, &item.Link, &item.Category, &item.Source, &item.SentAt)
		if err != nil {
			logger.Warn("Error scanning recent news row", "error", err)
			continue
		}
		items = append(items, item)
	}

	return items, nil
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

// GetTranslationCache retrieves translation from cache
func (pc *PostgresCache) GetTranslationCache(contentHash string) (TranslationCacheItem, error) {
	var item TranslationCacheItem

	query := `
		SELECT content_hash, title, content, summary, danish_translation, ukrainian_translation, ai_provider, created_at, last_used_at, use_count
		FROM translation_cache
		WHERE content_hash = $1
	`

	err := pc.db.QueryRow(query, contentHash).Scan(
		&item.ContentHash, &item.Title, &item.Content, &item.Summary,
		&item.DanishTranslation, &item.UkrainianTranslation, &item.AIProvider,
		&item.CreatedAt, &item.LastUsedAt, &item.UseCount,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return item, nil // Not found, return zero value
		}
		return item, fmt.Errorf("failed to get translation from cache: %v", err)
	}

	return item, nil
}

// SetTranslationCache stores translation in cache
func (pc *PostgresCache) SetTranslationCache(item TranslationCacheItem) error {
	// Use INSERT ON CONFLICT to handle updates
	query := `
		INSERT INTO translation_cache (content_hash, title, content, summary, danish_translation, ukrainian_translation, ai_provider, created_at, last_used_at, use_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW(), 1)
		ON CONFLICT (content_hash) DO UPDATE SET
			title = EXCLUDED.title,
			content = EXCLUDED.content,
			summary = EXCLUDED.summary,
			danish_translation = EXCLUDED.danish_translation,
			ukrainian_translation = EXCLUDED.ukrainian_translation,
			ai_provider = EXCLUDED.ai_provider,
			last_used_at = NOW(),
			use_count = translation_cache.use_count + 1
	`

	_, err := pc.db.Exec(query, item.ContentHash, item.Title, item.Content, item.Summary, item.DanishTranslation, item.UkrainianTranslation, item.AIProvider)
	if err != nil {
		return fmt.Errorf("failed to set translation cache: %v", err)
	}

	return nil
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

// GetTelegramUpdateOffset returns the last processed Telegram update_id.
func (pc *PostgresCache) GetTelegramUpdateOffset() (int64, error) {
	raw, found, err := pc.GetBotState("telegram_update_offset")
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	var offset int64
	if _, err := fmt.Sscan(raw, &offset); err != nil {
		return 0, fmt.Errorf("invalid telegram_update_offset value %q: %w", raw, err)
	}
	return offset, nil
}

// SaveTelegramUpdateOffset persists the last processed Telegram update_id.
func (pc *PostgresCache) SaveTelegramUpdateOffset(offset int64) error {
	return pc.SaveBotState("telegram_update_offset", fmt.Sprintf("%d", offset))
}

// GetBotState reads a small state value from telegram_bot_state table.
func (pc *PostgresCache) GetBotState(key string) (string, bool, error) {
	if strings.TrimSpace(key) == "" {
		return "", false, nil
	}

	var raw string
	err := pc.db.QueryRow(`SELECT state_value FROM telegram_bot_state WHERE state_key = $1`, key).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}

	return raw, true, nil
}

// SaveBotState upserts a small state value in telegram_bot_state table.
func (pc *PostgresCache) SaveBotState(key, value string) error {
	if strings.TrimSpace(key) == "" {
		return nil
	}

	_, err := pc.db.Exec(`
		INSERT INTO telegram_bot_state (state_key, state_value, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (state_key) DO UPDATE SET
			state_value = EXCLUDED.state_value,
			updated_at = NOW()
	`, key, value)
	return err
}

// SaveFeedbackButtonToken stores callback token -> sent news hash mapping.
func (pc *PostgresCache) SaveFeedbackButtonToken(token, newsHash string) error {
	if token == "" || newsHash == "" {
		return nil
	}
	_, err := pc.db.Exec(`
		INSERT INTO feedback_button_tokens (token, news_hash, created_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (token) DO UPDATE SET news_hash = EXCLUDED.news_hash
	`, token, newsHash)
	return err
}

// ResolveFeedbackButtonToken resolves callback token to sent news hash.
func (pc *PostgresCache) ResolveFeedbackButtonToken(token string) (string, bool, error) {
	if token == "" {
		return "", false, nil
	}
	var newsHash string
	err := pc.db.QueryRow(`SELECT news_hash FROM feedback_button_tokens WHERE token = $1`, token).Scan(&newsHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return newsHash, true, nil
}

// SaveTelegramReaction stores callback event idempotently and upserts user's current vote.
// Returns true when a new update_id was applied, false when it was a duplicate update.
func (pc *PostgresCache) SaveTelegramReaction(updateID int64, newsHash string, userID int64, reaction int) (bool, error) {
	if updateID == 0 || newsHash == "" || userID == 0 {
		return false, nil
	}
	if reaction != 1 && reaction != -1 {
		return false, fmt.Errorf("unsupported reaction value: %d", reaction)
	}

	tx, err := pc.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	result, err := tx.Exec(`
		INSERT INTO telegram_feedback_events (update_id, news_hash, user_id, reaction, created_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (update_id) DO NOTHING
	`, updateID, newsHash, userID, reaction)
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 0 {
		err = tx.Commit()
		if err != nil {
			return false, err
		}
		return false, nil
	}

	_, err = tx.Exec(`
		INSERT INTO telegram_feedback_votes (news_hash, user_id, reaction, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (news_hash, user_id) DO UPDATE SET
			reaction = EXCLUDED.reaction,
			updated_at = NOW()
	`, newsHash, userID, reaction)
	if err != nil {
		return false, err
	}

	err = tx.Commit()
	if err != nil {
		return false, err
	}
	return true, nil
}
