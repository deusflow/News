package scraper

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func TestExtractGenericContent_DoesNotStopAtThreeParagraphs(t *testing.T) {
	html := `<!doctype html>
<html><body>
<article>
<p>Short.</p>
<p>This is a real paragraph with enough length to be considered meaningful for extraction.</p>
<p>Another meaningful paragraph that contains some actual information and continues the story.</p>
<p>Third meaningful paragraph with details that would be missing if we stop too early.</p>
<p>Fourth meaningful paragraph to ensure we can collect more than three paragraphs.</p>
</article>
</body></html>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("doc parse: %v", err)
	}

	got := extractGenericContent(doc)
	// We expect at least 4 meaningful paragraphs (the first one is too short and will be skipped)
	if n := len(strings.Split(strings.TrimSpace(got), "\n\n")); n < 4 {
		t.Fatalf("expected >=4 paragraphs, got %d: %q", n, got)
	}
}

func TestExtractGenericContent_StopsOnLimits(t *testing.T) {
	// Generate lots of paragraphs. We don't assert exact count because limits are heuristic,
	// but it should not return an absurdly large string.
	var b strings.Builder
	b.WriteString("<html><body><main>")
	for i := 0; i < 200; i++ {
		b.WriteString("<p>")
		b.WriteString("This is a meaningful paragraph with plenty of letters to pass filters and simulate an article body. ")
		b.WriteString("It should be truncated by maxChars/maxParagraphs safeguards.")
		b.WriteString("</p>")
	}
	b.WriteString("</main></body></html>")

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("doc parse: %v", err)
	}

	got := extractGenericContent(doc)
	if len(got) > 4000 {
		t.Fatalf("expected generic content to be bounded, got len=%d", len(got))
	}
}
