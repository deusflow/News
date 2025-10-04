package ratelimit

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// AIRateLimiter manages rate limiting for all AI services
type AIRateLimiter struct {
	mu           sync.Mutex
	geminiCount  int
	groqCount    int
	cohereCount  int
	mistralCount int
	totalCount   int
	maxGemini    int
	maxGroq      int
	maxCohere    int
	maxMistral   int
	maxTotal     int
	resetTime    time.Time
	tokensSaved  int // Track how many tokens we saved via caching
	cacheHits    int
	cacheMisses  int
}

// NewAIRateLimiter creates a new rate limiter with configurable limits
func NewAIRateLimiter(maxGemini, maxGroq, maxCohere, maxMistral, maxTotal int) *AIRateLimiter {
	return &AIRateLimiter{
		maxGemini:  maxGemini,
		maxGroq:    maxGroq,
		maxCohere:  maxCohere,
		maxMistral: maxMistral,
		maxTotal:   maxTotal,
		resetTime:  time.Now().Add(24 * time.Hour), // Reset daily
	}
}

// CanUseGemini checks if we can make a Gemini request
func (rl *AIRateLimiter) CanUseGemini() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.checkReset()

	if rl.maxGemini > 0 && rl.geminiCount >= rl.maxGemini {
		log.Printf("⚠️ Gemini rate limit reached (%d/%d)", rl.geminiCount, rl.maxGemini)
		return false
	}

	if rl.maxTotal > 0 && rl.totalCount >= rl.maxTotal {
		log.Printf("⚠️ Total AI rate limit reached (%d/%d)", rl.totalCount, rl.maxTotal)
		return false
	}

	return true
}

// CanUseGroq checks if we can make a Groq request
func (rl *AIRateLimiter) CanUseGroq() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.checkReset()

	if rl.maxGroq > 0 && rl.groqCount >= rl.maxGroq {
		log.Printf("⚠️ Groq rate limit reached (%d/%d)", rl.groqCount, rl.maxGroq)
		return false
	}

	if rl.maxTotal > 0 && rl.totalCount >= rl.maxTotal {
		log.Printf("⚠️ Total AI rate limit reached (%d/%d)", rl.totalCount, rl.maxTotal)
		return false
	}

	return true
}

// CanUseCohere checks if we can make a Cohere request
func (rl *AIRateLimiter) CanUseCohere() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.checkReset()

	if rl.maxCohere > 0 && rl.cohereCount >= rl.maxCohere {
		log.Printf("⚠️ Cohere rate limit reached (%d/%d)", rl.cohereCount, rl.maxCohere)
		return false
	}

	if rl.maxTotal > 0 && rl.totalCount >= rl.maxTotal {
		log.Printf("⚠️ Total AI rate limit reached (%d/%d)", rl.totalCount, rl.maxTotal)
		return false
	}

	return true
}

// CanUseMistral checks if we can make a Mistral request
func (rl *AIRateLimiter) CanUseMistral() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.checkReset()

	if rl.maxMistral > 0 && rl.mistralCount >= rl.maxMistral {
		log.Printf("⚠️ Mistral rate limit reached (%d/%d)", rl.mistralCount, rl.maxMistral)
		return false
	}

	if rl.maxTotal > 0 && rl.totalCount >= rl.maxTotal {
		log.Printf("⚠️ Total AI rate limit reached (%d/%d)", rl.totalCount, rl.maxTotal)
		return false
	}

	return true
}

// UseGemini increments Gemini counter
func (rl *AIRateLimiter) UseGemini() error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.checkReset()

	if rl.maxGemini > 0 && rl.geminiCount >= rl.maxGemini {
		return fmt.Errorf("gemini rate limit exceeded")
	}

	if rl.maxTotal > 0 && rl.totalCount >= rl.maxTotal {
		return fmt.Errorf("total AI rate limit exceeded")
	}

	rl.geminiCount++
	rl.totalCount++
	rl.cacheMisses++

	log.Printf("📊 AI Usage: Gemini=%d/%d, Total=%d/%d", rl.geminiCount, rl.maxGemini, rl.totalCount, rl.maxTotal)

	return nil
}

// UseGroq increments Groq counter
func (rl *AIRateLimiter) UseGroq() error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.checkReset()

	if rl.maxGroq > 0 && rl.groqCount >= rl.maxGroq {
		return fmt.Errorf("groq rate limit exceeded")
	}

	if rl.maxTotal > 0 && rl.totalCount >= rl.maxTotal {
		return fmt.Errorf("total AI rate limit exceeded")
	}

	rl.groqCount++
	rl.totalCount++
	rl.cacheMisses++

	log.Printf("📊 AI Usage: Groq=%d/%d, Total=%d/%d", rl.groqCount, rl.maxGroq, rl.totalCount, rl.maxTotal)

	return nil
}

