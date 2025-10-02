package scraper

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// ArticleContent is full article content
type ArticleContent struct {
	Title   string
	Content string
	URL     string
}

// ExtractFullArticle gets full text of article by URL
func ExtractFullArticle(url string) (*ArticleContent, error) {
	// Make HTTP client with timeout
	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	// Get HTML page
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("error loading page: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	// Parse HTML
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error parsing HTML: %v", err)
	}

	// Get content by site
	content := extractContentBySource(doc, url)
	title := extractTitle(doc)

	if content == "" {
		return nil, fmt.Errorf("can't get content")
	}

	return &ArticleContent{
		Title:   title,
		Content: content,
		URL:     url,
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
			// Останавливаемся после первых 5 параграфов чтобы не захватить другие статьи
			if paragraphCount >= 5 {
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
			if len(paragraphs) >= 3 { // Максимум 3 параграфа для безопасности
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
func isNavigationOrOtherArticle(text string) bool {
	lowerText := strings.ToLower(text)

	// Навигационные элементы
	navIndicators := []string{
		"læs også", "se også", "følg", "cookie", "gdpr",
		"abonnement", "privatlivspolitik", "nyhedsbrev",
		"log ind", "opret", "del artikel", "print",
		"reklame", "annonce", "sponsor", "opdateret",
		"redigeret", "publiceret", "dr nyheder",
	}

	// Признаки других статей (международные новости, кото��ые не относятся к Дании)
	otherArticleIndicators := []string{
		"den russiske præsident", "vladimir putin", "kim jong-un",
		"nordkoreas leder", "kinas hovedstad", "beijing",
		"militærparade", "anden verdenskrig", "jeffrey epstein",
		"amerikanske kongres", "føderale efterforskning",
		"sexforbryder", "dokumenter fra", "undersøger",
	}

	allIndicators := append(navIndicators, otherArticleIndicators...)

	for _, indicator := range allIndicators {
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
		doc.Find(selector).Each(func(i int, s *goquery.Selection) {
			text := strings.TrimSpace(s.Text())
			if text != "" && len(text) > 20 {
				paragraphs = append(paragraphs, text)
			}
		})
		if len(paragraphs) >= 3 { // If we find 3 paragraphs, it's enough
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

	// Remove HTML tags
	content = strings.ReplaceAll(content, "<br>", " ")
	content = strings.ReplaceAll(content, "<br/>", " ")
	content = strings.ReplaceAll(content, "<p>", "\n\n")
	content = strings.ReplaceAll(content, "</p>", "")

	// Remove other HTML tags
	inTag := false
	var result strings.Builder
	for _, char := range content {
		if char == '<' {
			inTag = true
		} else if char == '>' {
			inTag = false
		} else if !inTag {
			result.WriteRune(char)
		}
	}

	content = strings.TrimSpace(result.String())

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
	if len(resultText) > 1800 {
		paragraphs := strings.Split(resultText, "\n\n")
		var selectedParagraphs []string
		totalLength := 0

		for _, paragraph := range paragraphs {
			if totalLength+len(paragraph) < 1600 {
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

// ExtractArticlesInBackground gets full content of articles in background
func ExtractArticlesInBackground(urls []string) map[string]*ArticleContent {
	result := make(map[string]*ArticleContent)

	for i, url := range urls {
		if i >= 5 { // Limit to 5 articles, don't overload
			break
		}

		log.Printf("Getting full content of article %d/%d: %s", i+1, len(urls), url)

		article, err := ExtractFullArticle(url)
		if err != nil {
			log.Printf("⚠️ Can't get content %s: %v", url, err)
			continue
		}

		if len(article.Content) > 100 { // Check content is not empty
			result[url] = article
			log.Printf("✅ Got content (%d chars)", len(article.Content))
		} else {
			log.Printf("⚠️ Content too short: %s", url)
		}

		// Small pause between requests, don't overload sites
		time.Sleep(500 * time.Millisecond)
	}

	return result
}

// ExtractImageURL fetches a page and tries to detect a representative image (og:image/twitter:image)
func ExtractImageURL(pageURL string) (string, error) {
	if strings.TrimSpace(pageURL) == "" {
		return "", fmt.Errorf("empty url")
	}

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Get(pageURL)
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
	if v, ok := doc.Find(`meta[property="og:image"]`).Attr("content"); ok {
		img := resolve(v)
		if isLikelyImage(img) {
			return img, nil
		}
	}
	if v, ok := doc.Find(`meta[property="og:image:secure_url"]`).Attr("content"); ok {
		img := resolve(v)
		if isLikelyImage(img) {
			return img, nil
		}
	}

	// Priority 2: twitter:image
	if v, ok := doc.Find(`meta[name="twitter:image"], meta[name="twitter:image:src"]`).Attr("content"); ok {
		img := resolve(v)
		if isLikelyImage(img) {
			return img, nil
		}
	}

	// Priority 3: link rel=image_src
	if v, ok := doc.Find(`link[rel="image_src"]`).Attr("href"); ok {
		img := resolve(v)
		if isLikelyImage(img) {
			return img, nil
		}
	}

	// Fallback: first <img> in main/article
	sel := []string{"article img", "main img", "img"}
	for _, s := range sel {
		if n := doc.Find(s).First(); n != nil && n.Length() > 0 {
			if v, ok := n.Attr("src"); ok {
				img := resolve(v)
				if isLikelyImage(img) {
					return img, nil
				}
			}
		}
	}

	return "", nil
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
	// Allow URLs without extension but with image providers
	if strings.Contains(u, "/images/") || strings.Contains(u, "cdn") {
		return true
	}
	return false
}
