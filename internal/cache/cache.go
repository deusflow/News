package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type CacheItem struct {
	Value     interface{}
	ExpiresAt time.Time
}

type Cache struct {
	mu    sync.RWMutex
	items map[string]CacheItem
}

func New() *Cache {
	c := &Cache{
		items: make(map[string]CacheItem),
	}

	// Cleanup expired items every hour
	go c.cleanupLoop()

	return c
}

func (c *Cache) Set(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = CacheItem{
		Value:     value,
		ExpiresAt: time.Now().Add(ttl),
	}
}

func (c *Cache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, exists := c.items[key]
	if !exists {
		return nil, false
	}

	if time.Now().After(item.ExpiresAt) {
		delete(c.items, key)
		return nil, false
	}

	return item.Value, true
}

func (c *Cache) GenerateKey(title, content string) string {
	h := sha256.New()
	h.Write([]byte(title + content))
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Cache) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.cleanup()
		}
	}
}

func (c *Cache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, item := range c.items {
		if now.After(item.ExpiresAt) {
			delete(c.items, key)
		}
	}
}
