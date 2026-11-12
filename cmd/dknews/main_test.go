package main

import (
	"os"
	"testing"
	"time"

	"github.com/deusflow/News/internal/app"
)

func TestIsTargetCopenhagenHour_NonScheduledRunsAlwaysAllowed(t *testing.T) {
	_ = os.Unsetenv("GITHUB_EVENT_NAME")
	_ = os.Unsetenv("CHECK_TIMEZONE_HOURS")

	if !isTargetCopenhagenHour("single") {
		t.Fatalf("expected manual/non-scheduled runs to always pass the timezone gate")
	}
	if !isTargetCopenhagenHour("digest") {
		t.Fatalf("expected manual/non-scheduled digest runs to always pass the timezone gate")
	}
	if !isTargetCopenhagenHour("breaking") {
		t.Fatalf("expected breaking mode runs to always pass the timezone gate")
	}

	// Delayed GitHub Actions schedule runs (where GITHUB_EVENT_NAME=schedule) must ALSO pass
	// when CHECK_TIMEZONE_HOURS is not set, because GitHub Actions cron often delays runs by 1-3 hours.
	_ = os.Setenv("GITHUB_EVENT_NAME", "schedule")
	defer os.Unsetenv("GITHUB_EVENT_NAME")
	if !isTargetCopenhagenHour("single") {
		t.Fatalf("expected delayed GitHub Actions schedule run to pass when CHECK_TIMEZONE_HOURS is unset")
	}
}

func TestIsTargetCopenhagenHour_ScheduledGating(t *testing.T) {
	_ = os.Setenv("CHECK_TIMEZONE_HOURS", "true")
	defer os.Unsetenv("CHECK_TIMEZONE_HOURS")

	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		t.Fatalf("failed to load Europe/Copenhagen location: %v", err)
	}
	nowDK := time.Now().In(loc)
	currentHour := nowDK.Hour()

	isTargetNewsHour := false
	for _, h := range []int{8, 10, 13, 17, 21} {
		if currentHour == h {
			isTargetNewsHour = true
			break
		}
	}

	result := isTargetCopenhagenHour("single")
	if result != isTargetNewsHour {
		t.Fatalf("expected isTargetCopenhagenHour to return %v for current Copenhagen hour %d, got %v", isTargetNewsHour, currentHour, result)
	}

	isDigestHour := (currentHour == 20)
	digestResult := isTargetCopenhagenHour("digest")
	if digestResult != isDigestHour {
		t.Fatalf("expected isTargetCopenhagenHour(digest) to return %v for hour %d, got %v", isDigestHour, currentHour, digestResult)
	}
}

func TestIsCopenhagenQuietHours_Main(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		t.Skip("Europe/Copenhagen timezone not available")
	}

	t.Setenv("ALLOW_NIGHT_PUBLISH", "")

	tNight := time.Date(2026, 11, 12, 23, 15, 0, 0, loc)
	if !app.IsCopenhagenQuietHours(tNight) {
		t.Errorf("expected 23:15 to be quiet hours")
	}

	tLate := time.Date(2026, 11, 12, 1, 49, 0, 0, loc)
	if !app.IsCopenhagenQuietHours(tLate) {
		t.Errorf("expected 01:49 to be quiet hours")
	}

	tDay := time.Date(2026, 11, 12, 10, 0, 0, 0, loc)
	if app.IsCopenhagenQuietHours(tDay) {
		t.Errorf("expected 10:00 to NOT be quiet hours")
	}

	t.Setenv("ALLOW_NIGHT_PUBLISH", "true")
	if app.IsCopenhagenQuietHours(tLate) {
		t.Errorf("expected ALLOW_NIGHT_PUBLISH=true to bypass quiet hours")
	}
}

