package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	_ "github.com/lib/pq"
)

func main() {
	// Read .env manually
	envBytes, err := os.ReadFile(".env")
	if err != nil {
		log.Fatalf("failed to read .env: %v", err)
	}

	var dbURL string
	for _, line := range strings.Split(string(envBytes), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "DATABASE_URL=") {
			dbURL = strings.TrimPrefix(line, "DATABASE_URL=")
			break
		}
	}

	if dbURL == "" {
		log.Fatal("DATABASE_URL not found in .env")
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("failed to ping db: %v", err)
	}
	fmt.Println("✅ Connected to Neon PostgreSQL successfully!")

	// Query last 15 sent news
	fmt.Println("\n--- Last 15 Sent News ---")
	rows, err := db.Query("SELECT title, sent_at, category, source FROM sent_news ORDER BY sent_at DESC LIMIT 15")
	if err != nil {
		log.Fatalf("query sent_news failed: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var title, category, source string
		var sentAt string
		if err := rows.Scan(&title, &sentAt, &category, &source); err != nil {
			log.Fatalf("scan failed: %v", err)
		}
		fmt.Printf("[%s] (%s / %s) %s\n", sentAt, category, source, title)
	}
}
