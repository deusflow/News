package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/deusflow/News/internal/ai/embedding"
)

// SentNewsItem represents a news item that was already sent
type SentNewsItem struct {
	Hash            string    `json:"hash"`
	Title           string    `json:"title"`
	TitleUkrainian  string    `json:"title_ukrainian,omitempty"`
	StoryClusterKey string    `json:"story_cluster_key,omitempty"`
	Embedding       []float32 `json:"embedding,omitempty"`
	Link            string    `json:"link"`
	Category        string    `json:"category"`
	SentAt          time.Time `json:"sent_at"`
	Source          string    `json:"source"`
}

// FileCache manages sent news items in a JSON file
type FileCache struct {
	filePath     string
	ttlHours     int
	items        map[string]SentNewsItem
	delayedPosts []DelayedPost
	mu           sync.RWMutex
}

// NewFileCache creates a new file cache instance
func NewFileCache(filePath string, ttlHours int) *FileCache {
	return &FileCache{
		filePath: filePath,
		ttlHours: ttlHours,
		items:    make(map[string]SentNewsItem),
	}
}

// Load loads existing cache from file
func (fc *FileCache) Load() error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	// Check if file exists
	if _, err := os.Stat(fc.filePath); os.IsNotExist(err) {
		// File doesn't exist, start with empty cache
		return nil
	}

	data, err := os.ReadFile(fc.filePath)
	if err != nil {
		return fmt.Errorf("failed to read cache file: %v", err)
	}

	if len(data) == 0 {
		return nil // Empty file
	}

	var items []SentNewsItem
	if err := json.Unmarshal(data, &items); err != nil {
		return fmt.Errorf("failed to unmarshal cache: %v", err)
	}

	// Load items into memory, filtering expired ones
	cutoffTime := time.Now().Add(-time.Duration(fc.ttlHours) * time.Hour)
	for _, item := range items {
		if item.SentAt.After(cutoffTime) {
			fc.items[item.Hash] = item
		}
	}

	return nil
}

// Save saves current cache to file
func (fc *FileCache) Save() error {
	fc.mu.RLock()
	items := make([]SentNewsItem, 0, len(fc.items))
	for _, item := range fc.items {
		items = append(items, item)
	}
	fc.mu.RUnlock()

	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache: %v", err)
	}

	if err := os.WriteFile(fc.filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %v", err)
	}

	return nil
}

// GenerateNewsHash creates a stable hash for news item
func (fc *FileCache) GenerateNewsHash(title, link string) string {
	// Normalize title: lowercase, trim spaces, remove extra whitespace
	normalizedTitle := strings.ToLower(strings.TrimSpace(title))
	normalizedTitle = strings.Join(strings.Fields(normalizedTitle), " ")

	// Extract domain from link for uniqueness
	domain := extractDomain(link)

	// Create hash from normalized title + domain
	h := sha256.New()
	h.Write([]byte(normalizedTitle + "|" + domain))
	return hex.EncodeToString(h.Sum(nil))[:16] // Use first 16 characters
}

// IsAlreadySent checks if news was already sent
func (fc *FileCache) IsAlreadySent(hash string) bool {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	item, exists := fc.items[hash]
	if !exists {
		return false
	}

	// Check if item is still within TTL
	cutoffTime := time.Now().Add(-time.Duration(fc.ttlHours) * time.Hour)
	return item.SentAt.After(cutoffTime)
}

// MarkAsSent marks news as sent
func (fc *FileCache) MarkAsSent(hash, title, link, category, source string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	fc.items[hash] = SentNewsItem{
		Hash:     hash,
		Title:    title,
		Link:     link,
		Category: category,
		SentAt:   time.Now(),
		Source:   source,
	}
}

// MarkAsSentWithSemanticData marks news as sent with semantic metadata
func (fc *FileCache) MarkAsSentWithSemanticData(hash, title, link, category, source, titleUA, clusterKey string, emb []float32) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	fc.items[hash] = SentNewsItem{
		Hash:            hash,
		Title:           title,
		TitleUkrainian:  titleUA,
		StoryClusterKey: clusterKey,
		Embedding:       emb,
		Link:            link,
		Category:        category,
		SentAt:          time.Now(),
		Source:          source,
	}
}