// UseCohere increments Cohere counter
func (rl *AIRateLimiter) UseCohere() error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.checkReset()

	if rl.maxCohere > 0 && rl.cohereCount >= rl.maxCohere {
		return fmt.Errorf("cohere rate limit exceeded")
	}

	if rl.maxTotal > 0 && rl.totalCount >= rl.maxTotal {
		return fmt.Errorf("total AI rate limit exceeded")
	}

	rl.cohereCount++
	rl.totalCount++
	rl.cacheMisses++

	log.Printf("📊 AI Usage: Cohere=%d/%d, Total=%d/%d", rl.cohereCount, rl.maxCohere, rl.totalCount, rl.maxTotal)

	return nil
}

// UseMistral increments Mistral counter
func (rl *AIRateLimiter) UseMistral() error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.checkReset()

	if rl.maxMistral > 0 && rl.mistralCount >= rl.maxMistral {
		return fmt.Errorf("mistral rate limit exceeded")
	}

	if rl.maxTotal > 0 && rl.totalCount >= rl.maxTotal {
		return fmt.Errorf("total AI rate limit exceeded")
	}

	rl.mistralCount++
	rl.totalCount++
	rl.cacheMisses++

	log.Printf("📊 AI Usage: Mistral=%d/%d, Total=%d/%d", rl.mistralCount, rl.maxMistral, rl.totalCount, rl.maxTotal)

	return nil
}

// RecordCacheHit records when we use cached translation (saves tokens!)
func (rl *AIRateLimiter) RecordCacheHit(estimatedTokens int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.cacheHits++
	rl.tokensSaved += estimatedTokens

	log.Printf("💰 Cache HIT! Saved ~%d tokens (Total saved: %d, Hit rate: %.1f%%)",
		estimatedTokens, rl.tokensSaved, rl.GetCacheHitRate())
}

// GetCacheHitRate returns cache hit rate percentage
func (rl *AIRateLimiter) GetCacheHitRate() float64 {
	total := rl.cacheHits + rl.cacheMisses
	if total == 0 {
		return 0
	}
	return float64(rl.cacheHits) / float64(total) * 100
}

// GetStats returns current rate limiter statistics
func (rl *AIRateLimiter) GetStats() map[string]interface{} {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	return map[string]interface{}{
		"gemini_used":    rl.geminiCount,
		"gemini_limit":   rl.maxGemini,
		"groq_used":      rl.groqCount,
		"groq_limit":     rl.maxGroq,
		"cohere_used":    rl.cohereCount,
		"cohere_limit":   rl.maxCohere,
		"mistral_used":   rl.mistralCount,
		"mistral_limit":  rl.maxMistral,
		"total_used":     rl.totalCount,
		"total_limit":    rl.maxTotal,
		"cache_hits":     rl.cacheHits,
		"cache_misses":   rl.cacheMisses,
		"cache_hit_rate": rl.GetCacheHitRate(),
		"tokens_saved":   rl.tokensSaved,
		"reset_time":     rl.resetTime,
	}
}

// PrintStats logs current statistics
func (rl *AIRateLimiter) PrintStats() {
	stats := rl.GetStats()
	log.Printf("📊 === AI Rate Limiter Statistics ===")
	log.Printf("  Gemini:  %d/%d", stats["gemini_used"], stats["gemini_limit"])
	log.Printf("  Groq:    %d/%d", stats["groq_used"], stats["groq_limit"])
	log.Printf("  Cohere:  %d/%d", stats["cohere_used"], stats["cohere_limit"])
	log.Printf("  Mistral: %d/%d", stats["mistral_used"], stats["mistral_limit"])
	log.Printf("  Total:   %d/%d", stats["total_used"], stats["total_limit"])
	log.Printf("  Cache:   %d hits, %d misses (%.1f%% hit rate)",
		stats["cache_hits"], stats["cache_misses"], stats["cache_hit_rate"])
	log.Printf("  Tokens saved: ~%d", stats["tokens_saved"])
	log.Printf("=====================================")
}

// checkReset resets counters if reset time has passed
func (rl *AIRateLimiter) checkReset() {
	if time.Now().After(rl.resetTime) {
		log.Printf("🔄 Resetting AI rate limiter counters")
		rl.PrintStats() // Print final stats before reset

		rl.geminiCount = 0
		rl.groqCount = 0
		rl.cohereCount = 0
		rl.mistralCount = 0
		rl.totalCount = 0
		rl.cacheHits = 0
		rl.cacheMisses = 0
		rl.tokensSaved = 0
		rl.resetTime = time.Now().Add(24 * time.Hour)
	}
}
