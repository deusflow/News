package scraper

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/PuerkitoBio/goquery"
)

// userAgents is a pool of modern desktop User-Agent strings.
// Rotated randomly on every request to avoid hardcoded version drift.
var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36",
}

// RandomUserAgent returns a random User-Agent from the pool.
func RandomUserAgent() string {
	return userAgents[rand.IntN(len(userAgents))]
}

// ArticleContent is full article content
type ArticleContent struct {
	Title         string
	Content       string
	URL           string
	ImageURL      string
	IsPaywalled   bool
	PaywallReason string
}

// paywallIndicators contains phrases commonly found in Danish paywalled articles or subscriber blocks.
var paywallIndicators = []string{
	"kræver abonnement",
	"forbeholdt abonnenter",
	"kun for abonnenter",
	"bliv abonnent",
	"køb abonnement",
	"tegne abonnement",
	"opret abonnement",
	"låst artikel",
	"låst indhold",
	"eksklusivt for abonnenter",
	"denne artikel er kun tilgængelig for abonnenter",
	"prøv 1 måned",
	"prøv en måned",
	"få adgang til hele artiklen",
	"få fuld adgang",
	"køb adgang",
	"få ubegrænset adgang",
	"log ind for at læse videre",
	"log ind for at læse hele",
	"artiklen kan kun læses af abonnenter",
	"dette er en plus-artikel",
	"plus-artikel",
	"er du allerede abonnent",
	"abonnér nu",
	"bestil abonnement",
	"køb digital adgang",
	"adgang til artiklen",
}

// knownPaywallDomains are publishers where many articles have hard/dynamic paywalls.
var knownPaywallDomains = []string{
	"berlingske.dk",
	"politiken.dk",
	"jyllands-posten.dk",
	"jp.dk",
	"borsen.dk",
	"information.dk",
	"weekendavisen.dk",
	"kristeligt-dagblad.dk",
	"finans.dk",
}

// IsPaywalledOrStub checks if an extracted article content is paywalled, a stub, or an empty teaser.
// It returns true and a human-readable reason if the article should be discarded before sending to AI.
func IsPaywalledOrStub(content string, articleURL string) (bool, string) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return true, "empty content"
	}

	lowerContent := strings.ToLower(trimmed)
	lowerURL := strings.ToLower(articleURL)

	// 1. Explicit paywall indicator phrases
	for _, phrase := range paywallIndicators {
		if strings.Contains(lowerContent, phrase) {
			return true, fmt.Sprintf("contains paywall phrase: %q", phrase)
		}
	}

	// 2. Count runes and paragraphs
	runes := []rune(trimmed)
	runeCount := len(runes)
	paragraphs := strings.Split(trimmed, "\n\n")

	// Filter out tiny 1-line fragments from paragraph count
	realParagraphCount := 0
	for _, p := range paragraphs {
		if len(strings.TrimSpace(p)) > 40 {
			realParagraphCount++
		}
	}

	// 3. Absolute minimum threshold: any article with less than 250 characters is a stub
	if runeCount < 250 {
		return true, fmt.Sprintf("stub content too short (%d chars, min 250)", runeCount)
	}

	// 4. Known paywall publisher check: paywalls on Berlingske/Politiken/JP often leak 1 teaser paragraph
	for _, domain := range knownPaywallDomains {
		if strings.Contains(lowerURL, domain) {
			if runeCount < 400 || realParagraphCount < 2 {
				return true, fmt.Sprintf("paywalled publisher %s with teaser content (%d chars, %d paragraphs)", domain, runeCount, realParagraphCount)
			}
		}
	}

	return false, ""
}

// scraperHTTPClient is shared across all scrape requests to reuse connections.
var scraperHTTPClient = &http.Client{Timeout: 15 * time.Second}

// ExtractFullArticle gets full text and og:image of article by URL.
// Uses a single HTTP request — image extraction reuses the same parsed document.
func ExtractFullArticle(ctx context.Context, articleURL string) (*ArticleContent, error) {

	req, err := http.NewRequestWithContext(ctx, "GET", articleURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}
	req.Header.Set("User-Agent", RandomUserAgent())

	resp, err := scraperHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error loading page: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error parsing HTML: %v", err)
	}

	content := extractContentBySource(doc, articleURL)
	title := extractTitle(doc)

	if content == "" {
		return nil, fmt.Errorf("can't get content")
	}

	// Extract og:image from the SAME document — no second HTTP request needed.
	imgURL := extractImageFromDoc(doc, articleURL)

	isPaywalled, paywallReason := IsPaywalledOrStub(content, articleURL)

	return &ArticleContent{
		Title:         title,
		Content:       content,
		URL:           articleURL,
		ImageURL:      imgURL,
		IsPaywalled:   isPaywalled,
		PaywallReason: paywallReason,
	}, nil
}

