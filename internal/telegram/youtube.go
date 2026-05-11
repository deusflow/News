package telegram

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kkdai/youtube/v2"
)

// 1a. URL type helpers

func IsYouTubeURL(url string) bool {
	return strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
}

func IsDRDirectVideo(url string) bool {
	return (strings.Contains(url, "dr.dk") || strings.Contains(url, "dr.tv")) &&
		strings.HasSuffix(strings.ToLower(url), ".mp4")
}

func IsDRPageURL(url string) bool {
	return (strings.Contains(url, "dr.dk") || strings.Contains(url, "dr.tv")) &&
		!strings.HasSuffix(strings.ToLower(url), ".mp4")
}

// 1b. Duration detection

// GetVideoDurationSeconds returns duration in seconds.
// Returns 0, error if duration cannot be determined.
// Error = caller must skip video and use photo fallback.
func GetVideoDurationSeconds(videoURL string) (int, error) {
	if IsYouTubeURL(videoURL) {
		client := youtube.Client{
			HTTPClient: &http.Client{Timeout: 15 * time.Second},
		}

		video, err := client.GetVideo(videoURL)
		if err != nil {
			return 0, err
		}
		if video.Duration == 0 {
			return 0, fmt.Errorf("duration unknown (0s)")
		}
		return int(video.Duration.Seconds()), nil
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

// 1c. YouTube stream for Level 1 upload

// GetYouTubeStream returns an io.ReadCloser and content size in bytes.
// Uses AndroidClient for better datacenter IP bypass.
// Returns nil, 0, error if blocked or no suitable format found.
// Quality preference: 720p → 480p → 360p
// HARD SIZE LIMIT: 50 * 1024 * 1024 bytes
// Format filter: MimeType must contain "video/mp4"
// Format must have BOTH video and audio (check AudioChannels > 0 or AudioQuality != "")
func GetYouTubeStream(videoURL string) (io.ReadCloser, int64, error) {
	const maxVideoBytes = 50 * 1024 * 1024

	client := youtube.Client{
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}

	video, err := client.GetVideo(videoURL)
	if err != nil {
		return nil, 0, err
	}

	var bestFormat *youtube.Format

	// Look for mp4 formats with audio, sorted by resolution preference
	// For simplicity, find the best format that has video and audio and is <= maxVideoBytes
	formats := video.Formats.WithAudioChannels()

	// Sort formats to prefer 720p > 480p > 360p
	var validFormats []youtube.Format
	for _, f := range formats {
		if !strings.Contains(f.MimeType, "video/mp4") {
			continue
		}
		if f.ContentLength > maxVideoBytes {
			continue
		}
		// If ContentLength is unknown in format, skip to avoid 50MB telegram fail
		if f.ContentLength == 0 {
			continue
		}
		// We want both video and audio. WithAudioChannels() already filters for audio.
		// We should ensure it has a video track (e.g., QualityLabel is set).
		if f.QualityLabel == "" {
			continue
		}
		validFormats = append(validFormats, f)
	}

	if len(validFormats) == 0 {
		return nil, 0, fmt.Errorf("no suitable mp4 format found under 50MB")
	}

	// Try to find preferred resolutions
	preferredResolutions := []string{"720p", "480p", "360p"}
	for _, res := range preferredResolutions {
		for _, f := range validFormats {
			if strings.HasPrefix(f.QualityLabel, res) {
				bestFormat = &f
				break
			}
		}
		if bestFormat != nil {
			break
		}
	}

	// If preferred not found, use the first valid one
	if bestFormat == nil {
		bestFormat = &validFormats[0]
	}

	stream, size, err := client.GetStream(video, bestFormat)
	if err != nil {
		return nil, 0, err
	}
	return stream, size, nil
}
