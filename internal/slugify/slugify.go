// Package slugify provides a single, canonical slug-generation function
// shared by website/generator.go and storage/supabase.go.
//
// Previously each file had its own generateSlug with slightly different
// transliteration maps — supabase.go was missing "ё", "э", "ы".
// A single source of truth prevents slug drift.
package slugify

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// slugReplacements maps special characters to ASCII transliterations.
// Covers: Danish/Nordic (æøå), German umlauts, full Ukrainian/Russian Cyrillic.
var slugReplacements = map[string]string{
	// Danish / Nordic
	"æ": "ae", "ø": "oe", "å": "aa",
	// German
	"ä": "ae", "ö": "oe", "ü": "ue",
	// Ukrainian-specific
	"і": "i", "ї": "yi", "є": "ye", "ґ": "g",
	// Cyrillic (shared Ukrainian/Russian)
	"а": "a", "б": "b", "в": "v", "г": "h",
	"д": "d", "е": "e", "ж": "zh", "з": "z",
	"и": "y", "й": "y", "к": "k", "л": "l",
	"м": "m", "н": "n", "о": "o", "п": "p",
	"р": "r", "с": "s", "т": "t", "у": "u",
	"ф": "f", "х": "kh", "ц": "ts", "ч": "ch",
	"ш": "sh", "щ": "shch", "ь": "", "ю": "yu",
	"я": "ya",
	// Russian-only (missing in old generator.go)
	"ё": "yo", "э": "e", "ы": "y",
}

var multiHyphen = regexp.MustCompile(`-+`)

// Slug converts a title to a URL-safe slug (no date suffix).
func Slug(title string) string {
	return slug(title, 60)
}

// SlugWithDate converts a title to a URL-safe slug and appends a date suffix
// in the format YYYYMMDD. Uses the published date so the same article always
// gets the same slug regardless of when the bot runs.
func SlugWithDate(title string, publishedAt time.Time) string {
	base := slug(title, 52) // leave room for "-YYYYMMDD" (9 chars)
	return fmt.Sprintf("%s-%s", base, publishedAt.Format("20060102"))
}

func slug(title string, maxLen int) string {
	// 1. Unicode normalisation
	title = norm.NFC.String(title)

	// 2. Lowercase
	title = strings.ToLower(title)

	// 3. Transliterate known special characters
	for from, to := range slugReplacements {
		title = strings.ReplaceAll(title, from, to)
	}

	// 4. Keep only ASCII letters, digits, spaces, hyphens
	var b strings.Builder
	for _, r := range title {
		switch {
		case unicode.IsLetter(r) && r <= 127:
			b.WriteRune(r)
		case unicode.IsDigit(r):
			b.WriteRune(r)
		case r == ' ' || r == '-':
			b.WriteRune('-')
		}
	}

	s := b.String()

	// 5. Collapse multiple hyphens, trim edges
	s = multiHyphen.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")

	// 6. Hard length limit — don't cut mid-word
	if len(s) > maxLen {
		s = s[:maxLen]
		if last := strings.LastIndex(s, "-"); last > maxLen/2 {
			s = s[:last]
		}
	}

	return s
}
