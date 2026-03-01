package storage

import (
	"database/sql"
	"fmt"
	"hash/fnv"
	"log"
	"sort"
	"strings"
	"time"

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

	log.Println("✅ PostgreSQL cache connected successfully")
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

	// Step 4: Create additional tables
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

	log.Println("✅ Database schema initialized")
	return nil
}

// IsAlreadySent checks if news was already sent (within TTL window)
func (pc *PostgresCache) IsAlreadySent(hash string) bool {
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)

	var count int
	query := `SELECT COUNT(*) FROM sent_news WHERE hash = $1 AND sent_at > $2`
	err := pc.db.QueryRow(query, hash, cutoffTime).Scan(&count)

	if err != nil {
		log.Printf("⚠️ Error checking duplicate: %v", err)
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
		log.Printf("⚠️ Error checking link duplicate: %v", err)
		return false
	}

	return count > 0
}

// IsContentDuplicate checks if content with similar hash already exists
// Uses SimHash-like approach: generates hash from normalized content words
// Two articles about the same event will have very similar content regardless of title
func (pc *PostgresCache) IsContentDuplicate(content string) (bool, string) {
	if len(content) < 100 {
		return false, "" // Too short to be meaningful
	}

	contentHash := generateContentHash(content)
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)

	// Check exact content hash match
	var existingTitle string
	query := `SELECT title FROM sent_news WHERE content_hash = $1 AND sent_at > $2 LIMIT 1`
	err := pc.db.QueryRow(query, contentHash, cutoffTime).Scan(&existingTitle)

	if err == nil {
		log.Printf("🔍 Found duplicate content (exact hash match): %s", existingTitle)
		return true, existingTitle
	}

	return false, ""
}

// generateContentHash creates a signature based on significant numbers in the content.
// News about the same event typically contains the same key statistics/numbers.
// This catches cases like "55000 soldiers killed" from different sources.
//
// Number separator fix: dots and commas are treated as part of a number ONLY when
// the previous character was a digit (i.e. "12.50" or "1,000"). In all other
// positions ("Mr. Smith", "abc, def") they flush the current buffer so we don't
// accidentally join unrelated digit sequences.
func generateContentHash(content string) string {
	normalized := strings.ToLower(content)

	var numbers []string
	var currentNum strings.Builder
	prevWasDigit := false

	for _, r := range normalized {
		if r >= '0' && r <= '9' {
			currentNum.WriteRune(r)
			prevWasDigit = true
		} else if (r == '.' || r == ',') && prevWasDigit {
			// Separator inside a number (e.g. "55.000" or "1,234") — keep accumulating.
			// We intentionally swallow the separator so "55.000" becomes "55000".
			// prevWasDigit stays true.
		} else {
			// Any non-digit, non-separator character (or separator after non-digit)
			// flushes the current number buffer.
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
	// Flush last number
	if currentNum.Len() >= 4 {
		numbers = append(numbers, currentNum.String())
	}

	// If no significant numbers found, use first 200 chars of normalized content
	if len(numbers) == 0 {
		// Fallback: use simple text hash
		var textOnly strings.Builder
		for _, r := range normalized {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				textOnly.WriteRune(r)
			}
		}
		text := textOnly.String()
		if len(text) > 200 {
			text = text[:200]
		}
		h := fnv.New64a()
		h.Write([]byte(text))
		return fmt.Sprintf("%016x", h.Sum64())
	}

	// Sort numbers for consistency
	sort.Strings(numbers)

	// Create signature from significant numbers
	signature := strings.Join(numbers, ",")

	// Generate hash
	h := fnv.New64a()
	h.Write([]byte(signature))
	return fmt.Sprintf("%016x", h.Sum64())
}

// MarkAsSent marks news as sent with transaction to prevent race conditions
func (pc *PostgresCache) MarkAsSent(hash, title, link, category, source string) error {
	// Use INSERT ON CONFLICT to handle race conditions
	query := `
		INSERT INTO sent_news (hash, title, link, category, source, sent_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (hash) DO UPDATE SET sent_at = NOW()
	`

	_, err := pc.db.Exec(query, hash, title, link, category, source)
	if err != nil {
		return fmt.Errorf("failed to mark as sent: %v", err)
	}

	return nil
}

// MarkAsSentWithContent marks news as sent and stores content hash for cross-source duplicate detection
func (pc *PostgresCache) MarkAsSentWithContent(hash, title, link, content, category, source string) error {
	contentHash := ""
	if len(content) >= 100 {
		contentHash = generateContentHash(content)
	}

	query := `
		INSERT INTO sent_news (hash, title, link, content_hash, category, source, sent_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (hash) DO UPDATE SET sent_at = NOW(), content_hash = EXCLUDED.content_hash
	`

	_, err := pc.db.Exec(query, hash, title, link, contentHash, category, source)
	if err != nil {
		return fmt.Errorf("failed to mark as sent: %v", err)
	}

	return nil
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
		log.Printf("🗑️ Cleaned up %d old records from sent_news", rows)
	}

	// Clean translation_cache (using last_used_at)
	// We can use the same TTL or a longer one. For now, using the same TTL to save space.
	queryTrans := `DELETE FROM translation_cache WHERE last_used_at < $1`
	resultTrans, err := pc.db.Exec(queryTrans, cutoffTime)
	if err != nil {
		log.Printf("⚠️ Failed to cleanup translation_cache: %v", err)
		// Don't fail the whole cleanup if this fails
	} else {
		rowsTrans, _ := resultTrans.RowsAffected()
		if rowsTrans > 0 {
			log.Printf("🗑️ Cleaned up %d old records from translation_cache", rowsTrans)
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
			log.Printf("⚠️ Error scanning row: %v", err)
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
