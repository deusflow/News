package main

import (
	"os"
	"testing"
	"time"
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

