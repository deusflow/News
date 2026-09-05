package storage

import (
	"testing"
	"time"
)

func TestFileCache_CheckSemanticDuplicate_OR_Logic(t *testing.T) {
	fc := NewFileCache("test_sent.json", 168)

	// Add an existing sent story
	// Simulating: "Українські чоловіки 23-59 років у Данії мають підтвердити військовий статус для посвідки"
	fc.MarkAsSentWithSemanticData(
		"hash1",
		"Ukrainske mænd skal have militærpapirer",
		"https://tv2.dk/story1",
		"immigrants",
		"TV2",
		"Українські чоловіки 23-59 років у Данії мають підтвердити військовий статус для посвідки",
		"ua-men-military-status-permit",
		[]float32{1.0, 0.0, 0.0, 0.0},
	)

	// Test Case 1: Tier 1 matches (cluster_key overlap >= 0.60), Tier 2 embedding is empty
	t.Run("Tier 1 cluster_key match triggers duplicate independently", func(t *testing.T) {
		res, err := fc.CheckSemanticDuplicate(
			"ua-military-status-permit-men", // 100% token overlap
			nil,                            // no embedding
			"Новий закон для чоловіків",
			7*24*time.Hour,
			0.60,
			0.85,
			false, // active mode
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.IsDuplicate {
			t.Errorf("expected IsDuplicate=true for Tier 1 match, got false")
		}
		if res.Trigger != "tier1_cluster_key" {
			t.Errorf("expected Trigger=tier1_cluster_key, got %s", res.Trigger)
		}
	})

	// Test Case 2: Tier 1 does NOT match (different cluster key), but Tier 2 embedding matches (cosine >= 0.85)
	t.Run("Tier 2 embedding match triggers duplicate independently when shadowMode=false", func(t *testing.T) {
		res, err := fc.CheckSemanticDuplicate(
			"copenhagen-metro-night-closure", // completely different cluster key!
			[]float32{0.99, 0.01, 0.0, 0.0},   // cosine sim > 0.99 with existing vector
			"Данія вимагає документи у біженців",
			7*24*time.Hour,
			0.60,
			0.85,
			false, // active mode
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.IsDuplicate {
			t.Errorf("expected IsDuplicate=true when Tier 2 fires in active mode, got false")
		}
		if res.Trigger != "tier2_embedding" {
			t.Errorf("expected Trigger=tier2_embedding, got %s", res.Trigger)
		}
		if !res.WouldReject {
			t.Errorf("expected WouldReject=true, got false")
		}
	})

	// Test Case 3: Shadow Mode behavior:
	// When Tier 2 fires in shadow mode, WouldReject=true, but IsDuplicate=false (publication allowed for observation)
	t.Run("Shadow Mode: Tier 2 embedding logs WouldReject but does not drop publication", func(t *testing.T) {
		res, err := fc.CheckSemanticDuplicate(
			"copenhagen-metro-night-closure", // different key
			[]float32{0.99, 0.01, 0.0, 0.0},   // high cosine similarity
			"Данія вимагає документи у біженців",
			7*24*time.Hour,
			0.60,
			0.85,
			true, // shadow mode active!
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.WouldReject {
			t.Errorf("expected WouldReject=true in shadow mode, got false")
		}
		if res.IsDuplicate {
			t.Errorf("expected IsDuplicate=false in shadow mode for Tier 2 match, got true")
		}
		if res.Trigger != "tier2_embedding" {
			t.Errorf("expected Trigger=tier2_embedding, got %s", res.Trigger)
		}
	})

	// Test Case 4: Shadow Mode with Tier 1 match:
	// Tier 1 is always enforced even in shadow mode!
	t.Run("Shadow Mode: Tier 1 cluster key is always enforced", func(t *testing.T) {
		res, err := fc.CheckSemanticDuplicate(
			"ua-men-military-status-permit",
			[]float32{0.1, 0.9, 0.0, 0.0}, // low cosine sim
			"Новий закон для чоловіків",
			7*24*time.Hour,
			0.60,
			0.85,
			true, // shadow mode
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.IsDuplicate {
			t.Errorf("expected IsDuplicate=true because Tier 1 fired in shadow mode, got false")
		}
	})

	// Test Case 5: Neither Tier 1 nor Tier 2 match (completely fresh story)
	t.Run("Completely new story passes both tiers", func(t *testing.T) {
		res, err := fc.CheckSemanticDuplicate(
			"green-energy-wind-park-investment",
			[]float32{0.0, 1.0, 0.0, 0.0}, // orthogonal vector
			"Данія відкриває новий вітропарк",
			7*24*time.Hour,
			0.60,
			0.85,
			false,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.IsDuplicate {
			t.Errorf("expected IsDuplicate=false for new story, got true")
		}
		if res.WouldReject {
			t.Errorf("expected WouldReject=false, got true")
		}
		if res.Trigger != "none" {
			t.Errorf("expected Trigger=none, got %s", res.Trigger)
		}
	})
}
