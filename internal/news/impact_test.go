package news

import "testing"

func TestCalculateImpactScore_PoliticsDominates(t *testing.T) {
	weights := map[string]int{
		"politics":  22,
		"lifestyle": 8,
	}
	got := calculateImpactScore(weights)
	if got < impactPriorityThreshold {
		t.Fatalf("expected impact >= threshold (%d), got %d", impactPriorityThreshold, got)
	}
}

func TestCalculateImpactScore_EntertainmentOnlyPenalty(t *testing.T) {
	weights := map[string]int{
		"lifestyle": 12,
		"sport":     5,
	}
	got := calculateImpactScore(weights)
	if got >= 0 {
		t.Fatalf("expected negative impact for entertainment-only content, got %d", got)
	}
}

func TestCalculateImpactScore_WorkAndSocietyCrossesThreshold(t *testing.T) {
	weights := map[string]int{
		"society": 16,
		"work":    14,
	}
	got := calculateImpactScore(weights)
	if got < impactPriorityThreshold {
		t.Fatalf("expected work+society impact >= threshold (%d), got %d", impactPriorityThreshold, got)
	}
}

func TestSortByPublishPriority_ImpactFirst(t *testing.T) {
	items := []News{
		{Title: "high-score-light", Score: 85, ImpactScore: 0},
		{Title: "public-impact", Score: 70, ImpactScore: 20},
	}

	sortByPublishPriority(items)

	if items[0].Title != "public-impact" {
		t.Fatalf("expected impact candidate first, got %q", items[0].Title)
	}
}

func TestSortByPublishPriority_ImpactGroupOrdersByImpactThenScore(t *testing.T) {
	items := []News{
		{Title: "impact-weak", Score: 95, ImpactScore: 14},
		{Title: "impact-strong", Score: 70, ImpactScore: 25},
		{Title: "normal", Score: 99, ImpactScore: 0},
	}

	sortByPublishPriority(items)

	if items[0].Title != "impact-strong" {
		t.Fatalf("expected strongest impact first, got %q", items[0].Title)
	}
	if items[1].Title != "impact-weak" {
		t.Fatalf("expected second impact candidate next, got %q", items[1].Title)
	}
	if items[2].Title != "normal" {
		t.Fatalf("expected non-impact item last, got %q", items[2].Title)
	}
}
