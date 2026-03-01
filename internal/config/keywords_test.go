package config

import (
	"os"
	"testing"
)

// writeYAML writes a keywords YAML file to a temp path and returns the path.
func writeYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "keywords_*.yaml")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = f.Close()
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	return f.Name()
}

// loadKW is a test helper that loads a KeywordsConfig from an inline YAML string.
func loadKW(t *testing.T, yaml string) *KeywordsConfig {
	t.Helper()
	kc, err := LoadKeywords(writeYAML(t, yaml))
	if err != nil {
		t.Fatalf("LoadKeywords: %v", err)
	}
	return kc
}

// ── BUG-1: word-boundary matching for short keywords ─────────────────────────

func TestCalculateScore_ShortKeyword_NoFalsePositive(t *testing.T) {
	kc := loadKW(t, `keywords:
  - word: "ai"
    category: "tech"
    weight: 15
`)
	// "ai" must NOT match inside "socialordfører" (substring false positive)
	score, cat := kc.CalculateScore("Socialordfører: Ingen overraskelse i misbrug")
	if score != 0 {
		t.Errorf("false positive inside danish word: score=%d cat=%q (expected score=0)", score, cat)
	}

	// Must NOT match inside "said"
	score2, _ := kc.CalculateScore("Trump said he would impose tariffs on Denmark")
	if score2 != 0 {
		t.Errorf("false positive in 'said': score=%d (expected 0)", score2)
	}

	// Must NOT match inside "Thailand"
	score3, _ := kc.CalculateScore("Dansk turist dræbt i Thailand")
	if score3 != 0 {
		t.Errorf("false positive in 'Thailand': score=%d (expected 0)", score3)
	}
}

func TestCalculateScore_ShortKeyword_MatchesWhenStandalone(t *testing.T) {
	kc := loadKW(t, `keywords:
  - word: "ai"
    category: "tech"
    weight: 15
`)
	cases := []struct {
		text string
		desc string
	}{
		{"AI er fremtiden for Danmark", "uppercase standalone"},
		{"brug af ai i sundhedssektoren", "lowercase between spaces"},
		{"fremtidens ai.", "ai before period"},
		{"Dansk startup bruger AI til at analysere data", "real article title"},
	}
	for _, tc := range cases {
		score, cat := kc.CalculateScore(tc.text)
		if score != 15 || cat != "tech" {
			t.Errorf("[%s] text=%q: expected score=15 cat=tech, got score=%d cat=%q",
				tc.desc, tc.text, score, cat)
		}
	}
}

func TestCalculateScore_ShortKeyword_Su_NoFalsePositive(t *testing.T) {
	kc := loadKW(t, `keywords:
  - word: "su"
    category: "money"
    weight: 8
`)
	// Must NOT fire inside "suppe", "resultat", "museum"
	falsePositives := []string{
		"resultat af valget i Danmark",
		"Nationalmuseum åbner udstilling",
	}
	for _, text := range falsePositives {
		score, _ := kc.CalculateScore(text)
		if score != 0 {
			t.Errorf("false positive for %q: score=%d (expected 0)", text, score)
		}
	}

	// "su" as standalone abbreviation must match
	score, cat := kc.CalculateScore("SU er statens uddannelsesstøtte til studerende")
	if score != 8 || cat != "money" {
		t.Errorf("expected su match: score=8 cat=money, got score=%d cat=%q", score, cat)
	}
}

func TestCalculateScore_ShortKeyword_Sl1(t *testing.T) {
	kc := loadKW(t, `keywords:
  - word: "sl1"
    category: "visas"
    weight: 30
`)
	score, cat := kc.CalculateScore("ukrainere med SL1 status kan søge forlængelse")
	if score != 30 || cat != "visas" {
		t.Errorf("expected sl1 match: score=30 cat=visas, got score=%d cat=%q", score, cat)
	}

	// Must NOT fire inside a word like "ssl12"
	score2, _ := kc.CalculateScore("ssl12 protokol bruges i netværk")
	if score2 != 0 {
		t.Errorf("false positive for ssl12: score=%d (expected 0)", score2)
	}
}

// ── FIX-2: category selected by SUM of weights, not single keyword max ───────

func TestCalculateScore_CategoryBySumNotMax(t *testing.T) {
	// 3 "local" keywords × weight 10 = 30 total
	// 1 "visas" keyword  × weight 25 = 25 total
	// Old code: topCategory = "visas"  (max single keyword weight = 25)
	// New code: topCategory = "local"  (max category sum = 30)
	kc := loadKW(t, `keywords:
  - word: "viborg"
    category: "local"
    weight: 10
  - word: "midtjylland"
    category: "local"
    weight: 10
  - word: "silkeborg"
    category: "local"
    weight: 10
  - word: "opholdstilladelse"
    category: "visas"
    weight: 25
`)
	text := "Viborg og Silkeborg i Midtjylland diskuterer opholdstilladelse for flygtninge"
	score, cat := kc.CalculateScore(text)

	if score != 55 { // 10+10+10+25
		t.Errorf("expected total score=55, got %d", score)
	}
	if cat != "local" {
		t.Errorf("expected category=local (sum 30 > visas sum 25), got %q", cat)
	}
}

