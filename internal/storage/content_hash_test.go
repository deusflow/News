package storage

import "testing"

func TestGenerateContentHash(t *testing.T) {
	tests := []struct {
		name     string
		content1 string
		content2 string
		sameHash bool
	}{
		{
			name:     "Identical content",
			content1: "Dette er en test nyhed om 55000 ukrainske soldater der er faldet i krigen.",
			content2: "Dette er en test nyhed om 55000 ukrainske soldater der er faldet i krigen.",
			sameHash: true,
		},
		{
			name:     "Same content different punctuation",
			content1: "Zelenskyj: 55.000 ukrainske soldater er døde i krigen!",
			content2: "Zelenskyj - 55000 ukrainske soldater er døde i krigen",
			sameHash: true,
		},
		{
			name:     "Same content different case",
			content1: "BREAKING: Ukraine reports 55000 soldiers killed",
			content2: "breaking: ukraine reports 55000 soldiers killed",
			sameHash: true,
		},
		{
			name:     "Completely different content",
			content1: "Zelenskyj melder om 55000 dræbte ukrainske soldater i krigen mod Rusland.",
			content2: "Danmark sender 100 millioner kroner til humanitær hjælp i Afrika.",
			sameHash: false,
		},
		{
			name:     "Similar topic different numbers",
			content1: "1000 mennesker evakueret fra Kiev",
			content2: "2000 mennesker evakueret fra Kharkiv",
			sameHash: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1 := generateContentHash(tt.content1)
			hash2 := generateContentHash(tt.content2)

			if tt.sameHash && hash1 != hash2 {
				t.Errorf("Expected same hash for:\n%q\n%q\nGot: %s vs %s", tt.content1, tt.content2, hash1, hash2)
			}
			if !tt.sameHash && hash1 == hash2 {
				t.Errorf("Expected different hash for:\n%q\n%q\nGot same: %s", tt.content1, tt.content2, hash1)
			}
		})
	}
}

func TestGenerateContentHash_RealWorldDuplicates(t *testing.T) {
	// These are real examples of the same news from DR and Ekstra Bladet
	drContent := `Zelenskyj melder om 55.000 dræbte ukrainske soldater. 
	Præsident Volodymyr Zelenskyj har for første gang offentliggjort et officielt tal 
	for ukrainske militære tab i krigen mod Rusland. Ifølge præsidenten er 55.000 
	ukrainske soldater blevet dræbt siden invasionen begyndte i februar 2022.`

	ebContent := `I krig i fire år: 55.000 ukrainske soldater har mistet livet.
	Ukraines præsident, Volodymyr Zelenskyj, oplyser at 55.000 ukrainske soldater 
	er faldet i krigen mod Rusland. Det er første gang at Ukraine officielt 
	offentliggør tabstallene siden Ruslands invasion i februar 2022.`

	hash1 := generateContentHash(drContent)
	hash2 := generateContentHash(ebContent)

	// These should have DIFFERENT hashes because the wording is different
	// Content hash catches exact/near-exact duplicates, not semantic duplicates
	// But both articles about the same event will share key phrases
	t.Logf("DR hash: %s", hash1)
	t.Logf("EB hash: %s", hash2)

	// Note: These will likely have different hashes because the wording is different
	// This is expected - we're catching exact duplicates, not semantic ones
	// For semantic duplicates, we'd need AI or more sophisticated algorithms
}

func TestGenerateContentHash_LegacyCompatibilityPath(t *testing.T) {
	content := "Zelenskyj melder om 55.000 draebte ukrainske soldater i krigen mod Rusland."
	newHash := generateContentHash(content)
	legacyHash := generateLegacyContentHash(content)

	if newHash == legacyHash {
		t.Fatalf("expected new hash and legacy hash to differ, both are %q", newHash)
	}
	if len(legacyHash) != 16 {
		t.Fatalf("expected legacy hash length 16, got %d", len(legacyHash))
	}
}

func TestGenerateContentHash_ShortContent(t *testing.T) {
	shortContent := "Test"
	hash := generateContentHash(shortContent)

	if len(hash) != 32 { // 128-bit (16 bytes) represented as hex
		t.Errorf("Expected 32 char hash, got %d chars: %s", len(hash), hash)
	}
}
