package rss

import (
	"github.com/mmcdole/gofeed"
	"gopkg.in/yaml.v3"
	"log"
	"os"
)

// FeedsConfig is YAML config structure
// feeds:
//   - https://...
type FeedsConfig struct {
	Feeds []string `yaml:"feeds"`
}

// LoadFeeds reads RSS feeds list from YAML file
func LoadFeeds(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg FeedsConfig
	dec := yaml.NewDecoder(f)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	return cfg.Feeds, nil
}

// FetchAllFeeds downloads and parses all feeds, returns news list
func FetchAllFeeds(urls []string) ([]*gofeed.Item, error) {
	parser := gofeed.NewParser()
	var allItems []*gofeed.Item
	successCount := 0

	for _, url := range urls {
		feed, err := parser.ParseURL(url)
		if err != nil {
			log.Printf("Error parsing RSS %s: %v", url, err)
			continue // Log error, but don't stop
		}
		allItems = append(allItems, feed.Items...)
		successCount++
		log.Printf("Loaded %d news from %s", len(feed.Items), url)
	}

	log.Printf("Processed RSS feeds: %d/%d ok", successCount, len(urls))
	return allItems, nil
}
