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
		got := sanitizeAIField(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeAIField(%q) = %q; expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestTruncateCaptionHTML_ClosesTagsAndRespectsMaxRunes(t *testing.T) {
	input := "<b>Заголовок новини</b>\n\nПерше речення досить довге. <i>Друге речення містить важливі деталі, які ми не хочемо різати посередині слова.</i> Тretie речення вже зайве."
	truncated := TruncateCaptionHTML(input, 60)

	if len([]rune(truncated)) > 60 {
		t.Fatalf("expected truncated string length <= 60 runes, got %d runes: %q", len([]rune(truncated)), truncated)
	}

	// Check that tags are closed
	if strings.Count(truncated, "<b>") != strings.Count(truncated, "</b>") {
		t.Errorf("unbalanced <b> tags: %q", truncated)
	}
	if strings.Count(truncated, "<i>") != strings.Count(truncated, "</i>") {
		t.Errorf("unbalanced <i> tags: %q", truncated)
	}
}

func TestFormatCaptionForPhoto_Enforces1024Limit(t *testing.T) {
	longText := strings.Repeat("Це дуже довгий текст речення, який описує масштабну подію у Данії. ", 30)
	n := News{
		Title:            "Масштабна реформа",
		TitleDanish:      "Omfattende reform i Danmark",
		TitleUkrainian:   "Масштабна реформа в Данії",
		SummaryDanish:    longText,
		SummaryUkrainian: longText,
		WhyItMatters:     "Це суттєво змінить податкову систему та правила ринку праці для всіх жителів.",
		FunFact:          "Данія є однією з країн із найвищим рівнем цифровізації держпослуг.",
		Category:         "society",
		Tags:             []string{"реформа", "данія", "податки"},
	}

	caption := FormatCaptionForPhoto(n, 1024)
	if len([]rune(caption)) > 1024 {
		t.Fatalf("caption exceeds 1024 runes: %d", len([]rune(caption)))
	}
	if caption == "" {
		t.Fatalf("expected non-empty caption fallback from TruncateCaptionHTML")
	}
}

