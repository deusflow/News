package main

import (
	"context"
	"fmt"
	"github.com/deusflow/News/internal/scraper"
)

func main() {
	s := scraper.NewNyidanmarkScraper()
	links, err := s.ScrapeFrontpage(context.Background())
	if err != nil {
		fmt.Println("Error scraping frontpage:", err)
		return
	}
	fmt.Printf("Found %d links\n", len(links))
	for _, link := range links {
		fmt.Println(link)
		item, err := s.ScrapeArticle(context.Background(), link)
		if err != nil {
			fmt.Println("  Error scraping article:", err)
		} else {
			fmt.Printf("  Title: %s\n  Date: %v\n", item.Item.Title, item.Item.PublishedParsed)
		}
	}
}
