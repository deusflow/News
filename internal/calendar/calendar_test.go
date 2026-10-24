package calendar

import (
	"strings"
	"testing"
	"time"
)

func TestGetTargetMonth(t *testing.T) {
	tests := []struct {
		name         string
		now          time.Time
		wantMonth    time.Month
		wantYear     int
		wantNom      string
		wantGen      string
	}{
		{
			name:      "Before 15th of the month targets current month",
			now:       time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC),
			wantMonth: time.September,
			wantYear:  2026,
			wantNom:   "Вересень",
			wantGen:   "вересня",
		},
		{
			name:      "On or after 15th of the month targets next month",
			now:       time.Date(2026, time.September, 16, 19, 0, 0, 0, time.UTC),
			wantMonth: time.October,
			wantYear:  2026,
			wantNom:   "Жовтень",
			wantGen:   "жовтня",
		},
		{
			name:      "Late December rolls over to January next year",
			now:       time.Date(2026, time.December, 29, 18, 0, 0, 0, time.UTC),
			wantMonth: time.January,
			wantYear:  2027,
			wantNom:   "Січень",
			wantGen:   "січня",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, y, nom, gen := GetTargetMonth(tt.now)
			if m != tt.wantMonth {
				t.Errorf("GetTargetMonth() month = %v, want %v", m, tt.wantMonth)
			}
			if y != tt.wantYear {
				t.Errorf("GetTargetMonth() year = %v, want %v", y, tt.wantYear)
			}
			if nom != tt.wantNom {
				t.Errorf("GetTargetMonth() nom = %v, want %v", nom, tt.wantNom)
			}
			if gen != tt.wantGen {
				t.Errorf("GetTargetMonth() gen = %v, want %v", gen, tt.wantGen)
			}
		})
	}
}

func TestGetTargetMonth_EnvOverrides(t *testing.T) {
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)

	// 1. Force "next" even on 5th of September -> October
	t.Setenv("CALENDAR_TARGET_MONTH", "next")
	m, y, nom, _ := GetTargetMonth(now)
	if m != time.October || y != 2026 || nom != "Жовтень" {
		t.Errorf("CALENDAR_TARGET_MONTH=next failed, got month %v, year %v, nom %s", m, y, nom)
	}

	// 2. Force specific month by number "11" -> November
	t.Setenv("CALENDAR_TARGET_MONTH", "11")
	m, y, nom, _ = GetTargetMonth(now)
	if m != time.November || y != 2026 || nom != "Листопад" {
		t.Errorf("CALENDAR_TARGET_MONTH=11 failed, got month %v, year %v, nom %s", m, y, nom)
	}

	// 3. Force by Ukrainian month name
	t.Setenv("CALENDAR_TARGET_MONTH", "Грудень")
	m, y, nom, _ = GetTargetMonth(now)
	if m != time.December || y != 2026 || nom != "Грудень" {
		t.Errorf("CALENDAR_TARGET_MONTH=Грудень failed, got month %v, year %v, nom %s", m, y, nom)
	}
}

func TestGetDanishStandardEvents_QuarterlyBornepenge(t *testing.T) {
	// January, April, July, October should have Børnepenge
	quarterMonths := []time.Month{time.January, time.April, time.July, time.October}
	for _, m := range quarterMonths {
		events := GetDanishStandardEvents(m, 2026)
		if !strings.Contains(events, "Børnepenge") {
			t.Errorf("Month %v should contain Børnepenge, got:\n%s", m, events)
		}
	}

	// Non-quarter month should not have Børnepenge
	febEvents := GetDanishStandardEvents(time.February, 2026)
	if strings.Contains(febEvents, "Børnepenge") {
		t.Errorf("February should not contain quarterly Børnepenge, got:\n%s", febEvents)
	}
}

func TestGetDanishStandardEvents_TaxMilestones(t *testing.T) {
	marchEvents := GetDanishStandardEvents(time.March, 2026)
	if !strings.Contains(marchEvents, "Årsopgørelse") {
		t.Errorf("March should mention Årsopgørelse, got:\n%s", marchEvents)
	}

	novEvents := GetDanishStandardEvents(time.November, 2026)
	if !strings.Contains(novEvents, "Forskudsopgørelse") {
		t.Errorf("November should mention Forskudsopgørelse, got:\n%s", novEvents)
	}
}

func TestHasTextContent(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"   ", false},
		{"<p><br></p>", false},
		{"&nbsp;&nbsp;&#8203;", false},
		{"<b>Календар</b>", true},
		{"Новини Данії", true},
	}

	for _, tt := range tests {
		if got := hasTextContent(tt.input); got != tt.want {
			t.Errorf("hasTextContent(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
