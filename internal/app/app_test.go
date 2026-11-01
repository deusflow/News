package app

import (
	"fmt"
	"testing"

	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/storage"
)

type mockCacheAdapter struct {
	CacheAdapter
	state   map[string]string
	aiCache map[string]string
}

func newMockCacheAdapter() *mockCacheAdapter {
	return &mockCacheAdapter{
		state:   make(map[string]string),
		aiCache: make(map[string]string),
	}
}

func (m *mockCacheAdapter) GetState(key string) (string, error) {
	return m.state[key], nil
}

func (m *mockCacheAdapter) SetState(key, value string) error {
	m.state[key] = value
	return nil
}

func (m *mockCacheAdapter) GetCachedAITranslation(hash string) (string, error) {
	return m.aiCache[hash], nil
}

func (m *mockCacheAdapter) SaveCachedAITranslation(hash, link, title, responseJSON string) error {
	m.aiCache[hash] = responseJSON
	return nil
}

func TestMonitorFeedHealth_IncrementsAndResets(t *testing.T) {
	mock := newMockCacheAdapter()
	cfg := &config.Config{}

	feedName := "DR_News"
	statusesFail := map[string]rss.FeedStatus{
		feedName: {
			Source:    rss.FeedSource{Name: feedName, URL: "https://example.com/rss"},
			ItemCount: 0,
			Err:       fmt.Errorf("connection timeout"),
		},
	}

	// Run 1: 1 failure
	monitorFeedHealth(mock, cfg, statusesFail)
	v1, _ := mock.GetState("feed_fails:" + feedName)
	if v1 != "1" {
		t.Fatalf("expected feed_fails to be 1, got %q", v1)
	}

	// Run 2: 2 failures
	monitorFeedHealth(mock, cfg, statusesFail)
	v2, _ := mock.GetState("feed_fails:" + feedName)
	if v2 != "2" {
		t.Fatalf("expected feed_fails to be 2, got %q", v2)
	}

	// Run 3: 3 failures (alert threshold reached)
	monitorFeedHealth(mock, cfg, statusesFail)
	v3, _ := mock.GetState("feed_fails:" + feedName)
	if v3 != "3" {
		t.Fatalf("expected feed_fails to be 3, got %q", v3)
	}

	// Run 4: recovery with 5 items
	statusesSuccess := map[string]rss.FeedStatus{
		feedName: {
			Source:    rss.FeedSource{Name: feedName, URL: "https://example.com/rss"},
			ItemCount: 5,
			Err:       nil,
		},
	}
	monitorFeedHealth(mock, cfg, statusesSuccess)
	v4, _ := mock.GetState("feed_fails:" + feedName)
	if v4 != "0" {
		t.Fatalf("expected feed_fails to reset to 0 after recovery, got %q", v4)
	}
}

func TestMonitorEmptyRuns_IncrementsAndResets(t *testing.T) {
	mock := newMockCacheAdapter()
	cfg := &config.Config{}
	m := metrics.New()

	// Run 1: 0 messages published
	monitorEmptyRuns(mock, cfg, m, 0)
	c1, _ := mock.GetState("consecutive_empty_runs")
	if c1 != "1" {
		t.Fatalf("expected consecutive_empty_runs to be 1, got %q", c1)
	}

	// Run 2: 0 messages published
	monitorEmptyRuns(mock, cfg, m, 0)
	c2, _ := mock.GetState("consecutive_empty_runs")
	if c2 != "2" {
		t.Fatalf("expected consecutive_empty_runs to be 2, got %q", c2)
	}

	// Run 3: 0 messages published (threshold = 3)
	monitorEmptyRuns(mock, cfg, m, 0)
	c3, _ := mock.GetState("consecutive_empty_runs")
	if c3 != "3" {
		t.Fatalf("expected consecutive_empty_runs to be 3, got %q", c3)
	}

	// Run 4: published 1 message
	m.IncrementTelegramMessagesSent()
	monitorEmptyRuns(mock, cfg, m, 0) // before was 0, now after is 1
	c4, _ := mock.GetState("consecutive_empty_runs")
	if c4 != "0" {
		t.Fatalf("expected consecutive_empty_runs to reset to 0 after publishing, got %q", c4)
	}
}

func TestFileCacheAdapter_AITranslationAndState(t *testing.T) {
	fc := storage.NewFileCache("test_cache.json", 48)
	adapter := &FileCacheAdapter{cache: fc}

	// Test State
	_ = adapter.SetState("test_key", "42")
	val, err := adapter.GetState("test_key")
	if err != nil || val != "42" {
		t.Fatalf("expected state value '42', got %q (err: %v)", val, err)
	}

	// Test AI Translation Cache
	testHash := "hash123"
	testJSON := `{"summary":"Test summary in Ukrainian","danish":"Dansk tekst"}`
	_ = adapter.SaveCachedAITranslation(testHash, "https://example.com", "Title", testJSON)

	cached, err := adapter.GetCachedAITranslation(testHash)
	if err != nil || cached != testJSON {
		t.Fatalf("expected cached JSON %q, got %q (err: %v)", testJSON, cached, err)
	}
}
