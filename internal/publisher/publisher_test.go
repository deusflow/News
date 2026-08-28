package publisher_test

import (
	"context"
	"testing"

	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/news"
	"github.com/deusflow/News/internal/publisher"
)

type mockCacheAdapter struct {
	sentFacts      map[string]bool
	markedFacts    []string
	sentItems      map[string]string
	savedDLQ       []string
	markContentErr error
}

func newMockCacheAdapter() *mockCacheAdapter {
	return &mockCacheAdapter{
		sentFacts: make(map[string]bool),
		sentItems: make(map[string]string),
	}
}

func (m *mockCacheAdapter) IsFunFactRecentlyUsed(fact string) bool {
	return m.sentFacts[fact]
}

func (m *mockCacheAdapter) MarkFunFactUsed(fact string) error {
	m.markedFacts = append(m.markedFacts, fact)
	return nil
}

func (m *mockCacheAdapter) SaveFailedNews(title, link, imageURL, messageText, errorMsg string) error {
	m.savedDLQ = append(m.savedDLQ, title)
	return nil
}

func (m *mockCacheAdapter) MarkAsSentWithContent(hash, title, link, content, category, source string) error {
	if m.markContentErr != nil {
		return m.markContentErr
	}
	m.sentItems[hash] = title
	return nil
}

func TestTelegramPublisher_FunFactDedup(t *testing.T) {
	cfg := &config.Config{
		Posting: config.PostingConfig{PhotoTextLimit: 1024},
		Telegram: config.TelegramConfig{
			Token:  "dummy_token",
			ChatID: "dummy_chat",
		},
	}
	m := metrics.New()
	cache := newMockCacheAdapter()
	cache.sentFacts["Existing Fact"] = true

	pub := publisher.NewTelegramPublisher(cfg, cache, m)

	n := news.News{
		Title:            "Test News",
		Category:         "work",
		SummaryDanish:    "Dansk tekst",
		SummaryUkrainian: "Український текст",
		TLDR:             "TLDR",
		FunFact:          "Existing Fact",
	}

	// We pass a dummy hash and mock context
	// When Publish is called, FunFact should be dropped if already used
	_, _ = pub.Publish(context.Background(), n, "hash123")

	// Verify that the duplicate fun fact was NOT marked as used again
	for _, f := range cache.markedFacts {
		if f == "Existing Fact" {
			t.Errorf("Duplicate fun fact should not have been marked as used")
		}
	}
}