// extractContentBySource gets content by news site
func extractContentBySource(doc *goquery.Document, url string) string {
	var content string

	switch {
	case strings.Contains(url, "dr.dk"):
		// For DR (Danmarks Radio)
		content = extractDRContent(doc)
	case strings.Contains(url, "ekstrabladet.dk"):
		// For Ekstrabladet
		content = extractEkstrabladetContent(doc)
	case strings.Contains(url, "tv2.dk"):
		// For TV2
		content = extractTV2Content(doc)
	case strings.Contains(url, "bt.dk"):
		// For BT
		content = extractBTContent(doc)
	default:
		// Generic parser for other sites
		content = extractGenericContent(doc)
	}

	return cleanContent(content)
}

// Enhanced content extraction with better article boundary detection
func extractDRContent(doc *goquery.Document) string {
	var paragraphs []string

	// Try different selectors for DR with priority order - более специфичные селекторы
	selectors := []string{
		"article .dre-article-body p", // Основное содержимое статьи
		".dre-article-body p",         // Альтернативный селектор
		"article[data-article-id] p",  // Статья с ID
		".article-content p",          // Контент статьи
		"main article p",              // Главная статья
	}

	articleFound := false
	for _, selector := range selectors {
		paragraphCount := 0
		doc.Find(selector).Each(func(i int, s *goquery.Selection) {
			// Останавливаемся после первых 8 параграфов чтобы не захватить другие статьи
			if paragraphCount >= 15 {
				return
			}

			text := strings.TrimSpace(s.Text())

			// Пропускаем пустые и короткие параграфы
			if text == "" || len(text) < 30 {
				return
			}

			// Проверяем на навигационные элементы и элементы других статей
			if isNavigationOrOtherArticle(text) {
				return
			}

			paragraphs = append(paragraphs, text)
			paragraphCount++
		})

		// Если нашли контент с этим селектором, используем его
		if len(paragraphs) >= 2 {
			articleFound = true
			break
		}
	}

	// Если ничего не нашли, пробуем более общий поиск, но с жестким ограничением
	if !articleFound {
		doc.Find("p").Each(func(i int, s *goquery.Selection) {
			if len(paragraphs) >= 5 { // Максимум 5 параграфа для безопасности
				return
			}

			text := strings.TrimSpace(s.Text())
			if len(text) > 50 && !isNavigationOrOtherArticle(text) {
				paragraphs = append(paragraphs, text)
			}
		})
	}

	return strings.Join(paragraphs, "\n\n")
}

// isNavigationOrOtherArticle проверяет, является ли текст навигацией или частью другой статьи
// isNavigationOrOtherArticle returns true for UI/navigation fragments that
// should be skipped during content extraction.
//
// IMPORTANT: this function must NOT filter by topic or subject matter.
// "Does this paragraph contain news about Putin?" is NOT a navigation question.
// Topic/subject filtering belongs exclusively in keywords/scoring (config/keywords.yaml).
// Putting topic filters here silently drops content from articles that happen to
// mention those names — even when the article is primarily about Denmark.
func isNavigationOrOtherArticle(text string) bool {
	lowerText := strings.ToLower(text)

	navIndicators := []string{
		"læs også", "se også", "følg", "cookie", "gdpr",
		"abonnement", "privatlivspolitik", "nyhedsbrev",
		"log ind", "opret", "del artikel", "print",
		"reklame", "annonce", "sponsor",
		"sidst opdateret", "senest opdateret",
		"redigeret", "publiceret", "dr nyheder",
		"vi tager ansvar for indholdet",
		"tilmeldt pressenævnet",
		"pressenævnet",
		"administrerende direktør",
		"ansvarshavende chefredaktør",
		"chefredaktør",
		"beskyttet efter lov om ophavsret",
		"tekst- og datamining",
		"forbeholder sig alle rettigheder",
		"cvr nr.",
		"bestil abonnement",
		"nulstil adgangskode",
	}

	for _, indicator := range navIndicators {
		if strings.Contains(lowerText, indicator) {
			return true
		}
	}

	return false
}

