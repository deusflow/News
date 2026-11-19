package news

import (
	"strings"
	"testing"
)

func TestVerifyNumbers_ValidMatches(t *testing.T) {
	danish := "Regeringen afsætter 50.000 kr. til projektet fra 2026. Derudover stiger taksten med 15% for fem kommuner."
	ukrainian := "Уряд виділяє 50 000 крон на проєкт із 2026 року. Тариф зросте на 15% для 5 комун."

	err := VerifyNumbers(danish, ukrainian)
	if err != nil {
		t.Fatalf("expected numbers to verify successfully, got: %v", err)
	}
}

func TestVerifyNumbers_DanishWordToDigit(t *testing.T) {
	danish := "I alt tyve organisationer modtager støtte, og bidraget stiger med femten procent."
	ukrainian := "Загалом 20 організацій отримають підтримку, а внесок зросте на 15%."

	err := VerifyNumbers(danish, ukrainian)
	if err != nil {
		t.Fatalf("expected Danish number words 'tyve'/'femten' to match digits '20'/'15%%', got: %v", err)
	}
}

func TestVerifyNumbers_SingleDigitPhrasingAllowed(t *testing.T) {
	danish := "Regeringen indfører nye regler i de kommende dage."
	ukrainian := "Уряд запроваджує 3 нові правила у найближчі дні."

	if err := VerifyNumbers(danish, ukrainian); err != nil {
		t.Fatalf("expected single-digit stylistic number to be allowed without false-positive rejection, got: %v", err)
	}

	// Single-digit percentages or decimals must STILL be verified against source
	ukrainianFakePercent := "Уряд підвищує податок на 5%."
	if err := VerifyNumbers(danish, ukrainianFakePercent); err == nil {
		t.Fatalf("expected hallucinated single-digit percentage 5%% to be rejected")
	}
}

func TestVerifyNumbers_HallucinatedNumberDetected(t *testing.T) {
	danish := "Regeringen overvejer en ny reform for skoler."
	ukrainian := "Уряд виділив 450 мільйонів крон на нову реформу шкіл."

	err := VerifyNumbers(danish, ukrainian)
	if err == nil {
		t.Fatalf("expected hallucinated number 450 to be detected and rejected")
	}
	if !strings.Contains(err.Error(), "450") {
		t.Fatalf("expected error message to mention 450, got: %v", err)
	}
}

func TestVerifyUkrainian_RussianLettersRejected(t *testing.T) {
	russianSamples := []string{
		"Правительство приняло решение об этом",
		"Новый закон вступил в силу",
		"Это важное событие для всех",
		"Всё прошло успешно и надёжно",
	}

	for _, sample := range russianSamples {
		if err := VerifyUkrainian(sample); err == nil {
			t.Errorf("expected text %q with Russian letters to be rejected", sample)
		}
	}

	validUkrainian := "Уряд ухвалив нове рішення щодо соціальної допомоги та виплат у Данії."
	if err := VerifyUkrainian(validUkrainian); err != nil {
		t.Errorf("expected valid Ukrainian to pass, got: %v", err)
	}
}

func TestGenerateNewsUserContent_TruncatesLongContent(t *testing.T) {
	longBody := strings.Repeat("Dansk tekst om nyheder og regler. ", 250) // ~8500 chars
	out := GenerateNewsUserContent("Kort titel", longBody)
	if len([]rune(out)) > maxAIContentRunes+100 {
		t.Errorf("expected truncated prompt under ~%d runes, got %d", maxAIContentRunes+100, len([]rune(out)))
	}
	if !strings.HasSuffix(out, "...") {
		t.Errorf("expected truncated content to end with ellipsis")
	}
}

func TestIsSameRunDuplicateTitle(t *testing.T) {
	processed := []string{
		"Person fundet livløs i Hornslet efter eksplosion i container",
	}
	dup, matched := isSameRunDuplicateTitle("Person fundet livløs i Hornslet efter eksplosion i container", processed)
	if !dup || matched == "" {
		t.Errorf("expected exact duplicate headline to be detected")
	}
	dupSimilar, _ := isSameRunDuplicateTitle("Person fundet livløs efter eksplosion i container i Hornslet", processed)
	if !dupSimilar {
		t.Errorf("expected near-identical headline to be detected by bigram similarity")
	}
	diff, _ := isSameRunDuplicateTitle("Ecco fyrer 76 i Danmark", processed)
	if diff {
		t.Errorf("expected unrelated headline not to be marked duplicate")
	}
}