// CheckSemanticDuplicate checks duplicate in file cache memory using OR logic
func (fc *FileCache) CheckSemanticDuplicate(clusterKey string, candidateEmbedding []float32, titleUA string, lookback time.Duration, keyThreshold, cosineThreshold float64, shadowMode bool) (SemanticCheckResult, error) {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	if lookback <= 0 {
		lookback = 7 * 24 * time.Hour
	}
	cutoffTime := time.Now().Add(-lookback)

	result := SemanticCheckResult{
		ShadowMode: shadowMode,
		Trigger:    "none",
	}

	for _, item := range fc.items {
		if item.SentAt.Before(cutoffTime) {
			continue
		}

		// Tier 1: Cluster Key check
		if clusterKey != "" && item.StoryClusterKey != "" {
			simKey := embedding.ClusterKeySimilarity(clusterKey, item.StoryClusterKey)
			if simKey > result.ClusterSimilarity {
				result.ClusterSimilarity = simKey
				if simKey >= keyThreshold && result.MatchedTitle == "" {
					result.MatchedTitle = item.TitleUkrainian
					if result.MatchedTitle == "" {
						result.MatchedTitle = item.Title
					}
				}
			}
		}

		// Tier 2: Embedding check
		if len(candidateEmbedding) > 0 && len(item.Embedding) > 0 {
			simCos := embedding.CosineSimilarity(candidateEmbedding, item.Embedding)
			if simCos > result.CosineSimilarity {
				result.CosineSimilarity = simCos
				if simCos >= cosineThreshold {
					matched := item.TitleUkrainian
					if matched == "" {
						matched = item.Title
					}
					result.MatchedTitle = matched
				}
			}
		}
	}

	tier1Fired := result.ClusterSimilarity >= keyThreshold && keyThreshold > 0
	tier2Fired := result.CosineSimilarity >= cosineThreshold && cosineThreshold > 0

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

	if shadowMode {
		result.IsDuplicate = tier1Fired
	} else {
		result.IsDuplicate = result.WouldReject
	}

	return result, nil
}

// Cleanup removes expired items from memory
func (fc *FileCache) Cleanup() {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	cutoffTime := time.Now().Add(-time.Duration(fc.ttlHours) * time.Hour)
	for hash, item := range fc.items {
		if item.SentAt.Before(cutoffTime) {
			delete(fc.items, hash)
		}
	}
}

// GetStats returns cache statistics
func (fc *FileCache) GetStats() map[string]int {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	return map[string]int{
		"total_items": len(fc.items),
	}
}

// extractDomain extracts domain from URL
func extractDomain(url string) string {
	if url == "" {
		return "unknown"
	}

	// Remove protocol
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimPrefix(url, "https://")

	// Get domain part
	parts := strings.Split(url, "/")
	if len(parts) == 0 {
		return "unknown"
	}

	domain := parts[0]
	// Remove www. prefix
	domain = strings.TrimPrefix(domain, "www.")

	return strings.ToLower(domain)
}

// EnqueueDelayedPost stores delayed post in memory for FileCache.
func (fc *FileCache) EnqueueDelayedPost(hash, title, link, newsJSON string, delay time.Duration) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	post := DelayedPost{
		ID:           len(fc.delayedPosts) + 1,
		Hash:         hash,
		Title:        title,
		Link:         link,
		NewsJSON:     newsJSON,
		PublishAfter: time.Now().Add(delay),
		Status:       "pending",
		CreatedAt:    time.Now(),
	}
	fc.delayedPosts = append(fc.delayedPosts, post)
	return nil
}

// GetReadyDelayedPosts returns delayed posts ready to send.
func (fc *FileCache) GetReadyDelayedPosts() ([]DelayedPost, error) {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	now := time.Now()
	var ready []DelayedPost
	for _, p := range fc.delayedPosts {
		if p.Status == "pending" && (p.PublishAfter.Before(now) || p.PublishAfter.Equal(now)) {
			ready = append(ready, p)
		}
	}
	return ready, nil
}

// MarkDelayedPostSent marks post as sent in memory.
func (fc *FileCache) MarkDelayedPostSent(id int) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	for i := range fc.delayedPosts {
		if fc.delayedPosts[i].ID == id {
			fc.delayedPosts[i].Status = "sent"
			return nil
		}
	}
	return nil
}

// MarkDelayedPostFailed marks post as failed in memory.
func (fc *FileCache) MarkDelayedPostFailed(id int, errMsg string) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	for i := range fc.delayedPosts {
		if fc.delayedPosts[i].ID == id {
			fc.delayedPosts[i].Status = "failed"
			return nil
		}
	}
	return nil
}
