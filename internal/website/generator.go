package website

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deusflow/News/internal/slugify"
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

// GeneratePostWithTimeout creates a markdown file with a timeout to prevent hanging
func (g *Generator) GeneratePostWithTimeout(ctx context.Context, post NewsPost, timeout time.Duration) error {
	if !g.enabled {
		return nil
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Run generation in a channel to respect timeout
	done := make(chan error, 1)
	go func() {
		done <- g.GeneratePost(post)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("website post generation timed out after %v", timeout)
	}
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
	slug := slugify.Slug(post.Title)
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
		sb.WriteString(sanitizeMarkdownContent(post.SummaryUkrainian))
	} else if post.ContentUkrainian != "" {
		sb.WriteString(sanitizeMarkdownContent(post.ContentUkrainian))
	} else {
		sb.WriteString(sanitizeMarkdownContent(post.Content))
	}
	sb.WriteString("\n\n")

	// Danish content
	sb.WriteString("## 🇩🇰 På dansk\n\n")
	if post.SummaryDanish != "" {
		sb.WriteString(sanitizeMarkdownContent(post.SummaryDanish))
	} else if post.ContentDanish != "" {
		sb.WriteString(sanitizeMarkdownContent(post.ContentDanish))
	} else {
		sb.WriteString("*Dansk version kommer snart*")
	}
	sb.WriteString("\n\n")

	// Fun fact
	if post.FunFact != "" {
		sb.WriteString("---\n\n")
		sb.WriteString(fmt.Sprintf("💡 **Цікавий факт:** %s\n", sanitizeMarkdownContent(post.FunFact)))
	}

	return sb.String()
}

// escapeYAML escapes a string for safe embedding inside YAML double-quoted scalars.
// Handles: backslash, double-quote, newline, carriage return, tab, and all
// Unicode control characters (U+0000–U+001F, U+007F) which are illegal in YAML.
func escapeYAML(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\") // must be first
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\t", " ")

	// Strip remaining ASCII control characters (U+0000–U+001F and U+007F)
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7F {
			continue // drop illegal control char
		}
		b.WriteRune(r)
	}

	// Collapse multiple spaces that may have appeared after replacements
	result := b.String()
	for strings.Contains(result, "  ") {
		result = strings.ReplaceAll(result, "  ", " ")
	}

	return strings.TrimSpace(result)
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

// sanitizeMarkdownContent removes HTML tags and potentially dangerous content
// from AI-generated text before embedding it in Hugo markdown files.
//
// Why: if AI returns text containing <script>, <iframe> or raw HTML, and Hugo
// templates use {{ .Content | safeHTML }}, that content would render as-is.
// Stripping tags at generation time is the safest defence regardless of template config.
func sanitizeMarkdownContent(s string) string {
	if s == "" {
		return ""
	}

	// Strip HTML tags (goquery is not available here; simple regex is fine
	// because we only need to remove AI-introduced tags, not full HTML parsing)
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	result := b.String()

	// Collapse multiple blank lines that might appear after tag removal
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}

	return strings.TrimSpace(result)
}
