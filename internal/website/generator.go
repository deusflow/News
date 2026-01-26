package website

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NewsPost represents data needed to generate a website post
type NewsPost struct {
	Title            string
	TitleUkrainian   string
	Content          string
	ContentUkrainian string
	ContentDanish    string
	Summary          string
	SummaryUkrainian string
	SummaryDanish    string
	Link             string
	ImageURL         string
	SourceName       string
	Category         string
	Tags             []string
	Mood             string
	TLDR             string
	FunFact          string
	PublishedAt      time.Time
}

// Generator handles website content generation
type Generator struct {
	contentDir string
	enabled    bool
}

// NewGenerator creates a new website generator
func NewGenerator(contentDir string, enabled bool) *Generator {
	return &Generator{
		contentDir: contentDir,
		enabled:    enabled,
	}
}

// IsEnabled returns whether website generation is enabled
func (g *Generator) IsEnabled() bool {
	return g.enabled
}

// GeneratePost creates a markdown file for the news post
func (g *Generator) GeneratePost(post NewsPost) error {
	if !g.enabled {
		return nil
	}

	// Ensure content directory exists
	postsDir := filepath.Join(g.contentDir, "posts")
	if err := os.MkdirAll(postsDir, 0755); err != nil {
		return fmt.Errorf("failed to create posts directory: %w", err)
	}

	// Generate filename
	slug := generateSlug(post.Title)
	dateStr := post.PublishedAt.Format("2006-01-02")
	filename := fmt.Sprintf("%s-%s.md", dateStr, slug)
	filePath := filepath.Join(postsDir, filename)

	// Check if file already exists
	if _, err := os.Stat(filePath); err == nil {
		// File exists, skip
		return nil
	}

	// Generate content
	content := g.generateMarkdown(post)

	// Write file
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write post file: %w", err)
	}

	return nil
}

// generateMarkdown creates the markdown content with front matter
func (g *Generator) generateMarkdown(post NewsPost) string {
	var sb strings.Builder

	// Front matter
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: %q\n", escapeYAML(post.Title)))
	sb.WriteString(fmt.Sprintf("date: %s\n", post.PublishedAt.Format(time.RFC3339)))
	sb.WriteString("draft: false\n")

	// Categories
	if post.Category != "" {
		sb.WriteString(fmt.Sprintf("categories: [%q]\n", post.Category))
	} else {
		sb.WriteString("categories: [\"News\"]\n")
	}

	// Tags
	if len(post.Tags) > 0 {
		tags := make([]string, len(post.Tags))
		for i, tag := range post.Tags {
			tags[i] = fmt.Sprintf("%q", tag)
		}
		sb.WriteString(fmt.Sprintf("tags: [%s]\n", strings.Join(tags, ", ")))
	}

	// Image
	if post.ImageURL != "" {
		sb.WriteString(fmt.Sprintf("image: %q\n", post.ImageURL))
	}

	// Source
	if post.Link != "" {
		sb.WriteString(fmt.Sprintf("source_url: %q\n", post.Link))
	}
	if post.SourceName != "" {
		sb.WriteString(fmt.Sprintf("source_name: %q\n", post.SourceName))
	}

	// TLDR
	if post.TLDR != "" {
		sb.WriteString(fmt.Sprintf("tldr: %q\n", escapeYAML(post.TLDR)))
	} else if post.SummaryUkrainian != "" {
		// Use first sentence of Ukrainian summary as TLDR
		tldr := getFirstSentence(post.SummaryUkrainian)
		sb.WriteString(fmt.Sprintf("tldr: %q\n", escapeYAML(tldr)))
	}

	// Mood
	if post.Mood != "" {
		sb.WriteString(fmt.Sprintf("mood: %q\n", post.Mood))
	}

	sb.WriteString("---\n\n")

	// Ukrainian content
	sb.WriteString("## 🇺🇦 Українською\n\n")
	if post.SummaryUkrainian != "" {
		sb.WriteString(post.SummaryUkrainian)
	} else if post.ContentUkrainian != "" {
		sb.WriteString(post.ContentUkrainian)
	} else {
		sb.WriteString(post.Content)
	}
	sb.WriteString("\n\n")

	// Danish content
	sb.WriteString("## 🇩🇰 På dansk\n\n")
	if post.SummaryDanish != "" {
		sb.WriteString(post.SummaryDanish)
	} else if post.ContentDanish != "" {
		sb.WriteString(post.ContentDanish)
	} else {
		sb.WriteString("*Dansk version kommer snart*")
	}
	sb.WriteString("\n\n")

	// Fun fact
	if post.FunFact != "" {
		sb.WriteString("---\n\n")
		sb.WriteString(fmt.Sprintf("💡 **Цікавий факт:** %s\n", post.FunFact))
	}

	return sb.String()
}

// generateSlug creates a URL-friendly slug from title
func generateSlug(title string) string {
	// Normalize unicode
	title = norm.NFC.String(title)

	// Convert to lowercase
	title = strings.ToLower(title)

	// Replace special characters with transliterations
	replacements := map[string]string{
		"æ": "ae", "ø": "oe", "å": "aa",
		"ä": "ae", "ö": "oe", "ü": "ue",
		"і": "i", "ї": "yi", "є": "ye",
		"а": "a", "б": "b", "в": "v", "г": "h", "ґ": "g",
		"д": "d", "е": "e", "ж": "zh", "з": "z", "и": "y",
		"й": "y", "к": "k", "л": "l", "м": "m", "н": "n",
		"о": "o", "п": "p", "р": "r", "с": "s", "т": "t",
		"у": "u", "ф": "f", "х": "kh", "ц": "ts", "ч": "ch",
		"ш": "sh", "щ": "shch", "ь": "", "ю": "yu", "я": "ya",
	}

	for from, to := range replacements {
		title = strings.ReplaceAll(title, from, to)
	}

	// Remove non-alphanumeric characters (keep spaces and hyphens)
	var result strings.Builder
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			result.WriteRune(r)
		} else if r == ' ' || r == '-' {
			result.WriteRune('-')
		}
	}

	slug := result.String()

	// Replace multiple hyphens with single hyphen
	re := regexp.MustCompile(`-+`)
	slug = re.ReplaceAllString(slug, "-")

	// Trim hyphens from start and end
	slug = strings.Trim(slug, "-")

	// Limit length
	if len(slug) > 60 {
		slug = slug[:60]
		// Don't cut in the middle of a word
		if lastHyphen := strings.LastIndex(slug, "-"); lastHyphen > 30 {
			slug = slug[:lastHyphen]
		}
	}

	return slug
}

// escapeYAML escapes special characters for YAML strings
func escapeYAML(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// getFirstSentence extracts the first sentence from text
func getFirstSentence(text string) string {
	// Find first sentence ending
	endings := []string{". ", "! ", "? "}
	minPos := len(text)

	for _, ending := range endings {
		if pos := strings.Index(text, ending); pos != -1 && pos < minPos {
			minPos = pos + 1
		}
	}

	if minPos < len(text) {
		return strings.TrimSpace(text[:minPos])
	}

	// No sentence ending found, return first 100 chars
	if len(text) > 100 {
		return strings.TrimSpace(text[:100]) + "..."
	}

	return strings.TrimSpace(text)
}
