package news

import (
	"fmt"
	"strings"
)

// ShouldUsePhoto решает, отправлять ли фото (влезает ли текст в лимит 1024)
func ShouldUsePhoto(n News, maxRunes int, a, b, c int) bool {
	// Проверяем полную длину: Заголовок + Датский + Украинский
	// Используем руны для корректной оценки длины
	fullText := fmt.Sprintf("%s\n\n🇩🇰 %s\n\n🇺🇦 %s", n.Title, n.SummaryDanish, n.SummaryUkrainian)
	return len([]rune(fullText)) <= maxRunes
}

// FormatCaptionForPhoto создает подпись для фото (лимит 1024)
func FormatCaptionForPhoto(n News, maxRunes int, a, b int) string {
	// Строим текст: Заголовок + Датский + Украинский
	var parts []string

	// 1. Заголовок
	parts = append(parts, n.Title)

	// 2. Датский текст
	if n.SummaryDanish != "" {
		parts = append(parts, "🇩🇰 "+n.SummaryDanish)
	}

	// 3. Украинский текст
	if n.SummaryUkrainian != "" {
		parts = append(parts, "🇺🇦 "+n.SummaryUkrainian)
	}

	// 4. Ссылка (если влезает, желательно добавить)
	// Но для caption часто не хватает места, поэтому пока без неё или минимально

	caption := strings.Join(parts, "\n\n")

	// БЕЗОПАСНАЯ ОБРЕЗКА
	runes := []rune(caption)
	if len(runes) > maxRunes {
		// Обрезаем и добавляем многоточие
		return string(runes[:maxRunes-3]) + "..."
	}

	return caption
}

// FormatNewsWithImage создает текст для сообщения БЕЗ фото (лимит 4096)
func FormatNewsWithImage(n News, a, b int) string {
	var parts []string

	parts = append(parts, fmt.Sprintf("<b>%s</b>", n.Title)) // Жирный заголовок

	if n.SummaryDanish != "" {
		parts = append(parts, "🇩🇰 "+n.SummaryDanish)
	}

	if n.SummaryUkrainian != "" {
		parts = append(parts, "🇺🇦 "+n.SummaryUkrainian)
	}

	// Добавляем FunFact или TLDR если есть место (для текстовых сообщений лимит большой)
	if n.FunFact != "" {
		parts = append(parts, "💡 "+n.FunFact)
	}

	if n.Link != "" {
		parts = append(parts, fmt.Sprintf("\n🔗 <a href=\"%s\">Читати оригінал / Læs mere</a>", n.Link))
	}

	return strings.Join(parts, "\n\n")
}
