package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/slugify"
)

// SupabaseClientOptions tunes HTTP behavior without changing defaults.
type SupabaseClientOptions struct {
	HTTPTimeout           time.Duration
	DuplicateCheckTimeout time.Duration
	MaxRetries            int
	RetryBaseDelay        time.Duration
	RetryMaxDelay         time.Duration
}

func defaultSupabaseClientOptions() SupabaseClientOptions {
	return SupabaseClientOptions{
		HTTPTimeout:           30 * time.Second,
		DuplicateCheckTimeout: 2 * time.Second,
		MaxRetries:            3,
		RetryBaseDelay:        2 * time.Second,
		RetryMaxDelay:         10 * time.Second,
	}
}

// isRetryableError checks if the HTTP status code is retryable (server errors, gateway issues)
func isRetryableError(statusCode int) bool {
	return statusCode == 502 || // Bad Gateway
		statusCode == 503 || // Service Unavailable
		statusCode == 504 || // Gateway Timeout
		statusCode == 429 || // Too Many Requests
		statusCode == 500 // Internal Server Error
}

// retryableRequest executes an HTTP request with retry logic for transient errors.
// ctx is forwarded to every request so SIGTERM cancels in-flight Supabase calls.
func (c *SupabaseClient) retryableRequest(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	var lastErr error
	var resp *http.Response

	for attempt := 0; attempt < c.options.MaxRetries; attempt++ {
		// Bail out immediately if context is already cancelled
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		var reqBody io.Reader
		if body != nil {
			reqBody = bytes.NewBuffer(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("apikey", c.serviceKey)
		req.Header.Set("Authorization", "Bearer "+c.serviceKey)
		if method == "POST" {
			req.Header.Set("Prefer", "return=minimal")
		}

		resp, err = c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			logger.Warn("Supabase request failed", "attempt", attempt+1, "max_attempts", c.options.MaxRetries, "error", err)

			delay := c.options.RetryBaseDelay * time.Duration(1<<attempt)
			if delay > c.options.RetryMaxDelay {
				delay = c.options.RetryMaxDelay
			}

			// Context-aware sleep: stop waiting if shutdown is signalled
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			}
			continue
		}

		// Check if response is retryable
		if isRetryableError(resp.StatusCode) {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("supabase error (status %d): %s", resp.StatusCode, string(respBody))
			logger.Warn("Supabase retryable response", "status", resp.StatusCode, "attempt", attempt+1, "max_attempts", c.options.MaxRetries, "body", string(respBody))

			delay := c.options.RetryBaseDelay * time.Duration(1<<attempt)
			if delay > c.options.RetryMaxDelay {
				delay = c.options.RetryMaxDelay
			}

			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			}
			continue
		}

		// Success or non-retryable error
		return resp, nil
	}

	return nil, fmt.Errorf("supabase request failed after %d retries: %w", c.options.MaxRetries, lastErr)
}

// SupabaseClient handles interactions with Supabase for website archive
type SupabaseClient struct {
	url        string
	serviceKey string
	httpClient *http.Client
	options    SupabaseClientOptions
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
	WhyItMatters     string    `json:"why_it_matters,omitempty"`
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
	return NewSupabaseClientWithOptions(url, serviceKey, defaultSupabaseClientOptions())
}

// NewSupabaseClientWithOptions creates a new Supabase client with custom retry/timeouts.
func NewSupabaseClientWithOptions(url, serviceKey string, options SupabaseClientOptions) (*SupabaseClient, error) {
	if url == "" || serviceKey == "" {
		return nil, fmt.Errorf("supabase URL and service key are required")
	}

	// Remove trailing slash from URL
	url = strings.TrimSuffix(url, "/")

	if options.HTTPTimeout <= 0 {
		options.HTTPTimeout = 30 * time.Second
	}
	if options.DuplicateCheckTimeout <= 0 {
		options.DuplicateCheckTimeout = 2 * time.Second
	}
	if options.MaxRetries < 1 {
		options.MaxRetries = 1
	}
	if options.RetryBaseDelay <= 0 {
		options.RetryBaseDelay = 2 * time.Second
	}
	if options.RetryMaxDelay < options.RetryBaseDelay {
		options.RetryMaxDelay = options.RetryBaseDelay
	}

	return &SupabaseClient{
		url:        url,
		serviceKey: serviceKey,
		httpClient: &http.Client{
			Timeout: options.HTTPTimeout,
		},
		options: options,
	}, nil
}

