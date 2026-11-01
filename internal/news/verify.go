package news

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	// numberRegex extracts substantive numbers (integers, decimals, and percentages)
	numberRegex = regexp.MustCompile(`\b\d+(?:[.,]\d+)?%?\b`)

	// danishNumberWords maps Danish words to numeric values
	danishNumberWords = map[string]int{
		"nul":         0,
		"en":          1,
		"et":          1,
		"to":          2,
		"tre":         3,
		"fire":        4,
		"fem":         5,
		"seks":        6,
		"syv":         7,
		"otte":        8,
		"ni":          9,
		"ti":          10,
		"elleve":      11,
		"tolv":        12,
		"tretten":     13,
		"fjorten":     14,
		"femten":      15,
		"seksten":     16,
		"sytten":      17,
		"atten":       18,
		"nitten":      19,
		"tyve":        20,
		"tredive":     30,
		"fyrre":       40,
		"halvtreds":   50,
		"tres":        60,
		"halvfjerds":  70,
		"firs":        80,
		"halvfems":    90,
		"hundrede":    100,
		"hundredevis": 100,
		"tusind":      1000,
		"tusinde":     1000,
		"tusindvis":   1000,
		"million":     1000000,
		"millioner":   1000000,
		"milliard":    1000000000,
		"milliarder":  1000000000,
		"første":      1,
		"anden":       2,
		"tredje":      3,
		"fjerde":      4,
		"femte":       5,
		"sjette":      6,
		"syvende":     7,
		"ottende":     8,
		"niende":      9,
		"tiende":      10,
	}
)

// normalizeNumericString cleans formatting (thousands dots, spaces) and returns standard float/int representation
func normalizeNumericString(raw string) (string, float64, bool) {
	s := strings.TrimRight(raw, "%.,;")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\u00a0", "")

	// Check Danish format with thousand dots e.g. "50.000" or "1.500.000"
	dotCount := strings.Count(s, ".")
	commaCount := strings.Count(s, ",")

	if dotCount > 0 && commaCount == 0 {
		// If multiple dots or exactly 3 digits after dot, it's a thousand separator: 50.000 -> 50000
		parts := strings.Split(s, ".")
		isThousandSep := true
		for _, p := range parts[1:] {
			if len(p) != 3 {
				isThousandSep = false
				break
			}
		}
		if isThousandSep {
			s = strings.ReplaceAll(s, ".", "")
		}
	} else if commaCount == 1 && dotCount == 0 {
		// Danish decimal comma: 2,5 -> 2.5
		s = strings.ReplaceAll(s, ",", ".")
	} else if dotCount > 0 && commaCount == 1 {
		// 15.000,50 -> 15000.50
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	}

	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", 0, false
	}
	return s, val, true
}

// extractNumbers parses all numeric tokens from text
func extractNumbers(text string) map[string]float64 {
	found := make(map[string]float64)
	matches := numberRegex.FindAllString(text, -1)
	for _, m := range matches {
		cleaned, val, ok := normalizeNumericString(m)
		if ok {
			found[cleaned] = val
		}
	}
	return found
}

// extractDanishSourceNumberValues parses all numbers and number-words from Danish source text
func extractDanishSourceNumberValues(danishText string) map[float64]bool {
	values := make(map[float64]bool)

	// Digits in text
	for _, val := range extractNumbers(danishText) {
		values[val] = true
	}

	// Danish words in text
	lower := strings.ToLower(danishText)
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	for _, w := range words {
		if numVal, ok := danishNumberWords[w]; ok {
			values[float64(numVal)] = true
		}
	}

	return values
}

// VerifyNumbers checks whether substantive numbers (>= 3, decimals, percentages, dates)
// in the Ukrainian translation are grounded in the original Danish source.
// Returns an error if an unverified number was hallucinated by AI.
func VerifyNumbers(danishSource, ukrainianText string) error {
	if strings.TrimSpace(ukrainianText) == "" || strings.TrimSpace(danishSource) == "" {
		return nil
	}

	sourceValues := extractDanishSourceNumberValues(danishSource)
	uaNumbers := extractNumbers(ukrainianText)

	for rawNum, val := range uaNumbers {
		// Whitelist 1 and 2: heavily used as Ukrainian pronouns, counters, or grammar ("1 день", "1-2 речення")
		if val == 1.0 || val == 2.0 {
			continue
		}

		// Also check if raw string exists directly in Danish text (e.g. "15%" or "2026")
		cleanRaw := strings.TrimRight(rawNum, "%")
		if strings.Contains(danishSource, rawNum) || strings.Contains(danishSource, cleanRaw) {
			continue
		}

		// Check numeric equivalence
		if sourceValues[val] {
			continue
		}

		return fmt.Errorf("hallucinated number %q (value %v) not found in Danish source text", rawNum, val)
	}

	return nil
}

// VerifyUkrainian confirms that the text is written in valid Ukrainian and not Russian
// (rejects Russian letters: ы, э, ъ).
func VerifyUkrainian(text string) error {
	const russianLetters = "ыэъЫЭЪ"
	if idx := strings.IndexAny(text, russianLetters); idx != -1 {
		return fmt.Errorf("text contains Russian character %q: %s", text[idx:idx+2], text)
	}
	return nil
}

