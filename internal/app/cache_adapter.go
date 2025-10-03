package app

import (
	"github.com/deusflow/News/internal/storage"
)

// CacheAdapter provides a unified interface for different cache implementations
type CacheAdapter interface {
	GenerateNewsHash(title, link string) string
	IsAlreadySent(hash string) bool
	IsLinkAlreadySent(link string) bool
	MarkAsSent(hash, title, link, category, source string) error
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
	// File cache doesn't have direct link check, so generate hash from link
	// This is a simplified check - in practice, file cache checks by hash only
	return false
}

func (f *FileCacheAdapter) MarkAsSent(hash, title, link, category, source string) error {
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

func (p *PostgresCacheAdapter) MarkAsSent(hash, title, link, category, source string) error {
	return p.cache.MarkAsSent(hash, title, link, category, source)
}
