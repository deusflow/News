package scraper

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/rss"
	"github.com/mmcdole/gofeed"
)

// NyidanmarkScraper извлекает официальные новости с nyidanmark.dk
type NyidanmarkScraper struct {
	client *http.Client
}

func NewNyidanmarkScraper() *NyidanmarkScraper {
	return &NyidanmarkScraper{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// ScrapeFrontpage находит ссылки на последние новости на главной странице (они там рендерятся сервером)
func (s *NyidanmarkScraper) ScrapeFrontpage(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://www.nyidanmark.dk/da/", nil)
	if err != nil {
		return nil, err
	}
	// Важно: nyidanmark.dk может блокировать пустые User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to load nyidanmark frontpage: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse html: %w", err)
	}

	var links []string
	doc.Find("a").Each(func(i int, sel *goquery.Selection) {
		href, exists := sel.Attr("href")
		if exists && strings.HasPrefix(href, "/da/Nyheder/") {
			fullURL := "https://www.nyidanmark.dk" + href
			links = append(links, fullURL)
		}
	})

	// Уникализация ссылок
	seen := make(map[string]bool)
	var uniqueLinks []string
	for _, link := range links {
		if !seen[link] {
			seen[link] = true
			uniqueLinks = append(uniqueLinks, link)
		}
	}

	return uniqueLinks, nil
}

// ScrapeArticle загружает контент конкретной новости
func (s *NyidanmarkScraper) ScrapeArticle(ctx context.Context, url string) (*rss.FeedItem, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to load article: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse article html: %w", err)
	}

	title := strings.TrimSpace(doc.Find("h1#news-headline").Text())
	if title == "" {
		return nil, fmt.Errorf("could not find title (h1#news-headline)")
	}

	manchet := strings.TrimSpace(doc.Find(".article-manchet").Text())
	
	var paragraphs []string
	doc.Find("article.article-content p.bodycopy-small, article.article-content h3").Each(func(i int, sel *goquery.Selection) {
		text := strings.TrimSpace(sel.Text())
		if text != "" {
			paragraphs = append(paragraphs, text)
		}
	})

	content := manchet + "\n\n" + strings.Join(paragraphs, "\n\n")

	// Парсинг даты (Publiceret 17-06-2026)
	pubDateStr := strings.TrimSpace(doc.Find("p.bodycopy-xsmall.black").First().Text())
	pubDateStr = strings.ReplaceAll(pubDateStr, "Publiceret ", "")
	
	var pubDate time.Time
	if pubDateStr != "" {
		parsed, err := time.Parse("02-01-2006", pubDateStr)
		if err == nil {
			pubDate = parsed
		} else {
			logger.Warn("Failed to parse nyidanmark date", "date_str", pubDateStr, "err", err)
			pubDate = time.Now()
		}
	} else {
		pubDate = time.Now()
	}

	feedSource := &rss.FeedSource{
		Name:     "Nyidanmark",
		Lang:     "da",
		Priority: 10,
	}

	return &rss.FeedItem{
		Item: &gofeed.Item{
			Title:           title,
			Link:            url,
			Description:     manchet,
			Content:         content,
			Published:       pubDateStr,
			PublishedParsed: &pubDate,
		},
		Source: feedSource,
	}, nil
}
