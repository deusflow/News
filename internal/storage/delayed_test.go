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
}