// SaveNews saves a news item to Supabase.
// Duplicate check is NOT performed here — the caller is responsible for ensuring
// this news was not already sent (use PostgresCache.IsSourceURLSent before calling).
// On slug conflict Supabase returns 409 / "duplicate" which we treat as success.
func (c *SupabaseClient) SaveNews(ctx context.Context, news NewsArchive) error {
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
	resp, err := c.retryableRequest(ctx, "POST", reqURL, body)
	if err != nil {
		return fmt.Errorf("failed to save news after retries: %w", err)
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		// Slug conflict is fine — idempotent, the record is already there.
		if resp.StatusCode == http.StatusConflict || strings.Contains(string(respBody), "duplicate") {
			return nil
		}
		return fmt.Errorf("supabase error (status %d): %s", resp.StatusCode, string(respBody))
	}

	logger.Info("News saved to Supabase", "title", news.Title, "source_url", news.SourceURL)
	return nil
}

// IsDuplicateNews checks if a similar news already exists in Supabase.
// Uses a short configurable context timeout so the bot is never blocked by slow responses.
// The HTTP request is cancelled when the timeout fires — no goroutine leak.
func (c *SupabaseClient) IsDuplicateNews(title string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.options.DuplicateCheckTimeout)
	defer cancel()
	return c.checkDuplicateInternal(ctx, title)
}

// checkDuplicateInternal performs the actual duplicate check with a cancellable context.
func (c *SupabaseClient) checkDuplicateInternal(ctx context.Context, title string) (bool, error) {
	normalizedTitle := normalizeTitle(title)

	oneDayAgo := time.Now().AddDate(0, 0, -1).Format(time.RFC3339)
	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?published_at=gte.%s&select=title",
		c.url, oneDayAgo)

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return false, err
	}

	req.Header.Set("apikey", c.serviceKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			logger.Warn("supabase duplicate check timeout", "duration_sec", c.options.DuplicateCheckTimeout.Seconds())
			return false, nil // timeout → allow the news through
		}
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

	for _, existing := range existingNews {
		if isSimilarTitle(normalizedTitle, normalizeTitle(existing.Title)) {
			return true, nil
		}
	}

	return false, nil
}

// IsDuplicateBySourceURL checks if news with the same source_url already exists.
// Uses a short configurable context timeout — no goroutine leak on slow responses.
func (c *SupabaseClient) IsDuplicateBySourceURL(ctx context.Context, sourceURL string) (bool, error) {
	if sourceURL == "" {
		return false, nil
	}

	checkCtx, cancel := context.WithTimeout(ctx, c.options.DuplicateCheckTimeout)
	defer cancel()

	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?source_url=eq.%s&select=id",
		c.url, url.QueryEscape(sourceURL))

	req, err := http.NewRequestWithContext(checkCtx, "GET", reqURL, nil)
	if err != nil {
		return false, err
	}

	req.Header.Set("apikey", c.serviceKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			logger.Warn("Supabase source_url duplicate check timeout, allowing news", "timeout_seconds", c.options.DuplicateCheckTimeout.Seconds())
			return false, nil
		}
		return false, err
	}
	defer resp.Body.Close()

	var existing []struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&existing); err != nil {
		return false, err
	}

	return len(existing) > 0, nil
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

	// Extract numbers/statistics - these are key indicators of same news
	numbers1 := extractNumbers(title1)
	numbers2 := extractNumbers(title2)

	// If both have significant numbers and they match, likely same news
	if len(numbers1) > 0 && len(numbers2) > 0 {
		commonNumbers := 0
		hasLargeNumber := false
		for _, n1 := range numbers1 {
			for _, n2 := range numbers2 {
				if n1 == n2 {
					commonNumbers++
					// Large numbers (4+ digits) are very specific identifiers
					if len(n1) >= 4 {
						hasLargeNumber = true
					}
				}
			}
		}
		// If they share a specific large number (like "55000"), it's almost certainly the same news
		if commonNumbers > 0 {
			wordSimilarity := calculateWordSimilarity(words1, words2)
			// Very large numbers (4+ digits) are strong indicators - lower threshold significantly
			if hasLargeNumber && wordSimilarity >= 0.3 {
				return true
			}
			// Regular numbers (3 digits) still need some word overlap
			if wordSimilarity >= 0.4 {
				return true
			}
		}
	}

	// Check for common key entities (names, places, organizations)
	// Words longer than 5 chars are more likely to be significant
	significantWords1 := filterSignificantWords(words1)
	significantWords2 := filterSignificantWords(words2)

	if len(significantWords1) > 0 && len(significantWords2) > 0 {
		significantSimilarity := calculateWordSimilarity(significantWords1, significantWords2)
		if significantSimilarity >= 0.6 {
			return true
		}
	}

	// Standard word overlap check (70% threshold)
	similarity := calculateWordSimilarity(words1, words2)
	return similarity >= 0.7
}

