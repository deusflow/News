package ai

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Manager struct {
	providers []Provider
}

func NewManager(providers ...Provider) *Manager {
	return &Manager{
		providers: providers,
	}
}

func (m *Manager) Close() {
	for _, p := range m.providers {
		p.Close()
	}
}

func (m *Manager) Name() string {
	return "ai-manager"
}

// tryParseRetrySeconds attempts to find a "retry in Xs" or "retry after Xs" style hint in error text.
// Returns seconds (int) and true if found.
func tryParseRetrySeconds(text string) (int, bool) {
	if text == "" {
		return 0, false
	}
	// Normalize
	lower := strings.ToLower(text)

	// Common patterns: "please retry in 58.66s", "retry in 58s", "retry after 60 seconds"
	re := regexp.MustCompile(`(?i)retry(?:\s*(?:in|after))?\s*[:]?\s*(\d+(?:\.\d+)?)\s*s`) // captures seconds with optional decimals
	if m := re.FindStringSubmatch(lower); len(m) >= 2 {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			secs := int(f + 0.5)
			return secs, true
		}
	}

	// Pattern: "retry after 60 seconds"
	re2 := regexp.MustCompile(`(\d+)\s*seconds`) // simple fallback
	if m := re2.FindStringSubmatch(lower); len(m) >= 2 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			return v, true
		}
	}

	return 0, false
}

// Generate пробует провайдеров по очереди; при 429/RateLimit может ждать и повторить запрос к тому же провайдеру
func (m *Manager) Generate(ctx context.Context, title, content, prompt string) (*Response, error) {
	var lastErr error

	for _, provider := range m.providers {
		log.Printf("🤖 Trying AI provider: %s", provider.Name())

		resp, err := provider.Generate(ctx, title, content, prompt)
		if err == nil {
			log.Printf("✅ AI Success with %s", provider.Name())
			return resp, nil
		}

		// If the provider signalled a rate limit and suggested a small retry window, honor it (<=180s)
		if secs, ok := tryParseRetrySeconds(err.Error()); ok {
			// Cap wait to 180s (3 minutes)
			if secs > 0 && secs <= 180 {
				log.Printf("⚠️ Provider %s requested retry after %ds — waiting then retrying once", provider.Name(), secs)
				select {
				case <-time.After(time.Duration(secs+2) * time.Second): // small buffer
					// proceed to retry
				case <-ctx.Done():
					return nil, ctx.Err()
				}

				// Retry once
				resp2, err2 := provider.Generate(ctx, title, content, prompt)
				if err2 == nil {
					log.Printf("✅ AI Success with %s (after wait)", provider.Name())
					return resp2, nil
				}
				// second failure - record and continue to next provider
				log.Printf("⚠️ Provider %s still failed after retry: %v", provider.Name(), err2)
				lastErr = err2
				continue
			}
			// If suggested wait is large (>180s), skip provider
			log.Printf("⚠️ Provider %s suggested long retry (%ds) — skipping to next provider", provider.Name(), secs)
			lastErr = err
			continue
		}

		// Not a rate-limit hint (or couldn't parse): log and continue
		log.Printf("⚠️ Provider %s failed: %v", provider.Name(), err)
		lastErr = err
	}

	return nil, fmt.Errorf("all AI providers failed, last error: %v", lastErr)
}
