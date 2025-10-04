package storage

import (
	"database/sql"
	"fmt"
	"log"
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

// initSchema creates the necessary tables if they don't exist
func (pc *PostgresCache) initSchema() error {
	schema := `
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
	`

	_, err := pc.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("failed to create schema: %v", err)
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

// Cleanup removes expired items from database
func (pc *PostgresCache) Cleanup() error {
	cutoffTime := time.Now().Add(-time.Duration(pc.ttlHours) * time.Hour)

	query := `DELETE FROM sent_news WHERE sent_at < $1`
	result, err := pc.db.Exec(query, cutoffTime)
	if err != nil {
		return fmt.Errorf("failed to cleanup: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows > 0 {
		log.Printf("🗑️ Cleaned up %d old records from database", rows)
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
