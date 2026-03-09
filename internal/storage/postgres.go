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
func (pc *PostgresCache) MarkAsSentWithContent(hash, title, link, content, category, source string) error {
	contentHash := ""
	if len(content) >= 100 {
		contentHash = generateContentHash(content)
	}

	query := `
		INSERT INTO sent_news (hash, title, link, source_url, content_hash, category, source, sent_at, supabase_synced)
		VALUES ($1, $2, $3, $3, $4, $5, $6, NOW(), FALSE)
		ON CONFLICT (hash) DO UPDATE SET
			sent_at = NOW(),
			content_hash = EXCLUDED.content_hash,
			source_url = EXCLUDED.source_url
	`

	_, err := pc.db.Exec(query, hash, title, link, contentHash, category, source)
	if err != nil {
		return fmt.Errorf("failed to mark as sent: %v", err)
	}

	return nil
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
