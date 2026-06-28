package news

import (
	"strings"
	"testing"
)

func TestRemoveTitleFromSummary_UnicodeSafeTrim(t *testing.T) {
	title := "Maersk stopper alle sejladser"
	summary := "Maersk stopper alle sejladser. Ruten lukkes midlertidigt af sikkerhedshensyn."

	got := removeTitleFromSummary(summary, title)
	want := "Ruten lukkes midlertidigt af sikkerhedshensyn."
	if got != want {
		t.Fatalf("unexpected trimmed summary\nwant: %q\n got: %q", want, got)
	}
}

func TestRemoveTitleFromSummary_DoesNotTrimWhenNotPrefix(t *testing.T) {
	title := "IKEA kommer til Randers"
	summary := "Projektet bliver storre end forst antaget."

	got := removeTitleFromSummary(summary, title)
	if got != summary {
		t.Fatalf("expected summary to remain unchanged, got: %q", got)
	}
}

func TestFormatNewsWithImage_IncludesWhyItMatters(t *testing.T) {
	n := News{
		Title:            "Test title",
		TitleUkrainian:   "Тестовий заголовок",
		SummaryDanish:    "Kort dansk tekst.",
		SummaryUkrainian: "Короткий український текст.",
		WhyItMatters:     "Це впливає на умови праці випускників у великих містах.",
		Category:         "society",
	}

	out := FormatNewsWithImage(n)
	if !strings.Contains(out, "Чому це важливо") {
		t.Fatalf("expected why-it-matters block in output, got: %q", out)
	}
}

func TestSanitizeStartFlag(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"🇸🇪 Новина про Данію", "🇩🇰 Новина про Данію"},
		{"🇳🇴 Новина про Норвегію", "🇩🇰 Новина про Норвегію"},
		{"🇩🇰 Новина про Данію", "🇩🇰 Новина про Данію"},
		{"Сьюзан 🇸🇪 Кронборг", "Сьюзан 🇸🇪 Кронборг"}, // inside string shouldn't be affected
	}

	for _, tt := range tests {
		got := sanitizeStartFlag(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeStartFlag(%q) = %q; expected %q", tt.input, got, tt.expected)
		}
	}
}
