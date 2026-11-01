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
	danish := "I alt tre organisationer modtager støtte."
	ukrainian := "Загалом 3 організації отримають підтримку."

	err := VerifyNumbers(danish, ukrainian)
	if err != nil {
		t.Fatalf("expected Danish number word 'tre' to match digit '3', got: %v", err)
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

