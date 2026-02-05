package app

import (
	"github.com/deusflow/News/internal/storage"
)

// CacheAdapter provides a unified interface for different cache implementations
type CacheAdapter interface {
	GenerateNewsHash(title, link string) string
	IsAlreadySent(hash string) bool
	IsLinkAlreadySent(link string) bool
	IsContentDuplicate(content string) (bool, string) // Returns (isDuplicate, existingTitle)
	MarkAsSent(hash, title, link, category, source string) error
	MarkAsSentWithContent(hash, title, link, content, category, source string) error
}

// FileCacheAdapter wraps FileCache to implement CacheAdapter
type FileCacheAdapter struct {
	cache *storage.FileCache
}

func (f *FileCacheAdapter) GenerateNewsHash(title, link string) string {
	return f.cache.GenerateNewsHash(title, link)
}

func (f *FileCacheAdapter) IsAlreadySent(hash string) bool {
	return f.cache.IsAlreadySent(hash)
}

func (f *FileCacheAdapter) IsLinkAlreadySent(link string) bool {
	// File cache doesn't have direct link check
	return false
}

func (f *FileCacheAdapter) IsContentDuplicate(content string) (bool, string) {
	// File cache doesn't support content duplicate detection
	return false, ""
}

func (f *FileCacheAdapter) MarkAsSent(hash, title, link, category, source string) error {
	f.cache.MarkAsSent(hash, title, link, category, source)
	return nil
}

func (f *FileCacheAdapter) MarkAsSentWithContent(hash, title, link, content, category, source string) error {
	// File cache doesn't support content hash, fall back to regular MarkAsSent
	f.cache.MarkAsSent(hash, title, link, category, source)
	return nil
}

// PostgresCacheAdapter wraps PostgresCache to implement CacheAdapter
type PostgresCacheAdapter struct {
	cache *storage.PostgresCache
}

func (p *PostgresCacheAdapter) GenerateNewsHash(title, link string) string {
	return p.cache.GenerateNewsHash(title, link)
}

func (p *PostgresCacheAdapter) IsAlreadySent(hash string) bool {
	return p.cache.IsAlreadySent(hash)
}

func (p *PostgresCacheAdapter) IsLinkAlreadySent(link string) bool {
	return p.cache.IsLinkAlreadySent(link)
}

func (p *PostgresCacheAdapter) IsContentDuplicate(content string) (bool, string) {
	return p.cache.IsContentDuplicate(content)
}

func (p *PostgresCacheAdapter) MarkAsSent(hash, title, link, category, source string) error {
	return p.cache.MarkAsSent(hash, title, link, category, source)
}

func (p *PostgresCacheAdapter) MarkAsSentWithContent(hash, title, link, content, category, source string) error {
	return p.cache.MarkAsSentWithContent(hash, title, link, content, category, source)
}