// filterSignificantWords returns words that are likely meaningful identifiers (proper nouns, key terms)
func filterSignificantWords(words []string) []string {
	var result []string
	for _, w := range words {
		// Words with 6+ characters are more likely to be significant
		if len(w) >= 6 {
			result = append(result, w)
		}
	}
	return result
}

// extractNumbers extracts all significant numeric values from text
func extractNumbers(text string) []string {
	re := regexp.MustCompile(`\d+`)
	matches := re.FindAllString(text, -1)
	// Filter out very small numbers (like "1", "2") that are less meaningful
	var result []string
	for _, m := range matches {
		if len(m) >= 3 { // At least 3 digits (like "100", "55000")
			result = append(result, m)
		}
	}
	return result
}

// calculateWordSimilarity calculates word overlap ratio between two word lists
func calculateWordSimilarity(words1, words2 []string) float64 {
	// Build word set from first title (only words > 2 chars)
	wordSet := make(map[string]bool)
	for _, w := range words1 {
		if len(w) > 2 {
			wordSet[w] = true
		}
	}

	// Count common words
	commonWords := 0
	for _, w := range words2 {
		if len(w) > 2 && wordSet[w] {
			commonWords++
		}
	}

	// Calculate similarity ratio using smaller word count as denominator
	minLen := len(words1)
	if len(words2) < minLen {
		minLen = len(words2)
	}

	if minLen == 0 {
		return 0
	}

	return float64(commonWords) / float64(minLen)
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

// GetCarouselNews retrieves random news for the carousel.
// Supabase REST API does not support ORDER BY RANDOM(), so we fetch a larger
// pool (3× limit, capped at 30) and shuffle it in Go before returning limit items.
func (c *SupabaseClient) GetCarouselNews(limit int) ([]NewsArchive, error) {
	if limit <= 0 {
		limit = 6
	}

	// Fetch a larger pool so the shuffle produces meaningful variety
	poolSize := limit * 3
	if poolSize > 30 {
		poolSize = 30
	}

	pool, err := c.GetActiveNews(poolSize)
	if err != nil {
		return nil, err
	}
	if len(pool) == 0 {
		return pool, nil
	}

	// Fisher-Yates shuffle
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := len(pool) - 1; i > 0; i-- {
		j := r.Intn(i + 1)
		pool[i], pool[j] = pool[j], pool[i]
	}

	if len(pool) > limit {
		pool = pool[:limit]
	}
	return pool, nil
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

// GenerateSlugWithDate creates a URL-friendly slug from title with a date suffix.
// Delegates to the canonical slugify package — single source of truth for all slug logic.
func GenerateSlugWithDate(title string, publishedAt time.Time) string {
	return slugify.SlugWithDate(title, publishedAt)
}
