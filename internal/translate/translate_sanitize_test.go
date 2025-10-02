package translate

import "testing"

func TestSanitizeAIText_RemovesInlineParenthesizedDisclaimer(t *testing.T) {
	in := "🇺🇦 Зовнішньоміністерство надісила заклик до данців у Марокко\n(Note: This translation is a machine translation and may contain errors. Always double-check with a reliable source for accurate translations.) В Марракеші та інших містах Марокко продовжуються демонстрації."
	out := SanitizeAIText(in)
	if out == "" {
		t.Fatalf("got empty output")
	}
	if contains := containsCaseInsensitive(out, "note:"); contains {
		t.Errorf("output still contains 'Note:' disclaimer: %q", out)
	}
	if !containsCaseInsensitive(out, "В Марракеші") {
		t.Errorf("expected Ukrainian content preserved after disclaimer removal, got: %q", out)
	}
}

func TestSanitizeAIText_RemovesFullLineNote(t *testing.T) {
	in := "Note: This translation is a machine translation and may contain errors.\nВ Марракеші та інших містах Марокко продовжуються демонстрації."
	out := SanitizeAIText(in)
	if contains := containsCaseInsensitive(out, "note:"); contains {
		t.Errorf("disclaimer line was not removed: %q", out)
	}
	if !containsCaseInsensitive(out, "Марокко") {
		t.Errorf("expected content line to remain: %q", out)
	}
}

func TestSanitizeAIText_RemovesBracketedDisclaimer(t *testing.T) {
	in := "[Note: Machine translation] Це тестовий рядок."
	out := SanitizeAIText(in)
	if contains := containsCaseInsensitive(out, "note"); contains {
		t.Errorf("bracketed disclaimer was not removed: %q", out)
	}
	if wantSub := "Це тестовий рядок"; !containsCaseInsensitive(out, wantSub) {
		t.Errorf("expected text preserved, want substring %q in %q", wantSub, out)
	}
}

func containsCaseInsensitive(s, sub string) bool {
	S := []rune(s)
	Sub := []rune(sub)
	// simple case-insensitive search; for ASCII keywords only
	return containsFold(string(S), string(Sub))
}

func containsFold(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(substr) > 0 && (indexFold(s, substr) >= 0)))
}

func indexFold(s, substr string) int {
	n := len(s)
	m := len(substr)
	for i := 0; i+m <= n; i++ {
		if equalFoldASCII(s[i:i+m], substr) {
			return i
		}
	}
	return -1
}

func equalFoldASCII(s, t string) bool {
	if len(s) != len(t) {
		return false
	}
	for i := 0; i < len(s); i++ {
		a, b := s[i], t[i]
		if 'A' <= a && a <= 'Z' {
			a += 'a' - 'A'
		}
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}
