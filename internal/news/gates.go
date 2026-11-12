package news

import (
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
)

const (
	// Gate thresholds
	gateCoreImpactMin         = 10
	gateKeywordScoreMin       = 12
	gateAudienceScoreMinValid = 4 // 1-3 is invalid
	gateAudienceScoreBypass   = 5 // 5+ bypasses structural checks
)

func isImpactCandidate(n News) bool {
	return n.ImpactScore >= impactPriorityThreshold
}

func isCoreImpactCategory(c Category) bool {
	switch c {
	case CategoryPolitics, CategorySociety, CategoryWork, CategoryEconomy, CategoryVisas, CategoryMoney, CategoryEducation, CategoryEU, CategoryWar, CategoryFamily:
		return true
	default:
		return false
	}
}

// PassesPublicImpactGate marks candidates that should be prioritized for publication.
func PassesPublicImpactGate(n News) bool {
	if strings.EqualFold(strings.TrimSpace(n.ConcreteAnchor), "insufficient_substance") {
		return false
	}
	if n.AudienceScore > 0 && n.AudienceScore < gateAudienceScoreMinValid {
		return false
	}
	if n.CoreImpactScore >= gateCoreImpactMin {
		return true
	}
	if n.ImpactScore >= impactPriorityThreshold {
		return true
	}
	cat := ValidateCategory(n.Category)
	return isCoreImpactCategory(cat) && n.KeywordScore >= gateKeywordScoreMin
}

// PassesAudienceRelevanceGate is a strict filter ensuring we do not publish empty noise.
// It allows structural/country-wide news, good lifestyle stories, but drops bare local incidents.
func PassesAudienceRelevanceGate(n News) bool {
	// Immediate rejection for stubs or paywalls
	if strings.EqualFold(strings.TrimSpace(n.ConcreteAnchor), "insufficient_substance") {
		return false
	}

	// If AI evaluated the news and gave it a low relevance score for Ukrainians, block it immediately
	// 1-3 scores correspond to weak/irrelevant connection.
	if n.AudienceScore > 0 && n.AudienceScore < gateAudienceScoreMinValid {
		return false
	}

	// High-profile national news bypass: AI scored this as important (5+).
	// Scores 5-6 = "High-profile national news, significant emergencies".
	// This prevents blocking major crimes, disasters, and emergencies
	// that don't have explicit policy/structural keywords.
	if n.AudienceScore >= gateAudienceScoreBypass {
		cat := ValidateCategory(n.Category)
		// Crime needs either structural signal or a really top score (9+, Very High Impact)
		if cat != CategoryCrime || n.AudienceScore >= 9 {
			return true
		}
	}

	if !n.HasDenmarkContext && !n.HasUkraineContext {
		return false
	}

	cat := ValidateCategory(n.Category)
	if cat == CategoryCrime {
		if !n.HasDenmarkContext {
			return false
		}
		policySignal := 0
		if len(n.KeywordCategoryWeights) > 0 {
			policySignal += n.KeywordCategoryWeights["politics"]
			policySignal += n.KeywordCategoryWeights["visas"]
			policySignal += n.KeywordCategoryWeights["work"]
			policySignal += n.KeywordCategoryWeights["money"]
			policySignal += n.KeywordCategoryWeights["housing"]
			policySignal += n.KeywordCategoryWeights["society"]
		}
		return policySignal >= 20
	}

	if PassesPublicImpactGate(n) {
		return true // Structural news passes
	}

	// If it's a weak local/transport incident, ruthlessly cut it
	if cat == CategoryLocal || cat == CategoryCrime {
		return false // No structural impact = rejected
	}

	// If it's lifestyle/sport/family, we allow it (30% balance) but require a decent score
	if cat == CategoryLifestyle || cat == CategoryFamily || cat == CategorySport {
		// Must not be empty triviality
		return n.KeywordScore >= 20 || n.Score >= 70
	}

	// Fallback for edge cases (including structural society)
	return n.Score >= 60
}

