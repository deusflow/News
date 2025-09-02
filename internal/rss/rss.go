package rss

import (
	"github.com/mmcdole/gofeed"
	"gopkg.in/yaml.v3"
	"log"
	"os"
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
	Source FeedSource
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
	parser := gofeed.NewParser()
	var allItems []*FeedItem
	successCount := 0

	for _, source := range sources {
		if !source.Active {
			log.Printf("Skipping inactive feed: %s", source.Name)
			continue
		}

		feed, err := parser.ParseURL(source.URL)
		if err != nil {
			log.Printf("Error parsing RSS %s (%s): %v", source.URL, source.Name, err)
			continue // Log error, but don't stop
		}

		// Wrap each item with source metadata
		for _, item := range feed.Items {
			feedItem := &FeedItem{
				Item:   item,
				Source: source,
			}
			allItems = append(allItems, feedItem)
		}

		successCount++
		log.Printf("Loaded %d news from %s (%s)", len(feed.Items), source.Name, source.URL)
	}

	log.Printf("Processed RSS feeds: %d/%d ok", successCount, len(sources))
	return allItems, nil
}
