package main

import (
	"fmt"
	"os"
)

func main() {
	// Проверяем токен
	token := os.Getenv("HUGGINGFACE_API_KEY")
	if token != "" {
		fmt.Printf("✅ Токен найден: %s...\n", token[:10])
	} else {
		fmt.Println("❌ Токен не найден")
		return
	}

	fmt.Println("🧪 Тест завершен успешно")
}
