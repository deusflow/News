package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
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

// GetActiveNews retrieves non-archived news from the last 10 days
func (c *SupabaseClient) GetActiveNews(ctx context.Context, limit int) ([]NewsArchive, error) {
	tenDaysAgo := time.Now().AddDate(0, 0, -10).Format(time.RFC3339)

	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?is_archived=eq.false&published_at=gte.%s&order=published_at.desc&limit=%d",
		c.url, tenDaysAgo, limit)

	return c.fetchNews(ctx, reqURL)
}

// GetNewsByDate retrieves news for a specific date
func (c *SupabaseClient) GetNewsByDate(ctx context.Context, date time.Time) ([]NewsArchive, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?published_at=gte.%s&published_at=lt.%s&order=published_at.desc",
		c.url, startOfDay.Format(time.RFC3339), endOfDay.Format(time.RFC3339))

	return c.fetchNews(ctx, reqURL)
}

// GetArchivedNews retrieves archived news (older than 10 days)
func (c *SupabaseClient) GetArchivedNews(ctx context.Context, limit, offset int) ([]NewsArchive, error) {
	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?is_archived=eq.true&order=published_at.desc&limit=%d&offset=%d",
		c.url, limit, offset)

	return c.fetchNews(ctx, reqURL)
}

// GetCarouselNews retrieves random news for the carousel.
// Supabase REST API does not support ORDER BY RANDOM(), so we fetch a larger
// pool (3× limit, capped at 30) and shuffle it in Go before returning limit items.
func (c *SupabaseClient) GetCarouselNews(ctx context.Context, limit int) ([]NewsArchive, error) {
	if limit <= 0 {
		limit = 6
	}

	// Fetch a larger pool so the shuffle produces meaningful variety
	poolSize := limit * 3
	if poolSize > 30 {
		poolSize = 30
	}

	pool, err := c.GetActiveNews(ctx, poolSize)
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
func (c *SupabaseClient) GetTrendingNews(ctx context.Context, limit int) ([]NewsArchive, error) {
	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?is_archived=eq.false&order=published_at.desc&limit=%d",
		c.url, limit)

	return c.fetchNews(ctx, reqURL)
}

// ArchiveOldNews marks news older than 10 days as archived
func (c *SupabaseClient) ArchiveOldNews(ctx context.Context) error {
	tenDaysAgo := time.Now().AddDate(0, 0, -10).Format(time.RFC3339)

	reqURL := fmt.Sprintf("%s/rest/v1/news_archive?is_archived=eq.false&published_at=lt.%s",
		c.url, tenDaysAgo)

	body := []byte(`{"is_archived": true}`)

	req, err := http.NewRequestWithContext(ctx, "PATCH", reqURL, bytes.NewBuffer(body))
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
func (c *SupabaseClient) fetchNews(ctx context.Context, reqURL string) ([]NewsArchive, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
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