func TestCalculateScore_SpamCategoryNeverReturnedAsTopCategory(t *testing.T) {
	// "spam" keywords reduce score but must never be the returned category
	kc := loadKW(t, `keywords:
  - word: "viborg"
    category: "local"
    weight: 10
  - word: "fodbold"
    category: "spam"
    weight: -20
`)
	score, cat := kc.CalculateScore("Viborg FF spiller fodbold i superligaen")
	if score != -10 { // 10 - 20
		t.Errorf("expected score=-10, got %d", score)
	}
	if cat == "spam" {
		t.Errorf("topCategory must never be 'spam', got %q", cat)
	}
}

func TestCalculateScore_NoMatch_ReturnsZeroEmptyCategory(t *testing.T) {
	kc := loadKW(t, `keywords:
  - word: "ukrainer"
    category: "visas"
    weight: 15
`)
	score, cat := kc.CalculateScore("FC Midtjylland vinder bundkampen")
	if score != 0 || cat != "" {
		t.Errorf("expected score=0 cat='', got score=%d cat=%q", score, cat)
	}
}

func TestCalculateScore_LongKeywordUsesContains(t *testing.T) {
	// Long keywords (>3 runes) still work via strings.Contains
	kc := loadKW(t, `keywords:
  - word: "opholdstilladelse"
    category: "visas"
    weight: 20
`)
	score, cat := kc.CalculateScore("ny opholdstilladelse for ukrainere i Danmark")
	if score != 20 || cat != "visas" {
		t.Errorf("expected score=20 cat=visas, got score=%d cat=%q", score, cat)
	}
}

// ── BUG-2: 'data' (4 runes) must not match as substring ─────────────────────

func TestCalculateScore_Data_NoFalsePositive(t *testing.T) {
	kc := loadKW(t, `keywords:
  - word: "data"
    category: "tech"
    weight: 12
`)
	// "data" must NOT fire inside "opdatering", "database", "standarder"
	falsePositives := []string{
		"Opdatering af systemet er nødvendig",
		"Brug database til at gemme informationer",
		"Høje standarder for datasikkerhed", // "data" inside "datasikkerhed" is borderline but a compound word
	}
	for _, text := range falsePositives {
		score, _ := kc.CalculateScore(text)
		if score != 0 {
			t.Errorf("BUG-2 false positive for %q: score=%d (expected 0)", text, score)
		}
	}

	// "data" standalone must still match
	positives := []string{
		"Apple data center i Viborg åbner snart",
		"Beskyttelse af data er vigtig",
		"data om ukrainske flygtninge",
	}
	for _, text := range positives {
		score, cat := kc.CalculateScore(text)
		if score != 12 || cat != "tech" {
			t.Errorf("BUG-2 missed match for %q: score=%d cat=%q (expected score=12 cat=tech)", text, score, cat)
		}
	}
}

// ── BUG-3: Mærsk economy news must score positively ──────────────────────────

func TestCalculateScore_Maersk_EconomyCategory(t *testing.T) {
	kc := loadKW(t, `keywords:
  - word: "mærsk"
    category: "economy"
    weight: 25
  - word: "maersk"
    category: "economy"
    weight: 25
  - word: "shipping"
    category: "economy"
    weight: 15
  - word: "sejlads"
    category: "economy"
    weight: 12
  - word: "fodbold"
    category: "spam"
    weight: -20
`)
	// Danish title from CI logs
	score, cat := kc.CalculateScore("Mærsk stopper alle sejladser gennem Hormuzstrædet")
	if score <= 0 {
		t.Errorf("BUG-3: Mærsk article should score > 0, got %d", score)
	}
	if cat != "economy" {
		t.Errorf("BUG-3: Mærsk article should be category=economy, got %q", cat)
	}
}

func TestCalculateScore_Maersk_EnglishSpelling(t *testing.T) {
	kc := loadKW(t, `keywords:
  - word: "mærsk"
    category: "economy"
    weight: 25
  - word: "maersk"
    category: "economy"
    weight: 25
`)
	score, cat := kc.CalculateScore("Maersk halts all sailings through Strait of Hormuz")
	if score != 25 || cat != "economy" {
		t.Errorf("English Maersk spelling: expected score=25 cat=economy, got score=%d cat=%q", score, cat)
	}
}

// ── Real-world cases from CI logs ────────────────────────────────────────────

func TestCalculateScore_RealWorld_FootballNegativeScore(t *testing.T) {
	kc := loadKW(t, `keywords:
  - word: "viborg"
    category: "local"
    weight: 5
  - word: "fodbold"
    category: "spam"
    weight: -20
  - word: "superliga"
    category: "spam"
    weight: -25
  - word: "nullert"
    category: "spam"
    weight: -15
`)
	score, _ := kc.CalculateScore("FC Midtjylland må nøjes med nullert i superliga-kamp")
	if score >= 0 {
		t.Errorf("football news should score < 0, got %d", score)
	}
}