func sortByPublishPriority(items []News) {
	sort.SliceStable(items, func(i, j int) bool {
		iGate := PassesPublicImpactGate(items[i])
		jGate := PassesPublicImpactGate(items[j])
		if iGate != jGate {
			return iGate
		}
		if items[i].CoreImpactScore != items[j].CoreImpactScore {
			return items[i].CoreImpactScore > items[j].CoreImpactScore
		}

		iImpact := isImpactCandidate(items[i])
		jImpact := isImpactCandidate(items[j])
		if iImpact != jImpact {
			return iImpact
		}
		if iImpact && items[i].ImpactScore != items[j].ImpactScore {
			return items[i].ImpactScore > items[j].ImpactScore
		}
		if items[i].AudienceScore != items[j].AudienceScore {
			return items[i].AudienceScore > items[j].AudienceScore
		}
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		iHasImage := items[i].ImageURL != ""
		jHasImage := items[j].ImageURL != ""
		if iHasImage != jHasImage {
			return iHasImage
		}
		return false
	})
}

func getMinKeywordScoreForAI() int {
	const defaultMin = 8
	if v := os.Getenv("MIN_KEYWORD_SCORE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultMin
}

func isVIPKeywordMatch(matches []config.KeywordMatch) bool {
	for _, m := range matches {
		switch strings.ToLower(m.Word) {
		case "voldtægt", "seksuelt overgreb", "overgreb", "grooming",
			"udvisning", "udvises", "udviste", "deportation":
			return true
		}
	}
	return false
}

func isBorderlineKeywordScore(score, min int) bool {
	if min <= 0 {
		return false
	}
	return score >= min-3 && score < min
}

func hasDenmarkContext(categoryWeights map[string]int, matches []config.KeywordMatch) bool {
	if len(categoryWeights) > 0 && categoryWeights["local"] > 0 {
		return true
	}
	for _, m := range matches {
		switch strings.ToLower(m.Word) {
		case "danmark", "denmark", "danish", "i danmark", "dansk", "danske", "danskere", "folketing", "regeringen", "statsminister", "kommune", "kommunen", "kommunerne", "region", "regioner":
			return true
		}
	}
	return false
}

func hasUkraineContext(matches []config.KeywordMatch) bool {
	for _, m := range matches {
		switch strings.ToLower(m.Word) {
		case "ukrainsk", "ukrainer", "ukrainske flygtninge", "ukrainsk statsborger", "sl1", "paragraf 24":
			return true
		}
	}
	return false
}

// isStubOrApology проверяет, не является ли ответ AI признанием отсутствия фактуры или стабом
func isStubOrApology(resp *ai.Response) bool {
	if resp == nil {
		return true
	}
	ca := strings.ToLower(strings.TrimSpace(resp.ConcreteAnchor))
	if ca == "insufficient_substance" ||
		strings.Contains(ca, "insufficient_substance") ||
		strings.Contains(ca, "не містить конкретики") ||
		strings.Contains(ca, "деталі невідомі") ||
		strings.Contains(ca, "подробиці не уточнюються") ||
		strings.Contains(ca, "ikke oplyst") ||
		strings.Contains(ca, "ikke tilgængelig") ||
		strings.Contains(ca, "mangler detaljer") {
		return true
	}

	dk := strings.ToLower(resp.Danish)
	ua := strings.ToLower(resp.Ukrainian)

	apologyMarkers := []string{
		"ikke tilgængelig",
		"ikke tilgængelige",
		"kildemateriale mangler",
		"i det åbne kildemateriale",
		"det åbne kildemateriale",
		"åbne kildemateriale",
		"ikke oplyst",
		"detaljer kendes ikke",
		"fremgår ikke af artiklen",
		"першоджерело не містить",
		"джерело не містить",
		"не містить додаткових",
		"не містить конкретики",
		"немає у відкритому доступі",
		"недоступні в відкритому",
		"у відкритому доступі немає",
		"деталі не розголошуються в джерелі",
		"деталі невідомі",
		"подробиці не уточнюються",
		"у статті не повідомляється",
	}

	for _, marker := range apologyMarkers {
		if strings.Contains(dk, marker) || strings.Contains(ua, marker) {
			return true
		}
	}
	return false
}
