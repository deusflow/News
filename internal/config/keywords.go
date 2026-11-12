package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Keyword represents a single keyword with its weight and category
type Keyword struct {
	Word     string `yaml:"word"`
	Category string `yaml:"category"`
	Weight   int    `yaml:"weight"`

	// normalizedWord stores lowercase/trimmed keyword for fast matching.
	normalizedWord string
	// wholeWordMatch is enabled for short keywords (<=4 runes) to avoid substring false positives.
	wholeWordMatch bool
}

// KeywordMatch describes one keyword hit for explainable ranking logs.
type KeywordMatch struct {
	Word      string
	Category  string
	Weight    int
	WholeWord bool
}

// KeywordsConfig holds the keywords for filtering
type KeywordsConfig struct {
	Keywords []Keyword `yaml:"keywords"`
}

// CalculateScore calculates the total score for a given text based on keywords.
//
// Backward-compatible wrapper around CalculateScoreDetailed.
func (kc *KeywordsConfig) CalculateScore(text string) (int, string) {
	total, topCategory, _ := kc.CalculateScoreDetailed(text)
	return total, topCategory
}

// CalculateScoreDetailed calculates keyword score and returns:
//  1. total score
//  2. top category by accumulated weight (excluding spam)
//  3. per-category accumulated weights (for impact-aware ranking)
func (kc *KeywordsConfig) CalculateScoreDetailed(text string) (int, string, map[string]int) {
	total, topCategory, categoryWeights, _ := kc.CalculateScoreDetailedWithMatches(text)
	return total, topCategory, categoryWeights
}

// CalculateScoreDetailedWithMatches returns score details and concrete keyword hits
// for explainable ranking logs.
func (kc *KeywordsConfig) CalculateScoreDetailedWithMatches(text string) (int, string, map[string]int, []KeywordMatch) {
	if kc == nil || len(kc.Keywords) == 0 {
		return 0, "", map[string]int{}, nil
	}

	lowerText := strings.ToLower(text)
	matches := make([]KeywordMatch, 0, 12)

	for i := range kc.Keywords {
		kw := &kc.Keywords[i]
		if kw.normalizedWord == "" {
			continue
		}

		matched := false
		if kw.wholeWordMatch {
			matched = containsWholeWord(lowerText, kw.normalizedWord)
		} else {
			matched = strings.Contains(lowerText, kw.normalizedWord)
		}

		if matched {
			matches = append(matches, KeywordMatch{
				Word:      kw.Word,
				Category:  kw.Category,
				Weight:    kw.Weight,
				WholeWord: kw.wholeWordMatch,
			})
		}
	}

	// Sort matches by weight descending
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Weight > matches[j].Weight
	})

	// Take only top 10 matches
	if len(matches) > 10 {
		matches = matches[:10]
	}

	totalScore := 0
	categoryWeights := make(map[string]int, 16) // category -> accumulated weight

	for _, m := range matches {
		totalScore += m.Weight
		categoryWeights[m.Category] += m.Weight
	}

	// Pick category with highest accumulated weight (ignore "spam" - it is a filter, not a label)
	topCategory := ""
	maxCatWeight := 0
	for cat, w := range categoryWeights {
		if cat == "spam" {
			continue
		}
		if w > maxCatWeight {
			maxCatWeight = w
			topCategory = cat
		}
	}

	return totalScore, topCategory, categoryWeights, matches
}

// LoadKeywords loads and pre-processes keywords from a YAML configuration file.
func LoadKeywords(path string) (*KeywordsConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			fmt.Printf("Warning: failed to close config file %s: %v\n", path, closeErr)
		}
	}()

	var cfg KeywordsConfig
	dec := yaml.NewDecoder(f)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse keywords config: %w", err)
	}

	if len(cfg.Keywords) == 0 {
		return nil, fmt.Errorf("keywords array is empty in config")
	}

	wholeWordMaxRunes := getEnvIntOrDefault("KEYWORD_WHOLE_WORD_MAX_RUNES", 4)
	if wholeWordMaxRunes < 1 {
		wholeWordMaxRunes = 4
	}
	strictWordBoundaries := strings.EqualFold(os.Getenv("KEYWORDS_STRICT_WORD_MATCH"), "true")

	for i := range cfg.Keywords {
		word := strings.TrimSpace(cfg.Keywords[i].Word)
		if word == "" {
			continue
		}
		cfg.Keywords[i].Word = word
		cfg.Keywords[i].normalizedWord = strings.ToLower(word)
		cfg.Keywords[i].Category = strings.ToLower(strings.TrimSpace(cfg.Keywords[i].Category))
		cfg.Keywords[i].wholeWordMatch = strictWordBoundaries || len([]rune(cfg.Keywords[i].normalizedWord)) <= wholeWordMaxRunes
	}

	return &KeywordsConfig{
		Keywords: cfg.Keywords,
	}, nil
}

// containsWholeWord checks whether token appears in text with non-word boundaries.
// Word characters are letters/digits (Unicode-aware), so "ai" won't match
// "socialordfører" or "said", but will match "AI" as a standalone token.
func containsWholeWord(text, token string) bool {
	if token == "" {
		return false
	}
	for start := strings.Index(text, token); start >= 0; {
		end := start + len(token)
		leftOK := start == 0 || !isWordRune(lastRune(text[:start]))
		rightOK := end == len(text) || !isWordRune(firstRune(text[end:]))
		if leftOK && rightOK {
			return true
		}
		nextStart := start + len(token)
		if nextStart >= len(text) {
			break
		}
		offset := strings.Index(text[nextStart:], token)
		if offset < 0 {
			break
		}
		start = nextStart + offset
	}
	return false
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	last := rune(0)
	for _, r := range s {
		last = r
	}
	return last
}
