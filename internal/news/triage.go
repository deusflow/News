package news

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/rss"
)

// triageHeadline is a lightweight representation for AI triage.
type triageHeadline struct {
	Index int
	Title string
	Source string
}

// triageResponse is what the AI returns: a list of selected headline indices.
type triageResponse struct {
	Selected []int `json:"selected"`
}

// triageSystemPrompt is a minimal prompt for the cheap headline triage call.
const triageSystemPrompt = `You are a news editor for a bilingual Telegram channel targeting Ukrainians living in Denmark.
You receive a numbered list of news headlines.
Your task: pick up to 3 headlines that would be MOST important or interesting for your audience.

Criteria for selection:
- Direct impact on daily life in Denmark (laws, taxes, housing, transport, healthcare)
- Major positive national developments (tax relief/skattelettelser, free public services, major student/family benefits, high job growth, large investments in welfare)
- Major national events (significant crimes, emergencies, political crises)
- Topics relevant to foreigners/immigrants in Denmark
- Surprising or unusual Danish news that provides cultural insight

Do NOT pick: sports results, celebrity gossip, weather, minor local incidents, routine municipal budgets.

Return ONLY valid JSON: {"selected": [0, 5, 12]}
If none are worth selecting, return: {"selected": []}
No explanations, no markdown.`

// runTriage sends a single cheap AI request to evaluate rejected headlines
// and rescue potentially interesting ones that keyword scoring missed.
//
// It returns the indices (into the rejected slice) of headlines the AI selected.
// The caller is responsible for merging them into topCandidates.
func runTriage(ctx context.Context, rejected []triageHeadline, provider ai.Provider) []int {
	if len(rejected) == 0 {
		return nil
	}

	// Build the headline list
	var sb strings.Builder
	sb.WriteString("Headlines:\n")
	for i, h := range rejected {
		fmt.Fprintf(&sb, "%d: [%s] %s\n", i, h.Source, h.Title)
	}

	userPrompt := sb.String()

	rawJSON, err := provider.GenerateRaw(ctx, triageSystemPrompt, userPrompt)
	if err != nil {
		logger.Warn("AI triage failed, skipping rescue", "error", err)
		return nil
	}

	if rawJSON == "" {
		logger.Warn("AI triage returned no parseable JSON")
		return nil
	}

	// Strip markdown fences if any
	rawJSON = strings.ReplaceAll(rawJSON, "```json", "")
	rawJSON = strings.ReplaceAll(rawJSON, "```", "")
	rawJSON = strings.TrimSpace(rawJSON)

	var result triageResponse
	if err := json.Unmarshal([]byte(rawJSON), &result); err != nil {
		logger.Warn("AI triage JSON parse failed", "error", err, "raw", rawJSON)
		return nil
	}

	// Validate indices
	valid := make([]int, 0, len(result.Selected))
	for _, idx := range result.Selected {
		if idx >= 0 && idx < len(rejected) {
			valid = append(valid, idx)
		}
	}

	if len(valid) > 0 {
		titles := make([]string, len(valid))
		for i, idx := range valid {
			titles[i] = rejected[idx].Title
		}
		logger.Info("AI triage rescued candidates",
			"count", len(valid),
			"titles", strings.Join(titles, " | "))
	}

	return valid
}



// collectRejectedHeadlines builds the list of headlines that did NOT pass
// the keyword pre-filter, for AI triage evaluation.
func collectRejectedHeadlines(items []*rss.FeedItem, picked map[string]bool) []triageHeadline {
	var rejected []triageHeadline
	for _, item := range items {
		if picked[item.Link] {
			continue
		}
		rejected = append(rejected, triageHeadline{
			Index:  len(rejected),
			Title:  item.Title,
			Source: item.Source.Name,
		})
	}
	return rejected
}
