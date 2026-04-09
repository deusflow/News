package rss

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/deusflow/News/internal/logger"
	"github.com/mmcdole/gofeed"
	"gopkg.in/yaml.v3"
)

// FeedSource represents a single RSS feed source with metadata
type FeedSource struct {
	URL        string   `yaml:"url"`
	Name       string   `yaml:"name"`
	Lang       string   `yaml:"lang"`
	Priority   int      `yaml:"priority"`
	Weight     int      `yaml:"weight"`
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
			logger.Warn("Failed to close feeds config file", "path", path, "error", closeErr)
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
			logger.Info("Skipping inactive feed", "source", source.Name)
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
					logger.Info("Feed fetch cancelled", "source", s.Name, "error", ctx.Err())
				} else {
					logger.Warn("Error parsing RSS feed", "url", s.URL, "source", s.Name, "error", err)
				}
				return
			}

			mu.Lock()
			defer mu.Unlock()
			successCount++
			for _, item := range feed.Items {
				allItems = append(allItems, &FeedItem{Item: item, Source: &s})
			}
			logger.Info("Loaded RSS items", "count", len(feed.Items), "source", s.Name, "url", s.URL)
		}(source)
	}

	wg.Wait()
	logger.Info("Processed RSS feeds", "success", successCount, "total", len(sources))

	if successCount == 0 && len(sources) > 0 {
		return nil, fmt.Errorf("all %d RSS feeds failed, no news fetched", len(sources))
	}

	return allItems, nil
}