// extractEkstrabladetContent gets content from ekstrabladet.dk
func extractEkstrabladetContent(doc *goquery.Document) string {
	var paragraphs []string

	// Selectors for Ekstrabladet
	selectors := []string{
		".article-body p",
		".article-content p",
		".content p",
		"article p",
		".body-text p",
	}

	for _, selector := range selectors {
		doc.Find(selector).Each(func(i int, s *goquery.Selection) {
			text := strings.TrimSpace(s.Text())
			if text != "" && len(text) > 10 {
				paragraphs = append(paragraphs, text)
			}
		})
		if len(paragraphs) > 0 {
			break
		}
	}

	return strings.Join(paragraphs, "\n\n")
}

// extractTV2Content gets content from tv2.dk
func extractTV2Content(doc *goquery.Document) string {
	var paragraphs []string

	selectors := []string{
		".article-body p",
		".content p",
		"article p",
		".article-text p",
	}

	for _, selector := range selectors {
		doc.Find(selector).Each(func(i int, s *goquery.Selection) {
			text := strings.TrimSpace(s.Text())
			if text != "" && len(text) > 10 {
				paragraphs = append(paragraphs, text)
			}
		})
		if len(paragraphs) > 0 {
			break
		}
	}

	return strings.Join(paragraphs, "\n\n")
}

// extractBTContent gets content from bt.dk
func extractBTContent(doc *goquery.Document) string {
	var paragraphs []string

	selectors := []string{
		".article-body p",
		".content p",
		"article p",
	}

	for _, selector := range selectors {
		doc.Find(selector).Each(func(i int, s *goquery.Selection) {
			text := strings.TrimSpace(s.Text())
			if text != "" && len(text) > 10 {
				paragraphs = append(paragraphs, text)
			}
		})
		if len(paragraphs) > 0 {
			break
		}
	}

	return strings.Join(paragraphs, "\n\n")
}

// extractGenericContent is universal parser for any site
func extractGenericContent(doc *goquery.Document) string {
	var paragraphs []string
	seen := make(map[string]struct{})

	// Strip non-content and layout containers so footers, sidebars and legal notices never leak
	doc.Find("footer, .footer, .site-footer, .prose-footer, [role='contentinfo'], nav, aside, .sidebar, .cookie-banner, .paywall-banner, .advertisement").Remove()

	// Limits chosen to feed LLM enough context but avoid capturing whole page/footer.
	const (
		minParagraphLen = 40
		maxParagraphs   = 40
		maxChars        = 8500
	)

	currentChars := 0

	addParagraph := func(text string) bool {
		text = strings.TrimSpace(text)
		if text == "" {
			return false
		}
		// Skip very short fragments (often captions, ads, UI labels)
		if len(text) < minParagraphLen {
			return false
		}
		// Skip navigation/ads/other-article fragments
		if isNavigationOrOtherArticle(text) {
			return false
		}

		// Heuristic: skip lines that are mostly punctuation/symbols
		letters := 0
		for _, r := range text {
			if unicode.IsLetter(r) {
				letters++
			}
		}
		if letters < 20 {
			return false
		}

		key := strings.ToLower(text)
		if _, ok := seen[key]; ok {
			return false
		}
		seen[key] = struct{}{}

		// Respect global limits
		if len(paragraphs) >= maxParagraphs {
			return true
		}
		if currentChars+len(text) > maxChars {
			return true
		}

		paragraphs = append(paragraphs, text)
		currentChars += len(text) + 2
		return false
	}

	// Try most popular selectors
	selectors := []string{
		"article p",
		".article p",
		".content p",
		".post-content p",
		".entry-content p",
		"main p",
		"#content p",
		".text p",
		"p",
	}

	for _, selector := range selectors {
		stop := false
		doc.Find(selector).EachWithBreak(func(i int, s *goquery.Selection) bool {
			text := strings.TrimSpace(s.Text())
			if shouldStop := addParagraph(text); shouldStop {
				stop = true
				return false
			}
			return true
		})

		// If a specific article container was found, don't fall back to broader selectors like page-wide "p"
		if stop || len(paragraphs) >= 2 || (len(paragraphs) >= 1 && currentChars >= 250) {
			break
		}
	}

	return strings.Join(paragraphs, "\n\n")
}

