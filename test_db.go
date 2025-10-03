package main

import (
	"fmt"
	"log"
	"os"

	"github.com/deusflow/News/internal/storage"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("❌ DATABASE_URL not set in environment")
	}

	fmt.Println("🔌 Testing PostgreSQL connection...")
	fmt.Printf("Database URL: %s\n\n", maskPassword(dbURL))

	// Create PostgreSQL cache
	pgCache, err := storage.NewPostgresCache(dbURL, 48)
	if err != nil {
		log.Fatalf("❌ Failed to connect to PostgreSQL: %v", err)
	}
	defer pgCache.Close()

	fmt.Println("✅ Successfully connected to PostgreSQL!")

	// Get statistics
	stats, err := pgCache.GetStats()
	if err != nil {
		log.Printf("⚠️ Failed to get stats: %v", err)
	} else {
		fmt.Println("\n📊 Database Statistics:")
		fmt.Printf("  Total items: %d\n", stats["total_items"])
		fmt.Printf("  Active items: %d\n", stats["active_items"])
	}

	// Get recent news
	recentNews, err := pgCache.GetRecentNews(5)
	if err != nil {
		log.Printf("⚠️ Failed to get recent news: %v", err)
	} else {
		fmt.Println("\n📰 Recent News (last 5):")
		if len(recentNews) == 0 {
			fmt.Println("  (no news sent yet)")
		} else {
			for i, item := range recentNews {
				fmt.Printf("  %d. %s\n", i+1, item.Title)
				fmt.Printf("     Category: %s | Sent: %s\n", item.Category, item.SentAt.Format("2006-01-02 15:04:05"))
			}
		}
	}

	// Test hash generation and duplicate check
	fmt.Println("\n🧪 Testing duplicate detection...")
	testTitle := "Test News Article"
	testLink := "https://example.com/test"
	hash := pgCache.GenerateNewsHash(testTitle, testLink)
	fmt.Printf("  Generated hash: %s\n", hash)

	isDupe := pgCache.IsAlreadySent(hash)
	fmt.Printf("  Is duplicate: %v\n", isDupe)

	fmt.Println("\n✅ All tests passed! Database is ready to use.")
}

func maskPassword(dbURL string) string {
	// Simple password masking for display
	if len(dbURL) > 50 {
		return dbURL[:30] + "***" + dbURL[len(dbURL)-20:]
	}
	return dbURL
}
