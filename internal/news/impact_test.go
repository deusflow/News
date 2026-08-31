package news

import (
	"context"
	"testing"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/rss"
	"github.com/mmcdole/gofeed"
)

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

func TestPassesAudienceRelevanceGate_Bypass(t *testing.T) {
	// General/society news item with AudienceScore >= 5 passes the national news bypass.
	newsItemPass := News{
		Title:             "Stor national infrastrukturplan vedtaget",
		Category:          "society",
		AudienceScore:     6,
		HasDenmarkContext: true,
	}

	if !PassesAudienceRelevanceGate(newsItemPass) {
		t.Error("expected general news with AudienceScore=6 to pass the gate")
	}

	// High-impact crime (AudienceScore >= 9) passes the gate.
	crimeTopPass := News{
		Title:             "National krisesituation",
		Category:          "crime",
		AudienceScore:     9,
		HasDenmarkContext: true,
	}

	if !PassesAudienceRelevanceGate(crimeTopPass) {
		t.Error("expected top-tier crime news with AudienceScore=9 to pass the gate")
	}

	// Routine/isolated crime with AudienceScore=6 (below 9 and no policy context) should be blocked.
	crimeRoutineBlock := News{
		Title:             "Skuddrab på Selinevej",
		Category:          "crime",
		AudienceScore:     6,
		HasDenmarkContext: true,
	}

	if PassesAudienceRelevanceGate(crimeRoutineBlock) {
		t.Error("expected routine crime news with AudienceScore=6 (no policy context) to be blocked by the gate")
	}

	// Crime news with AudienceScore=3 should also be blocked.
	newsItemBlock := News{
		Title:             "Lille tyveri i supermarked",
		Category:          "crime",
		AudienceScore:     3,
		HasDenmarkContext: true,
	}

	if PassesAudienceRelevanceGate(newsItemBlock) {
		t.Error("expected crime news with AudienceScore=3 to be blocked by the gate")
	}
}

func TestApplyCrossSourceBoost(t *testing.T) {
	// Mock preScored items. Two items should have highly similar titles and different sources.
	items := []preScored{
		{
			item: &rss.FeedItem{
				Item: &gofeed.Item{Title: "Politiet efterforsker skuddrab på Amager"},
				Source: &rss.FeedSource{Name: "DR Nyheder"},
			},
			kwScore: 10,
		},
		{
			item: &rss.FeedItem{
				Item: &gofeed.Item{Title: "Efterforskning i gang efter skuddrab på Amager"},
				Source: &rss.FeedSource{Name: "Berlingske"},
			},
			kwScore: 10,
		},
		{
			item: &rss.FeedItem{
				Item: &gofeed.Item{Title: "Helt urelateret nyhed om vejret i Jylland"},
				Source: &rss.FeedSource{Name: "TV Midtvest"},
			},
			kwScore: 10,
		},
	}

	// Run applyCrossSourceBoost.
	// Since items[0] and items[1] have a very similar title, they should receive a boost of +15.
	// items[2] should remain unchanged.
	applyCrossSourceBoost(items)

	if items[0].kwScore != 25 {
		t.Errorf("expected items[0] score to be boosted to 25, got %d", items[0].kwScore)
	}
	if items[1].kwScore != 25 {
		t.Errorf("expected items[1] score to be boosted to 25, got %d", items[1].kwScore)
	}
	if items[2].kwScore != 10 {
		t.Errorf("expected items[2] score to remain 10, got %d", items[2].kwScore)
	}
}

type MockTriageAI struct {
	ResponseText string
}

func (m *MockTriageAI) Name() string { return "mock-triage-ai" }
func (m *MockTriageAI) Close() {}
func (m *MockTriageAI) Generate(ctx context.Context, title, content, systemPrompt, userPrompt string) (*ai.Response, error) {
	return &ai.Response{
		Summary: m.ResponseText,
	}, nil
}
func (m *MockTriageAI) GenerateRaw(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return m.ResponseText, nil
}

func TestRunTriage(t *testing.T) {
	rejected := []triageHeadline{
		{Index: 0, Title: "Politiet undersøger skuddrab på Amager", Source: "DR Nyheder"},
		{Index: 1, Title: "Urelateret vejrudsigt for i morgen", Source: "TV Midtvest"},
		{Index: 2, Title: "Stor demonstration på Nørrebro mod nye regler", Source: "Berlingske"},
	}

	mockAI := &MockTriageAI{
		ResponseText: `{"selected": [0, 2]}`,
	}

	rescued := runTriage(context.Background(), rejected, mockAI)

	if len(rescued) != 2 {
		t.Fatalf("expected 2 rescued candidates, got %d", len(rescued))
	}
	if rescued[0] != 0 {
		t.Errorf("expected first rescued index to be 0, got %d", rescued[0])
	}
	if rescued[1] != 2 {
		t.Errorf("expected second rescued index to be 2, got %d", rescued[1])
	}
}


