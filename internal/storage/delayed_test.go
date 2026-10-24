package storage

import (
	"testing"
	"time"
)

func TestFileCache_DelayedPosts(t *testing.T) {
	cache := NewFileCache("test_delayed.json", 24)

	// 1. Enqueue post with future delay (should NOT be ready immediately)
	err := cache.EnqueueDelayedPost("hash_future", "Future High Impact Story", "https://example.com/future", `{"title":"future"}`, 30*time.Minute)
	if err != nil {
		t.Fatalf("EnqueueDelayedPost failed: %v", err)
	}

	ready, err := cache.GetReadyDelayedPosts()
	if err != nil {
		t.Fatalf("GetReadyDelayedPosts failed: %v", err)
	}
	if len(ready) != 0 {
		t.Errorf("expected 0 ready posts for 30m delay, got %d", len(ready))
	}

	// 2. Enqueue post with negative/zero delay (should be ready immediately)
	err = cache.EnqueueDelayedPost("hash_ready", "Ready High Impact Story", "https://example.com/ready", `{"title":"ready"}`, -1*time.Minute)
	if err != nil {
		t.Fatalf("EnqueueDelayedPost ready failed: %v", err)
	}

	ready, err = cache.GetReadyDelayedPosts()
	if err != nil {
		t.Fatalf("GetReadyDelayedPosts failed: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready post, got %d", len(ready))
	}
	if ready[0].Hash != "hash_ready" {
		t.Errorf("expected hash_ready, got %s", ready[0].Hash)
	}

	// 3. Mark as sent
	err = cache.MarkDelayedPostSent(ready[0].ID)
	if err != nil {
		t.Fatalf("MarkDelayedPostSent failed: %v", err)
	}

	// 4. Verify no longer ready
	readyAfterSent, err := cache.GetReadyDelayedPosts()
	if err != nil {
		t.Fatalf("GetReadyDelayedPosts failed: %v", err)
	}
	if len(readyAfterSent) != 0 {
		t.Errorf("expected 0 ready posts after marking sent, got %d", len(readyAfterSent))
	}

	// 5. Enqueue post and simulate worker crash during processing
	err = cache.EnqueueDelayedPost("hash_crashed", "Crashed Worker Story", "https://example.com/crashed", `{"title":"crashed"}`, -1*time.Minute)
	if err != nil {
		t.Fatalf("Enqueue crashed test failed: %v", err)
	}
	claimed, err := cache.GetReadyDelayedPosts()
	if err != nil || len(claimed) != 1 {
		t.Fatalf("Expected 1 claimed post, got %d (err: %v)", len(claimed), err)
	}
	// Immediately trying again returns 0 because it's processing
	again, err := cache.GetReadyDelayedPosts()
	if err != nil || len(again) != 0 {
		t.Fatalf("Expected 0 posts while in-flight processing, got %d", len(again))
	}
	// Simulate 35 minutes passing with worker dead
	cache.mu.Lock()
	staleClaim := time.Now().Add(-35 * time.Minute)
	cache.delayedPosts[len(cache.delayedPosts)-1].ClaimedAt = &staleClaim
	cache.mu.Unlock()

	// Should now be reclaimed!
	reclaimed, err := cache.GetReadyDelayedPosts()
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("Expected 1 reclaimed post after 35m timeout, got %d (err: %v)", len(reclaimed), err)
	}
	if reclaimed[0].Hash != "hash_crashed" {
		t.Errorf("expected hash_crashed, got %s", reclaimed[0].Hash)
	}
}

func TestFileCache_ReservationAndIsAlreadySent(t *testing.T) {
	cache := NewFileCache("test_res.json", 24)

	hash := "test_file_res"
	// 1. Reserve
	ok, err := cache.TryReservePublish(hash, "Test Title", "https://example.com")
	if err != nil || !ok {
		t.Fatalf("TryReservePublish failed: ok=%v, err=%v", ok, err)
	}

	// Fresh reservation counts as already sent in IsAlreadySent
	if !cache.IsAlreadySent(hash) {
		t.Errorf("IsAlreadySent for fresh reservation want true, got false")
	}

	// Second reservation fails
	ok2, _ := cache.TryReservePublish(hash, "Test Title", "https://example.com")
	if ok2 {
		t.Errorf("Concurrent TryReservePublish should fail, but got true")
	}

	// Age reservation by 20 minutes
	cache.mu.Lock()
	stale := time.Now().Add(-20 * time.Minute)
	item := cache.items[hash]
	item.ReservedAt = &stale
	cache.items[hash] = item
	cache.mu.Unlock()

	// Expired reservation is NOT already sent
	if cache.IsAlreadySent(hash) {
		t.Errorf("IsAlreadySent for expired reservation (>15m) want false, got true")
	}

	// Expired reservation can be reclaimed
	reclaimed, err := cache.TryReservePublish(hash, "Test Title", "https://example.com")
	if err != nil || !reclaimed {
		t.Errorf("Expected stale reservation to be reclaimed, got %v", reclaimed)
	}

	// Legacy row with ReservedAt == nil cannot be reclaimed
	legacyHash := "test_legacy_nil"
	cache.mu.Lock()
	cache.items[legacyHash] = SentNewsItem{
		Hash:       legacyHash,
		Title:      "Legacy Title",
		Link:       "https://example.com/legacy",
		SentAt:     time.Now().Add(-72 * time.Hour),
		ReservedAt: nil,
	}
	cache.mu.Unlock()

	legacyReclaim, _ := cache.TryReservePublish(legacyHash, "Legacy", "https://example.com")
	if legacyReclaim {
		t.Errorf("Legacy row with nil ReservedAt should NOT be reclaimed")
	}
}

