package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

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

// SaveNews saves a news item to Supabase
func (c *SupabaseClient) SaveNews(news NewsArchive) error {
	// Generate slug if not provided
	if news.Slug == "" {
		news.Slug = GenerateSlug(news.Title)
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

	// Create request
	reqURL := fmt.Sprintf("%s/rest/v1/news_archive", c.url)
	req, err := http.NewRequest("POST", reqURL, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", c.serviceKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("Prefer", "return=minimal")

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
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

	return nil
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
func GenerateSlug(title string) string {
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

	// Add timestamp suffix for uniqueness
	timestamp := time.Now().Format("20060102-150405")
	slug = fmt.Sprintf("%s-%s", slug, timestamp)

	return slug
}
