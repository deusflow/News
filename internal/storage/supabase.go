package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Retry configuration for Supabase requests
const (
	maxRetries     = 3
	retryBaseDelay = 2 * time.Second
	retryMaxDelay  = 10 * time.Second
)

// isRetryableError checks if the HTTP status code is retryable (server errors, gateway issues)
func isRetryableError(statusCode int) bool {
	return statusCode == 502 || // Bad Gateway
		statusCode == 503 || // Service Unavailable
		statusCode == 504 || // Gateway Timeout
		statusCode == 429 || // Too Many Requests
		statusCode == 500 // Internal Server Error
}

// retryableRequest executes an HTTP request with retry logic for transient errors
func (c *SupabaseClient) retryableRequest(method, url string, body []byte) (*http.Response, error) {
	var lastErr error
	var resp *http.Response

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Create new request for each attempt (body reader needs to be fresh)
		var reqBody io.Reader
		if body != nil {
			reqBody = bytes.NewBuffer(body)
		}

		req, err := http.NewRequest(method, url, reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		// Set headers
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("apikey", c.serviceKey)
		req.Header.Set("Authorization", "Bearer "+c.serviceKey)
		if method == "POST" {
			req.Header.Set("Prefer", "return=minimal")
		}

		// Execute request
		resp, err = c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("⚠️ Supabase request failed (attempt %d/%d): %v", attempt+1, maxRetries, err)

			// Wait before retry with exponential backoff
			delay := retryBaseDelay * time.Duration(1<<attempt)
			if delay > retryMaxDelay {
				delay = retryMaxDelay
			}
			time.Sleep(delay)
			continue
		}

		// Check if response is retryable
		if isRetryableError(resp.StatusCode) {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("supabase error (status %d): %s", resp.StatusCode, string(respBody))
			log.Printf("⚠️ Supabase returned %d (attempt %d/%d): %s", resp.StatusCode, attempt+1, maxRetries, string(respBody))

			// Wait before retry with exponential backoff
			delay := retryBaseDelay * time.Duration(1<<attempt)
			if delay > retryMaxDelay {
				delay = retryMaxDelay
			}
			time.Sleep(delay)
			continue
		}

		// Success or non-retryable error
		return resp, nil
	}

	return nil, fmt.Errorf("supabase request failed after %d retries: %w", maxRetries, lastErr)
}

// SupabaseClient handles interactions with Supabase for website archive
type SupabaseClient struct {
	url        string
	serviceKey string
	httpClient *http.Client
}

// NewsArchive represents a news item in Supabase
type NewsArchive struct {
	ID               int       `json:"id,omitempty"`
	Slug             string    `json:"slug"`
	Title            string    `json:"title"`
	TitleUkrainian   string    `json:"title_ukrainian,omitempty"`
	SummaryUkrainian string    `json:"summary_ukrainian,omitempty"`
	SummaryDanish    string    `json:"summary_danish,omitempty"`
	TLDR             string    `json:"tldr,omitempty"`
	FunFact          string    `json:"fun_fact,omitempty"`
	ImageURL         string    `json:"image_url,omitempty"`
	SourceURL        string    `json:"source_url,omitempty"`
	SourceName       string    `json:"source_name,omitempty"`
	Category         string    `json:"category,omitempty"`
	Tags             []string  `json:"tags,omitempty"`
	Mood             string    `json:"mood,omitempty"`
	PublishedAt      time.Time `json:"published_at"`
	CreatedAt        time.Time `json:"created_at,omitempty"`
	IsArchived       bool      `json:"is_archived,omitempty"`
}

