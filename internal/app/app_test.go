package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/news"
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
	fc := storage.NewFileCache(t.TempDir()+"/test_cache.json", 48)
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

func TestIsCopenhagenQuietHours(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		t.Skip("Europe/Copenhagen timezone not available")
	}

	t.Setenv("ALLOW_NIGHT_PUBLISH", "")

	// 23:00 Copenhagen time -> Quiet hours (true)
	tNight := time.Date(2026, 11, 12, 23, 0, 0, 0, loc)
	if !IsCopenhagenQuietHours(tNight) {
		t.Errorf("expected 23:00 to be quiet hours")
	}

	// 02:40 Copenhagen time -> Quiet hours (true)
	tLateNight := time.Date(2026, 11, 12, 2, 40, 0, 0, loc)
	if !IsCopenhagenQuietHours(tLateNight) {
		t.Errorf("expected 02:40 to be quiet hours")
	}

	// 07:15 Copenhagen time -> Quiet hours (true)
	tEarlyMorning := time.Date(2026, 11, 12, 7, 15, 0, 0, loc)
	if !IsCopenhagenQuietHours(tEarlyMorning) {
		t.Errorf("expected 07:15 to be quiet hours")
	}

	// 08:00 Copenhagen time -> Active hours (false)
	tMorning := time.Date(2026, 11, 12, 8, 0, 0, 0, loc)
	if IsCopenhagenQuietHours(tMorning) {
		t.Errorf("expected 08:00 to NOT be quiet hours")
	}

	// 13:00 Copenhagen time -> Active hours (false)
	tAfternoon := time.Date(2026, 11, 12, 13, 0, 0, 0, loc)
	if IsCopenhagenQuietHours(tAfternoon) {
		t.Errorf("expected 13:00 to NOT be quiet hours")
	}

	// Verify override via ALLOW_NIGHT_PUBLISH=true
	t.Setenv("ALLOW_NIGHT_PUBLISH", "true")
	if IsCopenhagenQuietHours(tLateNight) {
		t.Errorf("expected ALLOW_NIGHT_PUBLISH=true to bypass quiet hours")
	}
}

func TestFileCacheAdapter_GetRecentStoriesAndSaveMsgID(t *testing.T) {
	fc := storage.NewFileCache(t.TempDir()+"/test_stories_cache.json", 48)
	adapter := &FileCacheAdapter{cache: fc}

	// Mark an item as sent with semantic data
	fc.MarkAsSentWithSemanticData("hash1", "Title 1", "https://example.com/1", "politics", "DR", "Заголовок 1", "cluster-key-1", nil)

	// Save Telegram message ID
	if err := adapter.SaveTelegramMessageID("hash1", 9999); err != nil {
		t.Fatalf("SaveTelegramMessageID failed: %v", err)
	}

	// Fetch recent stories
	stories, err := adapter.GetRecentStories(24 * time.Hour)
	if err != nil {
		t.Fatalf("GetRecentStories failed: %v", err)
	}
	if len(stories) != 1 {
		t.Fatalf("expected 1 story, got %d", len(stories))
	}
	if stories[0].TelegramMessageID != 9999 {
		t.Fatalf("expected TelegramMessageID 9999, got %d", stories[0].TelegramMessageID)
	}
	if stories[0].Category != "politics" {
		t.Fatalf("expected Category 'politics', got %s", stories[0].Category)
	}
}

