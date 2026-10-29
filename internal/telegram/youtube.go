package telegram

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 1a. URL type helpers

func IsYouTubeURL(url string) bool {
	return strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
}

func IsDRDirectVideo(url string) bool {
	lower := strings.ToLower(strings.Split(url, "?")[0])
	return (strings.Contains(lower, "dr.dk") || strings.Contains(lower, "dr.tv") || strings.Contains(lower, "dr-massive")) &&
		strings.HasSuffix(lower, ".mp4")
}

func IsDRPageURL(url string) bool {
	return (strings.Contains(url, "dr.dk") || strings.Contains(url, "dr.tv")) &&
		!IsDRDirectVideo(url)
}

// ExtractDRDirectVideoURL tries to extract a direct .mp4 contentUrl from a DR.dk page's JSON-LD.
func ExtractDRDirectVideoURL(pageURL string) string {
	if IsDRDirectVideo(pageURL) {
		return pageURL
	}
	if !IsDRPageURL(pageURL) {
		return ""
	}
	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	html := string(body)
	contentURLRegex := regexp.MustCompile(`"contentUrl"\s*:\s*"(https?://[^"]+\.mp4[^"]*)"`)
	if m := contentURLRegex.FindStringSubmatch(html); len(m) > 1 {
		return m[1]
	}
	return ""
}

// 1b. Duration detection

// GetVideoDurationSeconds returns duration in seconds.
// Returns 0, error if duration cannot be determined.
// Error = caller must skip video and use photo fallback.
func GetVideoDurationSeconds(videoURL string) (int, error) {
	if IsYouTubeURL(videoURL) {
		return 0, fmt.Errorf("youtube video streaming not supported, using photo fallback")
	}

	// Direct DR.dk .mp4 video clips do not have HTML JSON-LD headers; allow native upload
	// (caller still verifies byte size via GetRemoteContentLength <= maxTelegramURLVideoBytes).
	if IsDRDirectVideo(videoURL) {
		return 60, nil
	}

	if IsDRPageURL(videoURL) {
		req, err := http.NewRequest("GET", videoURL, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, err
		}

		html := string(body)

		// Find <script type="application/ld+json">
		ldJsonRegex := regexp.MustCompile(`(?i)<script\s+type=["']application/ld\+json["'][^>]*>(.*?)</script>`)
		matches := ldJsonRegex.FindAllStringSubmatch(html, -1)

		for _, match := range matches {
			if len(match) > 1 {
				// quick search for "duration"
				durationRegex := regexp.MustCompile(`"duration"\s*:\s*"([^"]+)"`)
				durMatch := durationRegex.FindStringSubmatch(match[1])
				if len(durMatch) > 1 {
					return parseISO8601Duration(durMatch[1])
				}
			}
		}
		return 0, fmt.Errorf("duration unknown (no JSON-LD duration found)")
	}

	// DR direct mp4 or other, duration cannot be determined simply without downloading
	return 0, fmt.Errorf("duration unknown from direct URL")
}

// parseISO8601Duration converts "PT2M30S" → seconds
// Handles PTxH, PTxM, PTxS and combinations
func parseISO8601Duration(s string) (int, error) {
	if !strings.HasPrefix(s, "PT") {
		return 0, fmt.Errorf("invalid ISO 8601 duration format: %s", s)
	}

	s = strings.TrimPrefix(s, "PT")

	seconds := 0

	// Parse Hours
	if idx := strings.Index(s, "H"); idx != -1 {
		if val, err := strconv.Atoi(s[:idx]); err == nil {
			seconds += val * 3600
		}
		s = s[idx+1:]
	}

	// Parse Minutes
	if idx := strings.Index(s, "M"); idx != -1 {
		if val, err := strconv.Atoi(s[:idx]); err == nil {
			seconds += val * 60
		}
		s = s[idx+1:]
	}

	// Parse Seconds
	if idx := strings.Index(s, "S"); idx != -1 {
		if val, err := strconv.Atoi(s[:idx]); err == nil {
			seconds += val
		}
	}

	return seconds, nil
}