// extractTitle gets article title
func extractTitle(doc *goquery.Document) string {
	// Try different selectors for title
	selectors := []string{
		"h1",
		"title",
		".article-title",
		".headline",
		".entry-title",
	}

	for _, selector := range selectors {
		title := doc.Find(selector).First().Text()
		title = strings.TrimSpace(title)
		if title != "" {
			return title
		}
	}

	return ""
}

// cleanContent cleans and normalizes text with better formatting
func cleanContent(content string) string {
	if content == "" {
		return ""
	}

	// Note: goquery.Selection.Text() already strips all HTML tags,
	// so no need to process <br>, <p>, etc. - they don't exist here.
	// The paragraph structure (\n\n) is already added by extract functions.

	content = strings.TrimSpace(content)

	// Remove junk phrases from all sources
	junkPhrases := []string{
		"På Ekstra Bladet lægger vi stor vægt på at have en tæt dialog med jer læsere",
		"Jeres input er guld værd, og mange historier ville ikke kunne lade sig gøre uden jeres tip",
		"Men selv om vi også har tradition for at turde, når andre tier, værner vi om en sober og konstruktiv tone",
		"Ekstra Bladet og evt. politianmeldt",
		"DR Nyheder følger Danmarks Radio",
		"Følg med på dr.dk",
		"Læs også:", "Se også:", "Hør mere:", "Video:",
		"Læs mere på", "Klik her for at", "Følg os på",
		"Del artiklen", "Print artiklen", "Send til en ven", "Gem artiklen",
		"Cookie", "GDPR", "Privatlivspolitik", "Abonnement",
		"Tilmeld dig nyhedsbrevet", "Log ind", "Opret bruger",
	}

	for _, phrase := range junkPhrases {
		content = strings.ReplaceAll(content, phrase, "")
	}

	// Format paragraphs
	lines := strings.Split(content, "\n")
	var cleanLines []string
	var currentParagraph strings.Builder

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip empty and very short lines
		if len(line) < 8 {
			if currentParagraph.Len() > 0 {
				paragraph := strings.TrimSpace(currentParagraph.String())
				if len(paragraph) > 30 {
					cleanLines = append(cleanLines, paragraph)
				}
				currentParagraph.Reset()
			}
			continue
		}

		// Check for junk lines
		lower := strings.ToLower(line)
		isJunk := false
		junkIndicators := []string{
			"cookie", "gdpr", "reklame", "annonce", "læs mere",
			"klik her", "følg os", "del artikel", "print", "gem artikel",
		}

		for _, indicator := range junkIndicators {
			if strings.Contains(lower, indicator) {
				isJunk = true
				break
			}
		}

		if isJunk {
			continue
		}

		// Make sentences into paragraphs
		if strings.HasSuffix(line, ".") || strings.HasSuffix(line, "!") || strings.HasSuffix(line, "?") {
			if currentParagraph.Len() > 0 {
				currentParagraph.WriteString(" ")
			}
			currentParagraph.WriteString(line)

			paragraph := strings.TrimSpace(currentParagraph.String())
			if len(paragraph) > 30 {
				cleanLines = append(cleanLines, paragraph)
			}
			currentParagraph.Reset()
		} else {
			if currentParagraph.Len() > 0 {
				currentParagraph.WriteString(" ")
			}
			currentParagraph.WriteString(line)
		}
	}

	// Save last paragraph
	if currentParagraph.Len() > 0 {
		paragraph := strings.TrimSpace(currentParagraph.String())
		if len(paragraph) > 30 {
			cleanLines = append(cleanLines, paragraph)
		}
	}

	// Join paragraphs
	resultText := strings.Join(cleanLines, "\n\n")

	// Final clean
	for strings.Contains(resultText, "  ") {
		resultText = strings.ReplaceAll(resultText, "  ", " ")
	}
	for strings.Contains(resultText, "\n\n\n") {
		resultText = strings.ReplaceAll(resultText, "\n\n\n", "\n\n")
	}

	resultText = strings.TrimSpace(resultText)

	// Limit length, keep full paragraphs
	if len(resultText) > 8500 {
		paragraphs := strings.Split(resultText, "\n\n")
		var selectedParagraphs []string
		totalLength := 0

		for _, paragraph := range paragraphs {
			if totalLength+len(paragraph) < 8000 {
				selectedParagraphs = append(selectedParagraphs, paragraph)
				totalLength += len(paragraph) + 2
			} else {
				break
			}
		}

		if len(selectedParagraphs) > 0 {
			resultText = strings.Join(selectedParagraphs, "\n\n")
		}
	}

	return resultText
}

