package storage

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestPostgresLive_SchemaAndDelayedPosts(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("Skipping live Postgres test: TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	cache, err := NewPostgresCache(dbURL, 24)
	if err != nil {
		t.Fatalf("Failed to initialize PostgresCache: %v", err)
	}
	defer cache.Close()

	// Clean up delayed_posts table before test
	_, err = cache.db.Exec("DELETE FROM delayed_posts;")
	if err != nil {
		t.Fatalf("Failed to clean delayed_posts: %v", err)
	}

	now := time.Now().UTC()

	// 1. Insert test posts
	// Post 1: Normal ready pending post
	_, err = cache.db.Exec(`
		INSERT INTO delayed_posts (hash, title, link, news_json, publish_after, status, claimed_at)
		VALUES ($1, $2, $3, $4, $5, 'pending', NULL)
	`, "hash_pending_ready", "Pending Ready", "https://example.com/1", "{}", now.Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("Insert post 1 failed: %v", err)
	}

	// Post 2: Legacy stuck processing post with NULL claimed_at
	_, err = cache.db.Exec(`
		INSERT INTO delayed_posts (hash, title, link, news_json, publish_after, status, claimed_at)
		VALUES ($1, $2, $3, $4, $5, 'processing', NULL)
	`, "hash_stuck_null_claimed", "Stuck Null Claimed", "https://example.com/2", "{}", now.Add(-40*time.Minute))
	if err != nil {
		t.Fatalf("Insert post 2 failed: %v", err)
	}

	// Post 3: Stuck post with claimed_at 35 minutes ago
	_, err = cache.db.Exec(`
		INSERT INTO delayed_posts (hash, title, link, news_json, publish_after, status, claimed_at)
		VALUES ($1, $2, $3, $4, $5, 'processing', $6)
	`, "hash_stuck_35m", "Stuck 35m", "https://example.com/3", "{}", now.Add(-40*time.Minute), now.Add(-35*time.Minute))
	if err != nil {
		t.Fatalf("Insert post 3 failed: %v", err)
	}

	// Post 4: Active processing post claimed 5 minutes ago (MUST NOT be claimed)
	_, err = cache.db.Exec(`
		INSERT INTO delayed_posts (hash, title, link, news_json, publish_after, status, claimed_at)
		VALUES ($1, $2, $3, $4, $5, 'processing', $6)
	`, "hash_active_5m", "Active 5m", "https://example.com/4", "{}", now.Add(-10*time.Minute), now.Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("Insert post 4 failed: %v", err)
	}

	// Post 5: Future pending post (MUST NOT be claimed)
	_, err = cache.db.Exec(`
		INSERT INTO delayed_posts (hash, title, link, news_json, publish_after, status, claimed_at)
		VALUES ($1, $2, $3, $4, $5, 'pending', NULL)
	`, "hash_future", "Future Post", "https://example.com/5", "{}", now.Add(1*time.Hour))
	if err != nil {
		t.Fatalf("Insert post 5 failed: %v", err)
	}

	// 2. Query ready delayed posts
	posts, err := cache.GetReadyDelayedPosts(ctx)
	if err != nil {
		t.Fatalf("GetReadyDelayedPosts failed: %v", err)
	}

	if len(posts) != 3 {
		t.Fatalf("Expected exactly 3 claimed posts, got %d", len(posts))
	}

	claimedHashes := make(map[string]bool)
	for _, p := range posts {
		claimedHashes[p.Hash] = true
	}

	if !claimedHashes["hash_pending_ready"] {
		t.Errorf("Expected hash_pending_ready to be claimed")
	}
	if !claimedHashes["hash_stuck_null_claimed"] {
		t.Errorf("Expected hash_stuck_null_claimed (legacy NULL claimed_at) to be claimed")
	}
	if !claimedHashes["hash_stuck_35m"] {
		t.Errorf("Expected hash_stuck_35m to be claimed")
	}
	if claimedHashes["hash_active_5m"] {
		t.Errorf("hash_active_5m was claimed but should NOT have been (active within 30 min)")
	}
	if claimedHashes["hash_future"] {
		t.Errorf("hash_future was claimed but should NOT have been (publish_after in future)")
	}

	// 3. Immediately query again — should return 0 since all are now claimed
	postsSecondRun, err := cache.GetReadyDelayedPosts(ctx)
	if err != nil {
		t.Fatalf("Second GetReadyDelayedPosts failed: %v", err)
	}
	if len(postsSecondRun) != 0 {
		t.Errorf("Expected 0 posts on immediate second run, got %d", len(postsSecondRun))
	}

	// 4. Test MarkDelayedPostSent
	if err := cache.MarkDelayedPostSent(posts[0].ID); err != nil {
		t.Fatalf("MarkDelayedPostSent failed: %v", err)
	}

	var status string
	err = cache.db.QueryRow("SELECT status FROM delayed_posts WHERE id = $1", posts[0].ID).Scan(&status)
	if err != nil || status != "sent" {
		t.Fatalf("Expected status 'sent', got %v (err: %v)", status, err)
	}
}