// NewSupabaseClient creates a new Supabase client
func NewSupabaseClient(url, serviceKey string) (*SupabaseClient, error) {
	if url == "" || serviceKey == "" {
		return nil, fmt.Errorf("supabase URL and service key are required")
	}

	// Remove trailing slash from URL
	url = strings.TrimSuffix(url, "/")

	return &SupabaseClient{
		url:        url,
		serviceKey: serviceKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// SaveNews saves a news item to Supabase with retry logic for transient errors
func (c *SupabaseClient) SaveNews(news NewsArchive) error {
	// Step 1: Check by source_url (most reliable)
	isDuplicateURL, err := c.IsDuplicateBySourceURL(news.SourceURL)
	if err != nil {
		log.Printf("Warning: source_url duplicate check failed: %v", err)
	}
	if isDuplicateURL {
		log.Printf("⏭️ Skipping duplicate (same source_url): %s", news.SourceURL)
		return nil
	}

	// Step 2: Check by similar title (backup check)
	isDuplicate, err := c.IsDuplicateNews(news.Title)
	if err != nil {
		log.Printf("Warning: title duplicate check failed: %v", err)
	}
	if isDuplicate {
		log.Printf("⏭️ Skipping duplicate (similar title): %s", news.Title)
		return nil
	}

	// Set default category
	if news.Category == "" {
		news.Category = "News"
	}

	// Prepare JSON body
	body, err := json.Marshal(news)
	if err != nil {
		return fmt.Errorf("failed to marshal news: %w", err)
	}

	// Execute request with retry logic
	reqURL := fmt.Sprintf("%s/rest/v1/news_archive", c.url)
	resp, err := c.retryableRequest("POST", reqURL, body)
	if err != nil {
		return fmt.Errorf("failed to save news after retries: %w", err)
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		// Check if it's a duplicate (slug already exists)
		if resp.StatusCode == http.StatusConflict || strings.Contains(string(respBody), "duplicate") {
			return nil // Duplicate is OK, just skip
		}
		return fmt.Errorf("supabase error (status %d): %s", resp.StatusCode, string(respBody))
	}

	log.Printf("✅ News saved to Supabase: %s", news.Title)
	return nil
}

// IsDuplicateNews checks if a similar news already exists in Supabase
// Has a 2 second timeout to prevent blocking the bot
func (c *SupabaseClient) IsDuplicateNews(title string) (bool, error) {
	// Create a channel for the result
	type result struct {
		isDuplicate bool
		err         error
	}
	resultChan := make(chan result, 1)

	go func() {
		isDup, err := c.checkDuplicateInternal(title)
		resultChan <- result{isDup, err}
	}()

	// Wait for result with 2 second timeout
	select {
	case res := <-resultChan:
		return res.isDuplicate, res.err
	case <-time.After(2 * time.Second):
		fmt.Println("⚠️ Supabase duplicate check timeout (2s), skipping check")
		return false, nil // On timeout, allow the news (don't block)
	}
}

// checkDuplicateInternal performs the actual duplicate check
func (c *SupabaseClient) checkDuplicateInternal(title string) (bool, error) {
	// Normalize title for comparison
	normalizedTitle := normalizeTitle(title)

	// Get recent news (last 24 hours) to check for duplicates
	oneDayAgo := time.Now().AddDate(0, 0, -1).Format(time.RFC3339)

	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?published_at=gte.%s&select=title",
		c.url, oneDayAgo)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return false, err
	}

	req.Header.Set("apikey", c.serviceKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("failed to fetch news for duplicate check")
	}

	var existingNews []struct {
		Title string `json:"title"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&existingNews); err != nil {
		return false, err
	}

	// Check if any existing title is similar (>70% match)
	for _, existing := range existingNews {
		existingNormalized := normalizeTitle(existing.Title)
		if isSimilarTitle(normalizedTitle, existingNormalized) {
			return true, nil
		}
	}

	return false, nil
}

// IsDuplicateBySourceURL checks if news with the same source_url already exists
// This is more reliable than title comparison because URLs are unique per news
func (c *SupabaseClient) IsDuplicateBySourceURL(sourceURL string) (bool, error) {
	// Validate input
	if sourceURL == "" {
		return false, nil // No URL to check, allow the news
	}

	// Create result channel for timeout handling
	type result struct {
		isDuplicate bool
		err         error
	}
	resultChan := make(chan result, 1)

	// Run check in goroutine (for timeout)
	go func() {
		// Build request URL with filter
		// eq. means "equals" in Supabase query language
		reqURL := fmt.Sprintf("%s/rest/v1/news_archive?source_url=eq.%s&select=id",
			c.url,
			url.QueryEscape(sourceURL)) // Escape special characters in URL

		req, err := http.NewRequest("GET", reqURL, nil)
		if err != nil {
			resultChan <- result{false, err}
			return
		}

		// Add authentication headers
		req.Header.Set("apikey", c.serviceKey)
		req.Header.Set("Authorization", "Bearer "+c.serviceKey)

		// Execute request
		resp, err := c.httpClient.Do(req)
		if err != nil {
			resultChan <- result{false, err}
			return
		}
		defer resp.Body.Close()

		// Parse response
		var existing []struct {
			ID int `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&existing); err != nil {
			resultChan <- result{false, err}
			return
		}

		// If we found any records, it's a duplicate
		resultChan <- result{len(existing) > 0, nil}
	}()

	// Wait with 2 second timeout
	select {
	case res := <-resultChan:
		return res.isDuplicate, res.err
	case <-time.After(2 * time.Second):
		log.Println("⚠️ Source URL duplicate check timeout (2s), allowing news")
		return false, nil
	}
}