// extractImageFromDoc extracts the best representative image URL from an
// already-parsed goquery document. Called by ExtractFullArticle so we never
// make a second HTTP request just for the image.
func extractImageFromDoc(doc *goquery.Document, pageURL string) string {
	resolve := func(src string) string {
		src = strings.TrimSpace(src)
		if src == "" {
			return ""
		}
		u, err := url.Parse(src)
		if err != nil {
			return ""
		}
		if u.Scheme == "http" || u.Scheme == "https" {
			return src
		}
		base, err := url.Parse(pageURL)
		if err != nil {
			return src
		}
		return base.ResolveReference(u).String()
	}

	// Priority 1: og:image variants
	for _, attr := range []string{
		`meta[property="og:image"]`,
		`meta[property="og:image:url"]`,
		`meta[property="og:image:secure_url"]`,
	} {
		if v, ok := doc.Find(attr).Attr("content"); ok {
			if img := resolve(v); strings.HasPrefix(img, "http") {
				return img
			}
		}
	}

	// Priority 2: twitter:image
	if v, ok := doc.Find(`meta[name="twitter:image"], meta[name="twitter:image:src"]`).Attr("content"); ok {
		if img := resolve(v); strings.HasPrefix(img, "http") {
			return img
		}
	}

	// Priority 3: link rel=image_src
	if v, ok := doc.Find(`link[rel="image_src"]`).Attr("href"); ok {
		if img := resolve(v); strings.HasPrefix(img, "http") {
			return img
		}
	}

	// Fallback: first <img> in article/main
	for _, sel := range []string{"article img", "main img", "img"} {
		n := doc.Find(sel).First()
		if n == nil || n.Length() == 0 {
			continue
		}
		if v, ok := n.Attr("srcset"); ok {
			if img := resolve(pickLargestFromSrcset(v)); strings.HasPrefix(img, "http") && isLikelyImage(img) {
				return img
			}
		}
		if v, ok := n.Attr("data-src"); ok {
			if img := resolve(v); strings.HasPrefix(img, "http") && isLikelyImage(img) {
				return img
			}
		}
		if v, ok := n.Attr("src"); ok {
			if img := resolve(v); strings.HasPrefix(img, "http") && isLikelyImage(img) {
				return img
			}
		}
	}

	return ""
}

// ExtractImageURL fetches a page and extracts its representative image.
// Prefer using ExtractFullArticle which reuses the same HTTP request.
// This function exists for callers that only need the image without content.
func ExtractImageURL(pageURL string) (string, error) {
	if strings.TrimSpace(pageURL) == "" {
		return "", fmt.Errorf("empty url")
	}

	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}
	req.Header.Set("User-Agent", RandomUserAgent())

	resp, err := scraperHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("error loading page: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error parsing HTML: %v", err)
	}

	return extractImageFromDoc(doc, pageURL), nil
}

func isLikelyImage(u string) bool {
	u = strings.ToLower(strings.TrimSpace(u))
	if u == "" {
		return false
	}
	if strings.HasPrefix(u, "data:") || strings.HasSuffix(u, ".svg") {
		return false
	}
	if strings.HasSuffix(u, ".jpg") || strings.HasSuffix(u, ".jpeg") || strings.HasSuffix(u, ".png") || strings.HasSuffix(u, ".webp") || strings.HasSuffix(u, ".gif") {
		return true
	}
	// Allow URLs without extension but with common patterns (more permissive)
	if strings.Contains(u, "/images/") || strings.Contains(u, "cdn") || strings.Contains(u, "image") || strings.Contains(u, "_next/image") {
		return true
	}
	return false
}

// pickLargestFromSrcset parses a srcset attribute and returns the URL with the largest width descriptor
func pickLargestFromSrcset(srcset string) string {
	bestURL := ""
	bestW := -1
	parts := strings.Split(srcset, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// pattern: URL [<width>w]
		fields := strings.Fields(p)
		if len(fields) == 0 {
			continue
		}
		urlPart := fields[0]
		w := 0
		if len(fields) > 1 {
			last := fields[1]
			last = strings.TrimSpace(last)
			if strings.HasSuffix(last, "w") {
				if n, err := strconv.Atoi(strings.TrimSuffix(last, "w")); err == nil {
					w = n
				}
			}
		}
		if w > bestW {
			bestW = w
			bestURL = urlPart
		}
	}
	return strings.TrimSpace(bestURL)
}
