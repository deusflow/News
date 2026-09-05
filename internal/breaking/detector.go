package breaking

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/deusflow/News/internal/logger"
	"github.com/mmcdole/gofeed"
)

const defaultMeteoAlarmURL = "https://feeds.meteoalarm.org/feeds/meteoalarm-legacy-atom-denmark"

var defaultEmergencyNewsFeeds = []string{
	"https://www.dr.dk/nyheder/service/feeds/allenyheder",
	"https://www.berlingske.dk/next-api/feeds/alle",
	"https://www.bt.dk/next-api/feeds/kategori/nyheder",
}

// Critical emergency keywords in Danish headlines.
// Only high-confidence phrases that warrant interrupting channel flow.
var emergencyKeywords = []string{
	"storebæltsbroen lukket",
	"storebæltsbroen er lukket",
	"øresundsbroen lukket",
	"øresundsbroen er lukket",
	"togtrafikken indstillet",
	"tog holder stille i hele",
	"beredskabsmeddelelse",
	"giftig røg",
	"luk døre og vinduer",
	"varsler stormflod",
	"farligt vejr",
	"evakuering",
	"evakueres",
}

// EmergencyAlert contains metadata for an acute emergency event in Denmark.
type EmergencyAlert struct {
	Title       string
	URL         string
	Description string
	Severity    string // "severe", "extreme"
	Source      string // "DMI / MeteoAlarm", "DR Nyheder", etc.
	Category    string // "weather", "transport", "safety"
}

// EmergencyDetector monitors official Danish emergency channels and news feeds.
type EmergencyDetector struct {
	httpClient *http.Client
	meteoURL   string
	newsFeeds  []string
	feedParser *gofeed.Parser
}

// NewEmergencyDetector creates a detector with sensible defaults.
func NewEmergencyDetector(httpClient *http.Client) *EmergencyDetector {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &EmergencyDetector{
		httpClient: httpClient,
		meteoURL:   defaultMeteoAlarmURL,
		newsFeeds:  defaultEmergencyNewsFeeds,
		feedParser: gofeed.NewParser(),
	}
}

// Atom XML structures for MeteoAlarm CAP feed.
type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title       string    `xml:"title"`
	Updated     time.Time `xml:"updated"`
	Link        atomLink  `xml:"link"`
	Summary     string    `xml:"summary"`
	Event       string    `xml:"event"`
	Severity    string    `xml:"severity"`
	Certainty   string    `xml:"certainty"`
	Urgency     string    `xml:"urgency"`
	Description string    `xml:"description"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
}

// ScanForEmergencies checks both DMI MeteoAlarm for severe weather alerts
// and national news feeds for acute infrastructure / safety emergencies.
func (d *EmergencyDetector) ScanForEmergencies(ctx context.Context) (*EmergencyAlert, error) {
	// 1. Check DMI MeteoAlarm for Orange/Red (Severe/Extreme) warnings
	if alert, err := d.checkMeteoAlarm(ctx); err == nil && alert != nil {
		return alert, nil
	} else if err != nil {
		logger.Warn("Failed to check MeteoAlarm feed", "error", err)
	}

	// 2. Check national news feeds for critical breaking events
	if alert, err := d.checkNewsFeeds(ctx); err == nil && alert != nil {
		return alert, nil
	} else if err != nil {
		logger.Warn("Failed to check emergency news feeds", "error", err)
	}

	return nil, nil
}

// checkMeteoAlarm checks the official European CAP weather alert feed for Denmark.
func (d *EmergencyDetector) checkMeteoAlarm(ctx context.Context) (*EmergencyAlert, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.meteoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "DanishNewsBot/1.0 (+https://github.com/deusflow/News)")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meteoalarm request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("meteoalarm returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read meteoalarm body: %w", err)
	}

	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("failed to unmarshal meteoalarm XML: %w", err)
	}

	for _, entry := range feed.Entries {
		sev := strings.ToLower(strings.TrimSpace(entry.Severity))
		// We only trigger on Severe (Orange) or Extreme (Red) warnings.
		// Yellow / Moderate or Minor warnings are common routine weather and ignored.
		if sev == "severe" || sev == "extreme" || strings.Contains(strings.ToLower(entry.Title), "orange") || strings.Contains(strings.ToLower(entry.Title), "red") {
			url := entry.Link.Href
			if url == "" {
				url = "https://www.dmi.dk/varsler/"
			}
			desc := strings.TrimSpace(entry.Description)
			if desc == "" {
				desc = strings.TrimSpace(entry.Summary)
			}

			return &EmergencyAlert{
				Title:       entry.Title,
				URL:         url,
				Description: desc,
				Severity:    sev,
				Source:      "DMI / MeteoAlarm",
				Category:    "weather",
			}, nil
		}
	}

	return nil, nil
}

// checkNewsFeeds scans recent headlines from major Danish media for emergency phrases.
func (d *EmergencyDetector) checkNewsFeeds(ctx context.Context) (*EmergencyAlert, error) {
	oneHourAgo := time.Now().Add(-60 * time.Minute)

	for _, feedURL := range d.newsFeeds {
		feed, err := d.feedParser.ParseURLWithContext(feedURL, ctx)
		if err != nil {
			logger.Warn("Failed to parse emergency feed", "url", feedURL, "error", err)
			continue
		}

		for _, item := range feed.Items {
			// Check publication time — only fresh news within the last 60 mins
			if item.PublishedParsed != nil && item.PublishedParsed.Before(oneHourAgo) {
				continue
			}

			lowerTitle := strings.ToLower(item.Title)
			for _, kw := range emergencyKeywords {
				if strings.Contains(lowerTitle, kw) {
					category := "safety"
					if strings.Contains(kw, "broen") || strings.Contains(kw, "tog") {
						category = "transport"
					} else if strings.Contains(kw, "vejr") || strings.Contains(kw, "storm") {
						category = "weather"
					}

					sourceName := feed.Title
					if sourceName == "" {
						sourceName = "Danish News"
					}

					return &EmergencyAlert{
						Title:       item.Title,
						URL:         item.Link,
						Description: item.Description,
						Severity:    "severe",
						Source:      sourceName,
						Category:    category,
					}, nil
				}
			}
		}
	}

	return nil, nil
}
