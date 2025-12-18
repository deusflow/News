package rss

import (
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

// FetchAllFeeds downloads and parses all feeds, returns news list with source metadata
func FetchAllFeeds(sources []FeedSource) ([]*FeedItem, error) {
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

		wg.Add(1)
		go func(s FeedSource) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }() // Release semaphore

			// Create a new parser for each goroutine to ensure thread safety
			parser := gofeed.NewParser()
			// Set a timeout to prevent hanging if a feed is slow
			parser.Client = &http.Client{
				Timeout: 30 * time.Second,
			}

			feed, err := parser.ParseURL(s.URL)
			if err != nil {
				log.Printf("Error parsing RSS %s (%s): %v", s.URL, s.Name, err)
				return
			}

			mu.Lock()
			defer mu.Unlock()
			successCount++
			// Wrap each item with source metadata
			for _, item := range feed.Items {
				feedItem := &FeedItem{
					Item:   item,
					Source: &s,
				}
				allItems = append(allItems, feedItem)
			}
			log.Printf("Loaded %d news from %s (%s)", len(feed.Items), s.Name, s.URL)
		}(source)
	}

	wg.Wait()
	log.Printf("Processed RSS feeds: %d/%d ok", successCount, len(sources))
	return allItems, nil
}