// normalizeTitle removes common variations to compare titles
func normalizeTitle(title string) string {
	// Convert to lowercase
	title = strings.ToLower(title)

	// Remove punctuation
	re := regexp.MustCompile(`[^\p{L}\p{N}\s]`)
	title = re.ReplaceAllString(title, "")

	// Remove extra whitespace
	title = strings.Join(strings.Fields(title), " ")

	return title
}

// isSimilarTitle checks if two normalized titles are similar enough to be duplicates
func isSimilarTitle(title1, title2 string) bool {
	// If titles are exactly the same
	if title1 == title2 {
		return true
	}

	// Check if one contains the other (common for different sources)
	if strings.Contains(title1, title2) || strings.Contains(title2, title1) {
		return true
	}

	// Calculate word overlap
	words1 := strings.Fields(title1)
	words2 := strings.Fields(title2)

	if len(words1) == 0 || len(words2) == 0 {
		return false
	}

	// Count common words
	wordSet := make(map[string]bool)
	for _, w := range words1 {
		if len(w) > 2 { // Skip very short words
			wordSet[w] = true
		}
	}

	commonWords := 0
	for _, w := range words2 {
		if len(w) > 2 && wordSet[w] {
			commonWords++
		}
	}

	// Calculate similarity ratio
	minLen := len(words1)
	if len(words2) < minLen {
		minLen = len(words2)
	}

	// If more than 70% of words match, consider it a duplicate
	similarity := float64(commonWords) / float64(minLen)
	return similarity >= 0.7
}

// GetActiveNews retrieves non-archived news from the last 10 days
func (c *SupabaseClient) GetActiveNews(limit int) ([]NewsArchive, error) {
	tenDaysAgo := time.Now().AddDate(0, 0, -10).Format(time.RFC3339)

	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?is_archived=eq.false&published_at=gte.%s&order=published_at.desc&limit=%d",
		c.url, tenDaysAgo, limit)

	return c.fetchNews(reqURL)
}

// GetNewsByDate retrieves news for a specific date
func (c *SupabaseClient) GetNewsByDate(date time.Time) ([]NewsArchive, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?published_at=gte.%s&published_at=lt.%s&order=published_at.desc",
		c.url, startOfDay.Format(time.RFC3339), endOfDay.Format(time.RFC3339))

	return c.fetchNews(reqURL)
}

// GetArchivedNews retrieves archived news (older than 10 days)
func (c *SupabaseClient) GetArchivedNews(limit, offset int) ([]NewsArchive, error) {
	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?is_archived=eq.true&order=published_at.desc&limit=%d&offset=%d",
		c.url, limit, offset)

	return c.fetchNews(reqURL)
}

