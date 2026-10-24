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

func TestPostgresLive_TryReservePublish_ConcurrencyAndStaleReclaim(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("Skipping live Postgres test: TEST_DATABASE_URL not set")
	}

	cache, err := NewPostgresCache(dbURL, 24)
	if err != nil {
		t.Fatalf("Failed to initialize PostgresCache: %v", err)
	}
	defer cache.Close()

	testHash := "hash_concurrency_race"
	_, _ = cache.db.Exec("DELETE FROM sent_news WHERE hash IN ($1, $2);", testHash, "hash_cancel_test")

	// 1. Concurrency test: 10 parallel goroutines competing for the exact same hash
	const workers = 10
	results := make(chan bool, workers)
	startBarrier := make(chan struct{})

	for i := 0; i < workers; i++ {
		go func(workerID int) {
			<-startBarrier
			reserved, resErr := cache.TryReservePublish(testHash, "Race Title", "https://example.com/race")
			if resErr != nil {
				t.Errorf("Worker %d got error: %v", workerID, resErr)
				results <- false
				return
			}
			results <- reserved
		}(i)
	}

	close(startBarrier)

	wins := 0
	losses := 0
	for i := 0; i < workers; i++ {
		if <-results {
			wins++
		} else {
			losses++
		}
	}

	if wins != 1 || losses != workers-1 {
		t.Fatalf("Expected exactly 1 winner and %d losers, got %d wins and %d losses", workers-1, wins, losses)
	}

	// 2. Active lock test: immediately trying again within 15 min window must fail
	activeReserved, err := cache.TryReservePublish(testHash, "Race Title", "https://example.com/race")
	if err != nil {
		t.Fatalf("TryReservePublish error: %v", err)
	}
	if activeReserved {
		t.Fatalf("Expected active reservation to reject, but it succeeded")
	}

	// 3. Stale reservation test: age the reservation past 15 minutes
	_, err = cache.db.Exec(`
		UPDATE sent_news 
		SET reserved_at = (NOW() AT TIME ZONE 'UTC') - INTERVAL '20 minutes'
		WHERE hash = $1;
	`, testHash)
	if err != nil {
		t.Fatalf("Failed to age reservation: %v", err)
	}

	// Reclaim should now succeed
	reclaimed, err := cache.TryReservePublish(testHash, "Race Title", "https://example.com/race")
	if err != nil {
		t.Fatalf("Failed to reclaim stale reservation: %v", err)
	}
	if !reclaimed {
		t.Fatalf("Expected stale reservation (>15m) to be reclaimed, but got false")
	}

	// 4. Finalized post protection: once MarkAsSentWithSemanticData is called, content_hash is non-null
	err = cache.MarkAsSentWithSemanticData(testHash, "Final Title", "https://example.com/race", "Detailed news content", "tech", "DR", "Укр Заголовок", "cluster123", nil)
	if err != nil {
		t.Fatalf("MarkAsSentWithSemanticData failed: %v", err)
	}

	// Even if reserved_at is aged again, finalized post must NEVER be reclaimed
	_, err = cache.db.Exec(`
		UPDATE sent_news 
		SET reserved_at = (NOW() AT TIME ZONE 'UTC') - INTERVAL '30 minutes'
		WHERE hash = $1;
	`, testHash)
	if err != nil {
		t.Fatalf("Failed to age reservation: %v", err)
	}

	finalizedReserved, err := cache.TryReservePublish(testHash, "Race Title", "https://example.com/race")
	if err != nil {
		t.Fatalf("TryReservePublish error on finalized: %v", err)
	}
	if finalizedReserved {
		t.Fatalf("CRITICAL BUG: Finalized post was overwritten by TryReservePublish!")
	}

	// 5. Cancel reservation test:
	cancelHash := "hash_cancel_test"
	res1, err := cache.TryReservePublish(cancelHash, "Cancel Title", "https://example.com/cancel")
	if err != nil || !res1 {
		t.Fatalf("Initial reservation failed: res=%v, err=%v", res1, err)
	}

	if err := cache.CancelPublishReservation(cancelHash); err != nil {
		t.Fatalf("CancelPublishReservation failed: %v", err)
	}

	// After cancellation, a new reservation must succeed
	res2, err := cache.TryReservePublish(cancelHash, "Cancel Title", "https://example.com/cancel")
	if err != nil || !res2 {
		t.Fatalf("Re-reservation after cancel failed: res=%v, err=%v", res2, err)
	}
}

