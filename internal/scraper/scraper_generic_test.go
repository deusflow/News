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

func TestIsNavigationOrOtherArticle_AllowsRegularUpdatedSentence(t *testing.T) {
	text := "Artiklen blev opdateret med nye tal fra kommunen i eftermiddag."
	if isNavigationOrOtherArticle(text) {
		t.Fatalf("expected regular sentence with 'opdateret' to pass content filter")
	}
}

func TestIsNavigationOrOtherArticle_FiltersExplicitUpdateLabel(t *testing.T) {
	text := "Sidst opdateret kl. 14:03"
	if !isNavigationOrOtherArticle(text) {
		t.Fatalf("expected explicit update label to be treated as navigation/meta")
	}
}

func TestExtractGenericContent_DoesNotLeakOtherArticlesInInfiniteScroll(t *testing.T) {
	html := `<!doctype html>
<html><body>
<main>
  <article>
    <h1>Pas på: Rovfugl opholder sig på motorvej</h1>
    <p>= 1280)" x-cloak class="flex flex-row gap-2">{</p>
    <p>Bilister på rute 15 ved Herning advares torsdag aften om en større rovfugl ved kørebanen, oplyser Vejdirektoratet.</p>
    <p>Trafikken glider fortsat roligt forbi stedet, og politiet opfordrer til at sænke farten en smule forbi nødsporet.</p>
  </article>
  <article>
    <h2>Viborg Kommune vedtager ny plan om sygehus og vold</h2>
    <p>Del artiklen med andre læsere i Viborg Kommune om læge, unge, minister og økonomi i Danmark.</p>
  </article>
</main>
</body></html>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("doc parse: %v", err)
	}

	got := extractGenericContent(doc)
	if strings.Contains(got, "Viborg Kommune") || strings.Contains(got, "minister") {
		t.Fatalf("expected infinite-scroll second <article> not to leak into first article, got: %q", got)
	}
	if strings.Contains(got, "x-cloak") {
		t.Fatalf("expected AlpineJS fragment to be filtered out, got: %q", got)
	}
	if !strings.Contains(got, "Vejdirektoratet") {
		t.Fatalf("expected first article body to be preserved, got: %q", got)
	}
}

func TestDetectDOMPaywallGate_DetectsInformationPaywallAndSkipsAudioBoilerplate(t *testing.T) {
	html := `<!doctype html>
<html><body>
<article>
  <h1>Partier oprørte over kontroversielt lovforslag</h1>
  <p>Din browser understøtter ikke afspilning af lyd.</p>
  <p>Det er i øjeblikket ikke muligt at afspille denne lydfil.</p>
  <p>Et flertal i Folketinget diskuterer et nyt lovforslag om børnefamilier efter måneders politisk pres fra oppositionen.</p>
  <div class="button pw-subscribe-button">Bestil her</div>
  <form class="openid-connect-login-form"></form>
</article>
<article>
  <p>Anbefalet anden artikel i sidebaren med over tusind tegn om helt andre emner der ellers ville narre længdetjekket.</p>
</article>
</body></html>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("doc parse: %v", err)
	}

	domPaywalled, _ := detectDOMPaywallGate(doc)
	if !domPaywalled {
		t.Fatalf("expected DOM paywall gate to be detected before stripping")
	}

	got := extractGenericContent(doc)
	if strings.Contains(got, "browser understøtter ikke") || strings.Contains(got, "ikke muligt at afspille") {
		t.Fatalf("expected audio-player boilerplate to be filtered out, got: %q", got)
	}
	if strings.Contains(got, "Anbefalet anden artikel") {
		t.Fatalf("expected sidebar article not to leak into content, got: %q", got)
	}
}
