package scraper

import (
	"testing"
	"time"
)

func TestIsRecentNyidanmarkURL(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 15, 0, 0, danishLocation())

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "current month",
			url:  "https://www.nyidanmark.dk/da/Nyheder/2026/11/Ny-lov-om-ukrainere",
			want: true,
		},
		{
			name: "previous month",
			url:  "https://www.nyidanmark.dk/da/Nyheder/2026/10/Regler-aendres",
			want: true,
		},
		{
			name: "two months ago",
			url:  "https://www.nyidanmark.dk/da/Nyheder/2026/09/Gammel-nyhed",
			want: false,
		},
		{
			name: "previous year",
			url:  "https://www.nyidanmark.dk/da/Nyheder/2025/12/Meget-gammel",
			want: false,
		},
		{
			name: "url without year-month pattern",
			url:  "https://www.nyidanmark.dk/da/Nyheder/Vigtig-info",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRecentNyidanmarkURL(tt.url, now); got != tt.want {
				t.Errorf("isRecentNyidanmarkURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestParseNyidanmarkDate(t *testing.T) {
	loc := danishLocation()
	morningRun := time.Date(2026, 11, 14, 8, 15, 0, 0, loc)

	t.Run("today keeps current timestamp", func(t *testing.T) {
		got, err := parseNyidanmarkDate("14-11-2026", morningRun)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(morningRun) {
			t.Errorf("got %v, want %v", got, morningRun)
		}
	})

	t.Run("yesterday uses noon so morning run stays under 24h MaxAge", func(t *testing.T) {
		got, err := parseNyidanmarkDate("13-11-2026", morningRun)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		age := morningRun.Sub(got)
		if age >= 24*time.Hour {
			t.Errorf("yesterday article age = %v, expected < 24h", age)
		}
		if age != 20*time.Hour+15*time.Minute {
			t.Errorf("yesterday article age = %v, want 20h15m", age)
		}
	})

	t.Run("two days ago exceeds 24h MaxAge", func(t *testing.T) {
		got, err := parseNyidanmarkDate("12-11-2026", morningRun)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		age := morningRun.Sub(got)
		if age <= 24*time.Hour {
			t.Errorf("two-day-old article age = %v, expected > 24h", age)
		}
	})

	t.Run("handles updated suffix without parse error", func(t *testing.T) {
		got, err := parseNyidanmarkDate("09-10-2026 - Senest opdateret: 28-10-2026", morningRun)
		if err != nil {
			t.Fatalf("unexpected error with Senest opdateret suffix: %v", err)
		}
		if got.Year() != 2026 || got.Month() != time.October || got.Day() != 9 {
			t.Errorf("got %v, want 2026-10-09", got)
		}
	})
}