// GetCarouselNews retrieves random news for the carousel
func (c *SupabaseClient) GetCarouselNews(limit int) ([]NewsArchive, error) {
	// Supabase doesn't support ORDER BY RANDOM(), so we fetch more and randomize in Go
	// Or use the active news endpoint
	return c.GetActiveNews(limit)
}

// GetTrendingNews retrieves the latest N news items
func (c *SupabaseClient) GetTrendingNews(limit int) ([]NewsArchive, error) {
	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?is_archived=eq.false&order=published_at.desc&limit=%d",
		c.url, limit)

	return c.fetchNews(reqURL)
}

// ArchiveOldNews marks news older than 10 days as archived
func (c *SupabaseClient) ArchiveOldNews() error {
	tenDaysAgo := time.Now().AddDate(0, 0, -10).Format(time.RFC3339)

	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?is_archived=eq.false&published_at=lt.%s",
		c.url, tenDaysAgo)

	body := []byte(`{"is_archived": true}`)

	req, err := http.NewRequest("PATCH", reqURL, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", c.serviceKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("Prefer", "return=minimal")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("supabase error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// fetchNews is a helper to fetch news from a URL
func (c *SupabaseClient) fetchNews(reqURL string) ([]NewsArchive, error) {
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("apikey", c.serviceKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("supabase error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var news []NewsArchive
	if err := json.NewDecoder(resp.Body).Decode(&news); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return news, nil
}

// Ping checks if Supabase is reachable
func (c *SupabaseClient) Ping() error {
	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?limit=1", c.url)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return err
	}

	req.Header.Set("apikey", c.serviceKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("supabase ping failed with status %d", resp.StatusCode)
	}

	return nil
}

// GenerateSlug creates a URL-friendly slug from title
// Uses published date for uniqueness instead of current time
func GenerateSlug(title string) string {
	return GenerateSlugWithDate(title, time.Now())
}

// GenerateSlugWithDate creates a URL-friendly slug from title with specific date
func GenerateSlugWithDate(title string, publishedAt time.Time) string {
	// Normalize unicode
	title = norm.NFC.String(title)

	// Convert to lowercase
	title = strings.ToLower(title)

	// Replace special characters with transliterations
	replacements := map[string]string{
		"æ": "ae", "ø": "oe", "å": "aa",
		"ä": "ae", "ö": "oe", "ü": "ue",
		"і": "i", "ї": "yi", "є": "ye",
		"а": "a", "б": "b", "в": "v", "г": "h", "ґ": "g",
		"д": "d", "е": "e", "ж": "zh", "з": "z", "и": "y",
		"й": "y", "к": "k", "л": "l", "м": "m", "н": "n",
		"о": "o", "п": "p", "р": "r", "с": "s", "т": "t",
		"у": "u", "ф": "f", "х": "kh", "ц": "ts", "ч": "ch",
		"ш": "sh", "щ": "shch", "ь": "", "ю": "yu", "я": "ya",
		"ё": "yo", "э": "e", "ы": "y",
	}

	for from, to := range replacements {
		title = strings.ReplaceAll(title, from, to)
	}

	// Remove non-alphanumeric characters (keep spaces and hyphens)
	var result strings.Builder
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			result.WriteRune(r)
		} else if r == ' ' || r == '-' {
			result.WriteRune('-')
		}
	}

	slug := result.String()

	// Replace multiple hyphens with single hyphen
	re := regexp.MustCompile(`-+`)
	slug = re.ReplaceAllString(slug, "-")

	// Trim hyphens from start and end
	slug = strings.Trim(slug, "-")

	// Limit length
	if len(slug) > 60 {
		slug = slug[:60]
		// Don't cut in the middle of a word
		if lastHyphen := strings.LastIndex(slug, "-"); lastHyphen > 30 {
			slug = slug[:lastHyphen]
		}
	}

	// Add date suffix based on PUBLISHED date (not current time!)
	// This ensures the same news always gets the same slug
	dateSuffix := publishedAt.Format("20060102")
	slug = fmt.Sprintf("%s-%s", slug, dateSuffix)

	return slug
}
