package breaking_test

import (
	"context"
	"os"
	"testing"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/breaking"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/metrics"
)

type mockDedupeChecker struct {
	sentURLs   map[string]bool
	sentHashes map[string]bool
	markedHash []string
}

func (m *mockDedupeChecker) GenerateNewsHash(title, link string) string {
	return "hash_" + title + "_" + link
}

func (m *mockDedupeChecker) IsAlreadySent(hash string) bool {
	return m.sentHashes[hash]
}

func (m *mockDedupeChecker) IsSourceURLSent(sourceURL string) bool {
	return m.sentURLs[sourceURL]
}

func (m *mockDedupeChecker) MarkAsSentWithContent(hash, title, link, content, category, source string) error {
	m.markedHash = append(m.markedHash, hash)
	return nil
}

func TestBreaking_Deduplication(t *testing.T) {
	url := "https://www.dr.dk/nyheder/test-breaking"
	title := "Test Breaking Title"

	os.Setenv("BREAKING_URL", url)
	os.Setenv("BREAKING_TITLE", title)
	defer func() {
		os.Unsetenv("BREAKING_URL")
		os.Unsetenv("BREAKING_TITLE")
	}()

	cache := &mockDedupeChecker{
		sentURLs:   map[string]bool{url: true},
		sentHashes: make(map[string]bool),
	}

	cfg := &config.Config{
		Telegram: config.TelegramConfig{
			Token:  "test_token",
			ChatID: "test_chat",
		},
	}
	m := metrics.New()
	mgr := ai.NewManager(m, 5)

	// Since cache.IsSourceURLSent(url) is true, Run should return nil immediately without attempting AI
	err := breaking.Run(context.Background(), cfg, mgr, cache)
	if err != nil {
		t.Fatalf("Expected nil error on duplicate URL, got: %v", err)
	}

	if len(cache.markedHash) > 0 {
		t.Errorf("Should not have marked duplicate as sent")
	}
}
