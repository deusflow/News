package main

import (
	"dknews/internal/translate"
	"fmt"
	"os"
)

func main() {
	// Проверяем наличие токена
	if token := os.Getenv("HUGGINGFACE_API_KEY"); token != "" {
		fmt.Printf("✅ Hugging Face токен найден: %s...\n", token[:10])
	} else {
		fmt.Println("⚠️ Hugging Face токен не найден")
	}

	// Тестируем перевод
	testTexts := []string{
		"Regeringen annoncerede i dag nye tiltag for at hjælpe ukrainske flygtninge.",
		"Danmark sender våben til Ukraine for at støtte forsvaret.",
		"Ny lovgivning om visa for ukrainere i Danmark træder i kraft.",
	}

	for i, text := range testTexts {
		fmt.Printf("\n=== Тест %d ===\n", i+1)
		fmt.Printf("Оригинал: %s\n", text)

		translation, err := translate.TranslateText(text, "da", "uk")
		if err != nil {
			fmt.Printf("❌ Ошибка: %v\n", err)
		} else {
			fmt.Printf("Перевод: %s\n", translation)
		}
	}
}
