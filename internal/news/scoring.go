package news

import (
	"strings"
	"github.com/deusflow/News/internal/logger"
)

const (
	impactPriorityThreshold = 12

	// Impact score magic numbers
	impactPenaltyPureIncidentCap = 10
	impactBoostPolicyLocalCap    = 15
	impactPenaltyIsolatedCrime   = 8
	impactBoostCrimeContext      = 6
	impactBoostScaleCap          = 12
	impactMaxCap                 = 40
	impactMinCap                 = -10

	// Editorial signal magic numbers
	editorialBoostCoreCap       = 20
	editorialPenaltySoftOnlyCap = 10
	editorialPenaltySoftLowCore = 6
	editorialBoostPolicyCap     = 12
)

// calculateImpactScore derives an explicit public-impact signal from keyword
// category contributions. It is intentionally independent from AI.
func calculateImpactScore(categoryWeights map[string]int) int {
	if len(categoryWeights) == 0 {
		return 0
	}

	impact := 0
	impact += categoryWeights["politics"]
	impact += categoryWeights["society"]
	impact += categoryWeights["work"]
	impact += categoryWeights["eu"]
	impact += categoryWeights["economy"] / 2
	impact += categoryWeights["visas"] / 2
	impact += categoryWeights["money"] / 2
	impact += categoryWeights["housing"] / 2
	impact += categoryWeights["health"] / 2
	impact += categoryWeights["transport"] / 3
	impact += categoryWeights["local"] / 3

	// Entertainment-only items get a small penalty when no public-impact signal exists.
	if impact == 0 {
		light := categoryWeights["lifestyle"] + categoryWeights["sport"]
		if light > 0 {
			impact -= min(6, light/3)
		}
	}

	// Contextual filter: limit raw local/transport incidents without structural framing.
	structural := categoryWeights["politics"] + categoryWeights["work"] + categoryWeights["money"] + categoryWeights["visas"] + categoryWeights["economy"] + categoryWeights["education"] + categoryWeights["eu"]
	incidentScore := categoryWeights["transport"] + categoryWeights["local"]
	if structural < 5 && incidentScore > 10 {
		impact -= min(impactPenaltyPureIncidentCap, incidentScore/2) // Penalty for pure traffic/local incident
	} else if categoryWeights["local"] > 0 {
		// local-with-policy synergy: local news + strong policy/money/housing context
		policyWeights := categoryWeights["work"] + categoryWeights["money"] + categoryWeights["visas"] + categoryWeights["housing"] + categoryWeights["society"]
		if policyWeights >= 5 {
			impact += min(impactBoostPolicyLocalCap, policyWeights) // solid boost for local law/money/society news
		}
	}

	crimeScore := categoryWeights["crime"]
	if crimeScore > 0 {
		if structural < 5 {
			impact -= min(impactPenaltyIsolatedCrime, crimeScore/3) // Penalize isolated crime without structural context
		} else {
			impact += min(impactBoostCrimeContext, crimeScore/4) // Allow limited boost when crime has broader context
		}
	}

	scaleBoost := categoryWeights["politics"] + categoryWeights["economy"] + categoryWeights["eu"]
	if scaleBoost > 0 {
		impact += min(impactBoostScaleCap, scaleBoost/4)
	}

	if impact > impactMaxCap {
		impact = impactMaxCap
	}
	if impact < impactMinCap {
		impact = impactMinCap
	}
	return impact
}

func calculateEditorialSignals(categoryWeights map[string]int) (coreImpact int, softScore int, adjustment int) {
	if len(categoryWeights) == 0 {
		return 0, 0, 0
	}

	coreImpact += categoryWeights["politics"]
	coreImpact += categoryWeights["society"]
	coreImpact += categoryWeights["work"]
	coreImpact += categoryWeights["economy"]
	coreImpact += categoryWeights["visas"]
	coreImpact += categoryWeights["money"]
	coreImpact += categoryWeights["education"]
	coreImpact += categoryWeights["health"]
	coreImpact += categoryWeights["housing"]
	coreImpact += categoryWeights["eu"]

	// Local and transport contribute less directly to structural CoreImpact unless accompanied by context.
	incidentImpact := categoryWeights["local"] + categoryWeights["transport"]
	if coreImpact >= 5 {
		// Structural context exists broadly, allow incident words to add flavor.
		coreImpact += incidentImpact / 2
	} else {
		// Barely structural, heavily local.
		coreImpact += incidentImpact / 4
	}

	softScore += categoryWeights["lifestyle"]
	softScore += categoryWeights["sport"]
	softScore += categoryWeights["family"]

	coreBoost := min(editorialBoostCoreCap, coreImpact/4)
	softPenalty := 0
	if coreImpact == 0 && softScore > 0 {
		softPenalty = min(editorialPenaltySoftOnlyCap, softScore/2)
	} else if coreImpact < 10 && softScore > 0 {
		softPenalty = min(editorialPenaltySoftLowCore, softScore/3)
	}

	adjustment = coreBoost - softPenalty

	policyFocus := categoryWeights["visas"] + categoryWeights["work"] + categoryWeights["money"] + categoryWeights["housing"] + categoryWeights["society"]
	if policyFocus > 0 {
		adjustment += min(editorialBoostPolicyCap, policyFocus/4)
	}
	return coreImpact, softScore, adjustment
}

func applyCrossSourceBoost(scored []preScored) {
	if len(scored) < 2 {
		return
	}

	const similarityThreshold = 0.30
	const boostPerSource = 15
	const maxBoost = 45

	type bigramSet map[string]struct{}
	bigrams := make([]bigramSet, len(scored))
	for i, s := range scored {
		bigrams[i] = titleBigrams(s.item.Title)
	}

	// For each item, count how many OTHER sources cover the same story.
	for i := range scored {
		extraSources := 0
		seenSources := map[string]bool{scored[i].item.Source.Name: true}

		for j := range scored {
			if i == j {
				continue
			}
			// Same source doesn't count as cross-source
			if seenSources[scored[j].item.Source.Name] {
				continue
			}

			sim := jaccardBigrams(bigrams[i], bigrams[j])
			if sim >= similarityThreshold {
				extraSources++
				seenSources[scored[j].item.Source.Name] = true
			}
		}

		if extraSources > 0 {
			boost := extraSources * boostPerSource
			if boost > maxBoost {
				boost = maxBoost
			}
			scored[i].kwScore += boost
			logger.Info("cross-source boost applied",
				"title", scored[i].item.Title,
				"source", scored[i].item.Source.Name,
				"extra_sources", extraSources,
				"boost", boost,
				"new_kw_score", scored[i].kwScore)
		}
	}
}

// titleBigrams extracts character bigrams from a lowercased title.
func titleBigrams(title string) map[string]struct{} {
	lower := strings.ToLower(title)
	runes := []rune(lower)
	set := make(map[string]struct{}, len(runes))
	for i := 0; i+1 < len(runes); i++ {
		bg := string(runes[i : i+2])
		set[bg] = struct{}{}
	}
	return set
}

// jaccardBigrams computes Jaccard similarity between two bigram sets.
func jaccardBigrams(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for bg := range a {
		if _, ok := b[bg]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