func TestCategoryAntiFatigue_ScoreAdjustment(t *testing.T) {
	fc := storage.NewFileCache(t.TempDir()+"/fatigue_cache.json", 48)
	adapter := &FileCacheAdapter{cache: fc}

	// 1. Prior story was politics
	fc.MarkAsSentWithSemanticData("hash_prev", "Previous Politics Story", "https://example.com/p", "politics", "DR", "Політика", "danish-politics", nil)

	recentStories, err := adapter.GetRecentStories(24 * time.Hour)
	if err != nil || len(recentStories) == 0 {
		t.Fatalf("failed to get recent stories: %v", err)
	}
	lastCategory := news.ValidateCategory(recentStories[0].Category)
	if lastCategory != news.CategoryPolitics {
		t.Fatalf("expected lastCategory to be politics, got %s", lastCategory)
	}

	// Candidate A: Politics (initial score 80) -> penalized by -15 to 65
	candPolitics := news.News{
		Title:    "Another Politics Candidate",
		Category: string(news.CategoryPolitics),
		Score:    80,
	}
	// Candidate B: Education/Family (initial score 75) -> unaffected, remains 75
	candEducation := news.News{
		Title:    "School Reform Candidate",
		Category: string(news.CategoryEducation),
		Score:    75,
	}

	candidates := []news.News{candPolitics, candEducation}
	for i := range candidates {
		ApplyCategoryAntiFatigue(&candidates[i], lastCategory)
	}

	if candidates[0].Score != 65 {
		t.Errorf("expected politics candidate score to be penalized to 65, got %d", candidates[0].Score)
	}
	if candidates[1].Score != 75 {
		t.Errorf("expected education candidate score to remain 75, got %d", candidates[1].Score)
	}
	// Education candidate now outranks politics candidate!
	if candidates[1].Score <= candidates[0].Score {
		t.Errorf("expected education candidate to outrank politics candidate due to anti-fatigue")
	}
}

func TestDevelopingStory_Lifecycle(t *testing.T) {
	clusterThreshold := 0.5
	cosineThreshold := 0.85

	morningRecord := storage.SentStoryRecord{
		Hash:              "hash_morning",
		Title:             "Zelensky ankommer til Danmark",
		StoryClusterKey:   "zelensky-visit-danmark-aftale",
		TelegramMessageID: 1001,
		SentAt:            time.Now().Add(-6 * time.Hour),
		Category:          "politics",
	}

	recentStories := []storage.SentStoryRecord{morningRecord}

	// 1. Candidate 2 (Evening update): Concrete outcome anchor -> Approved with reply_to = 1001
	candEveningValid := news.News{
		Title:           "Danmark og Ukraine indgår aftale om 1.5 mia. kr",
		StoryClusterKey: "zelensky-visit-danmark-aftale",
		ConcreteAnchor:  "1.5 milliarder kroner til forsvarsindustri",
		AudienceScore:   9,
	}

	approved, replyToID, reason := EvaluateDevelopingStory(candEveningValid, recentStories, clusterThreshold, cosineThreshold, false)
	if !approved {
		t.Fatalf("expected evening valid candidate to be approved as developing update, got reason: %s", reason)
	}
	if replyToID != 1001 {
		t.Fatalf("expected replyToID to be 1001, got %d", replyToID)
	}

	// 2. Candidate 2 (Evening without substance): "insufficient_substance" -> Rejected
	candEveningStub := news.News{
		Title:           "Mødet fortsætter bag lukkede døre",
		StoryClusterKey: "zelensky-visit-danmark-aftale",
		ConcreteAnchor:  "insufficient_substance",
		AudienceScore:   5,
	}
	approvedStub, _, reasonStub := EvaluateDevelopingStory(candEveningStub, recentStories, clusterThreshold, cosineThreshold, false)
	if approvedStub {
		t.Fatalf("expected stub update without substance to be rejected")
	}
	if reasonStub == "" {
		t.Fatalf("expected reason for rejecting stub candidate")
	}

	// 3. Candidate 3 (Night after 2 posts already sent today) -> Rejected due to daily limit (>= 2)
	eveningRecord := storage.SentStoryRecord{
		Hash:              "hash_evening",
		Title:             candEveningValid.Title,
		StoryClusterKey:   candEveningValid.StoryClusterKey,
		TelegramMessageID: 1002,
		SentAt:            time.Now().Add(-1 * time.Hour),
		Category:          "politics",
	}
	twoStoriesToday := []storage.SentStoryRecord{eveningRecord, morningRecord}
	approvedThird, _, reasonThird := EvaluateDevelopingStory(candEveningValid, twoStoriesToday, clusterThreshold, cosineThreshold, false)
	if approvedThird {
		t.Fatalf("expected third post of the day for same cluster to be blocked")
	}
	if reasonThird != "daily cluster limit reached (>= 2 posts today)" {
		t.Fatalf("expected daily limit reason, got %s", reasonThird)
	}
}
