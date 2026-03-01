package rss

import (
	"context"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
	"gopkg.in/yaml.v3"
)

// FeedSource represents a single RSS feed source with metadata
type FeedSource struct {
	URL        string   `yaml:"url"`
	Name       string   `yaml:"name"`
	Lang       string   `yaml:"lang"`
	Priority   int      `yaml:"priority"`
	Active     bool     `yaml:"active"`
	Categories []string `yaml:"categories"`
}

// FeedsConfig is YAML config structure for extended feeds format
type FeedsConfig struct {
	Feeds []FeedSource `yaml:"feeds"`
}

// FeedItem wraps gofeed.Item with source metadata
type FeedItem struct {
	*gofeed.Item
	Source *FeedSource
}

// LoadFeeds reads RSS feeds list from YAML file
func LoadFeeds(path string) ([]FeedSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			log.Printf("Warning: failed to close file %s: %v", path, closeErr)
		}
	}()

	var cfg FeedsConfig
	dec := yaml.NewDecoder(f)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	return cfg.Feeds, nil
}

// rssHTTPClient is shared across all feed fetches to reuse connections.
var rssHTTPClient = &http.Client{Timeout: 30 * time.Second}

// FetchAllFeeds downloads and parses all active feeds concurrently.
// The context is forwarded to every HTTP request — cancelling it (e.g. on
// SIGTERM) will stop in-flight feed fetches immediately.
func FetchAllFeeds(ctx context.Context, sources []FeedSource) ([]*FeedItem, error) {
	var allItems []*FeedItem
	var mu sync.Mutex
	var wg sync.WaitGroup
	successCount := 0

	// Limit concurrency to 5 parallel requests
	sem := make(chan struct{}, 5)

	for _, source := range sources {
		if !source.Active {
			log.Printf("Skipping inactive feed: %s", source.Name)
			continue
		}

		// Don't start new fetches if context is already cancelled
		if ctx.Err() != nil {
			break
		}

		wg.Add(1)
		go func(s FeedSource) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			parser := gofeed.NewParser()
			parser.Client = rssHTTPClient

			feed, err := parser.ParseURLWithContext(s.URL, ctx)
			if err != nil {
				if ctx.Err() != nil {
					log.Printf("Feed fetch cancelled for %s: %v", s.Name, ctx.Err())
				} else {
					log.Printf("Error parsing RSS %s (%s): %v", s.URL, s.Name, err)
				}
				return
			}

			mu.Lock()
			defer mu.Unlock()
			successCount++
			for _, item := range feed.Items {
				allItems = append(allItems, &FeedItem{Item: item, Source: &s})
			}
			log.Printf("Loaded %d news from %s (%s)", len(feed.Items), s.Name, s.URL)
		}(source)
	}

	wg.Wait()
	log.Printf("Processed RSS feeds: %d/%d ok", successCount, len(sources))
	return allItems, nil
}
