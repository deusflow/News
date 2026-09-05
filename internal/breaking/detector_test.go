package breaking

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetector_MeteoAlarm(t *testing.T) {
	// Sample XML with an Orange (Severe) wind warning
	meteoXMLAlert := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:cap="urn:oasis:names:tc:emergency:cap:1.2">
  <title>MeteoAlarm - Alerting Europe for Extreme Weather</title>
  <entry>
    <title>Orange Wind Warning for Denmark</title>
    <link href="https://meteoalarm.org/alert/123"/>
    <summary>Voldsomt vejr med stormstød op til orkanstyrke.</summary>
    <event>Wind</event>
    <severity>Severe</severity>
    <description>Voldsomt blæsevejr rammer den jyske vestkyst og Storebælt.</description>
  </entry>
</feed>`

	// Sample XML with calm weather (no alerts)
	meteoXMLCalm := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:cap="urn:oasis:names:tc:emergency:cap:1.2">
  <title>MeteoAlarm - Alerting Europe for Extreme Weather</title>
</feed>`

	// Sample XML with Yellow (Moderate) alert that should be IGNORED
	meteoXMLYellow := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:cap="urn:oasis:names:tc:emergency:cap:1.2">
  <title>MeteoAlarm - Alerting Europe for Extreme Weather</title>
  <entry>
    <title>Yellow Rain Warning for Denmark</title>
    <link href="https://meteoalarm.org/alert/456"/>
    <summary>Lidt kraftig regn.</summary>
    <event>Rain</event>
    <severity>Moderate</severity>
    <description>Almindelig dansk regnbyge.</description>
  </entry>
</feed>`

	t.Run("Calm weather returns no alert", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(meteoXMLCalm))
		}))
		defer ts.Close()

		d := NewEmergencyDetector(ts.Client())
		d.meteoURL = ts.URL

		alert, err := d.checkMeteoAlarm(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if alert != nil {
			t.Errorf("expected nil alert for calm weather, got %+v", alert)
		}
	})

	t.Run("Yellow Moderate alert is ignored", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(meteoXMLYellow))
		}))
		defer ts.Close()

		d := NewEmergencyDetector(ts.Client())
		d.meteoURL = ts.URL

		alert, err := d.checkMeteoAlarm(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if alert != nil {
			t.Errorf("expected yellow alert to be ignored, got %+v", alert)
		}
	})

	t.Run("Orange Severe alert triggers EmergencyAlert", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(meteoXMLAlert))
		}))
		defer ts.Close()

		d := NewEmergencyDetector(ts.Client())
		d.meteoURL = ts.URL

		alert, err := d.checkMeteoAlarm(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if alert == nil {
			t.Fatalf("expected alert for Orange warning, got nil")
		}
		if alert.Category != "weather" {
			t.Errorf("expected category weather, got %s", alert.Category)
		}
		if alert.Severity != "severe" {
			t.Errorf("expected severity severe, got %s", alert.Severity)
		}
		if !strings.Contains(alert.Title, "Orange Wind Warning") {
			t.Errorf("unexpected title: %s", alert.Title)
		}
	})
}

func TestDetector_EmergencyKeywords(t *testing.T) {
	tests := []struct {
		title       string
		shouldMatch bool
	}{
		{"Storebæltsbroen lukket for al biltrafik på grund af blæst", true},
		{"Øresundsbroen lukket midlertidigt", true},
		{"Togtrafikken indstillet mellem Odense og Fredericia", true},
		{"Beredskabsmeddelelse fra politiet: Bliv indendørs", true},
		{"Giftig røg efter brand i industriområde: Luk døre og vinduer", true},
		{"DMI varsler stormflod langs kysterne", true},
		{"Beboere evakueres efter gasudslip", true},
		{"Regeringen fremlægger ny finanslov for 2027", false},
		{"Fodboldlandsholdet vinder venskabskamp", false},
		{"DSB indsætter nye tog på Sjælland", false},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			lower := strings.ToLower(tt.title)
			matched := false
			for _, kw := range emergencyKeywords {
				if strings.Contains(lower, kw) {
					matched = true
					break
				}
			}
			if matched != tt.shouldMatch {
				t.Errorf("title %q match = %v, expected %v", tt.title, matched, tt.shouldMatch)
			}
		})
	}
}

func TestBuildEmergencyFallbackNews(t *testing.T) {
	n := buildEmergencyFallbackNews(
		"Storebæltsbroen lukket for al trafik",
		"https://vejdirektoratet.dk/123",
		"Vejdirektoratet",
		"Høj vindhastighed tvinger myndighederne til at lukke forbindelsen.",
	)

	if n.Category != "emergency" {
		t.Errorf("expected category emergency, got %s", n.Category)
	}
	if n.Mood != "urgent" {
		t.Errorf("expected mood urgent, got %s", n.Mood)
	}
	if !strings.HasPrefix(n.TitleUkrainian, "🚨 ЕКСТРЕНЕ ПОВІДОМЛЕННЯ:") {
		t.Errorf("unexpected TitleUkrainian: %s", n.TitleUkrainian)
	}
	if n.AudienceScore != 12 {
		t.Errorf("expected audience score 12, got %d", n.AudienceScore)
	}
}
