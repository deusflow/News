package news

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// moodEmoji maps validated mood values to their display emoji.
var moodEmoji = map[string]string{
	"positive": "🟢",
	"negative": "🔴",
	"shocking": "⚡",
	"urgent":   "🚨",
	"neutral":  "🔵",
}

// GetMoodEmoji returns the display emoji for a mood string.
func GetMoodEmoji(mood string) string {
	if e, ok := moodEmoji[strings.ToLower(strings.TrimSpace(mood))]; ok {
		return e
	}
	return "🔵"
}

// ──────────────────────────────────────────────────────────────────────
// formatHeader — строка ТЕМЫ (первая строка поста).
//
//	💻 ТЕХНОЛОГІЇ
//	🏙️ VIBORG
//	🇺🇦 ВАЖЛИВО ДЛЯ УКРАЇНЦІВ
//
// ──────────────────────────────────────────────────────────────────────
func formatHeader(n News) string {
	cat := ValidateCategory(n.Category)
	return fmt.Sprintf("%s <b>%s</b>", CategoryEmoji(cat), CategoryLabel(cat))
}

// ──────────────────────────────────────────────────────────────────────
// formatTags — хештеги одной строкой.
//
//	#оборона #Данія #рекрутинг
//
// ──────────────────────────────────────────────────────────────────────
func formatTags(tags []string) string {
	var out []string
	for _, t := range tags {
		t = strings.TrimSpace(strings.TrimPrefix(t, "#"))
		if t != "" {
			out = append(out, "#"+strings.ReplaceAll(t, " ", "_"))
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " ")
}

// ──────────────────────────────────────────────────────────────────────
// removeTitleFromSummary — если AI повторил заголовок в начале body,
// убираем его, чтобы не было дубля.
// ──────────────────────────────────────────────────────────────────────
func removeTitleFromSummary(summary, title string) string {
	if summary == "" || title == "" {
		return summary
	}
	summary = strings.TrimSpace(summary)
	title = strings.TrimSpace(title)

	normalizedTitle := strings.TrimRight(strings.ToLower(title), ".!?:;,-–—")
	if normalizedTitle == "" {
		return summary
	}

	summaryLower := strings.ToLower(summary)
	if strings.HasPrefix(summaryLower, normalizedTitle) {
		rest := summary[len(normalizedTitle):]
		rest = strings.TrimLeft(rest, ".!?:;,:-–— \n\t")
		if rest != "" {
			return strings.TrimSpace(rest)
		}
	}
	return summary
}

// ──────────────────────────────────────────────────────────────────────
// FormatNewsWithImage — длинный пост без фото (лимит 4096 символов).
// Используется когда фото нет или текст не влезает в caption.
//
// Формат:
//
//	💻 ТЕХНОЛОГІЇ
//	🏙️ VIBORG
//	🇺🇦 ВАЖЛИВО ДЛЯ УКРАЇНЦІВ
//
//	💬 TLDR-заголовок на украинском
//
//	🇩🇰 Заголовок на датском.
//	Текст новости 2-4 предложения.
//
//	🇺🇦 Заголовок на украинском.
//	Текст новости 2-4 предложения.
//
//	#тег1 #тег2 #тег3
//
//	━━━━━━━━━━━━━━━
//	💡 Цікавий факт
//
// Ссылка "🔗 Читати оригінал" вынесена в inline-кнопку (app.go),
// поэтому в тексте её нет.
// ──────────────────────────────────────────────────────────────────────
func FormatNewsWithImage(n News) string {
	var b strings.Builder

	// 1. Тема
	b.WriteString(formatHeader(n))
	b.WriteString("\n\n")

	// 2. TLDR-заголовок
	if tldr := strings.TrimSpace(n.TLDR); tldr != "" {
		b.WriteString("💬 <b>" + tldr + "</b>")
		b.WriteString("\n\n")
	}

	// 3. Датская версия: заголовок + body
	b.WriteString("🇩🇰 <b>" + strings.TrimSpace(n.Title) + "</b>")
	if dk := strings.TrimSpace(removeTitleFromSummary(n.SummaryDanish, n.Title)); dk != "" {
		b.WriteString("\n" + dk)
	}
	b.WriteString("\n\n")

	// 4. Украинская версия: заголовок + body
	ukTitle := strings.TrimSpace(n.TitleUkrainian)
	if ukTitle == "" {
		ukTitle = strings.TrimSpace(n.Title)
	}
	b.WriteString("🇺🇦 <b>" + ukTitle + "</b>")
	if ua := strings.TrimSpace(removeTitleFromSummary(n.SummaryUkrainian, ukTitle)); ua != "" {
		b.WriteString("\n" + ua)
	}
	b.WriteString("\n\n")

	// 5. Теги
	if tags := formatTags(n.Tags); tags != "" {
		b.WriteString(tags)
		b.WriteString("\n")
	}

	// 6. Факт (через разделитель)
	if fact := strings.TrimSpace(n.FunFact); fact != "" {
		b.WriteString("\n━━━━━━━━━━━━━━━\n")
		b.WriteString("💡 <i>" + fact + "</i>")
	}

	return b.String()
}

// ──────────────────────────────────────────────────────────────────────
// FormatCaptionForPhoto — подпись под фото (лимит 1024 символа Telegram).
//
// Тот же формат что FormatNewsWithImage, но с жёсткой обрезкой.
// Если не влезает — сначала убираем факт, потом теги, потом обрезаем.
// ──────────────────────────────────────────────────────────────────────
func FormatCaptionForPhoto(n News, maxLen int) string {
	if maxLen <= 0 || maxLen > 1024 {
		maxLen = 1024
	}

	ukTitle := strings.TrimSpace(n.TitleUkrainian)
	if ukTitle == "" {
		ukTitle = strings.TrimSpace(n.Title)
	}

	var sb strings.Builder

	// 1. Тема
	sb.WriteString(formatHeader(n))
	sb.WriteString("\n\n")

	// 2. TLDR
	if tldr := strings.TrimSpace(n.TLDR); tldr != "" {
		sb.WriteString("💬 <b>" + tldr + "</b>")
		sb.WriteString("\n\n")
	}

	// 3. Датская
	sb.WriteString("🇩🇰 <b>" + strings.TrimSpace(n.Title) + "</b>")
	if dk := strings.TrimSpace(removeTitleFromSummary(n.SummaryDanish, n.Title)); dk != "" {
		sb.WriteString("\n" + dk)
	}
	sb.WriteString("\n\n")

	// 4. Украинская
	sb.WriteString("🇺🇦 <b>" + ukTitle + "</b>")
	if ua := strings.TrimSpace(removeTitleFromSummary(n.SummaryUkrainian, ukTitle)); ua != "" {
		sb.WriteString("\n" + ua)
	}

	// Основной контент готов — проверяем сколько осталось места
	core := sb.String()
	coreLen := utf8.RuneCountInString(core)

	// 5. Теги (добавляем если есть место)
	tags := formatTags(n.Tags)
	tagsBlock := ""
	if tags != "" {
		tagsBlock = "\n\n" + tags
	}

	// 6. Факт (добавляем если есть место)
	factBlock := ""
	if fact := strings.TrimSpace(n.FunFact); fact != "" {
		factBlock = "\n\n━━━━━━━━━━━━━━━\n💡 <i>" + fact + "</i>"
	}

	// Пробуем полную версию (core + tags + fact)
	full := core + tagsBlock + factBlock
	if utf8.RuneCountInString(full) <= maxLen {
		return full
	}

	// Не влезает — убираем факт
	withTags := core + tagsBlock
	if utf8.RuneCountInString(withTags) <= maxLen {
		return withTags
	}

	// Не влезает даже без факта — убираем теги тоже
	if coreLen <= maxLen {
		return core
	}

	// Даже core не влезает — жёсткая обрезка
	runes := []rune(core)
	return string(runes[:maxLen-3]) + "..."
}

// ShouldUsePhoto проверяет, помещается ли контент под фото (caption ≤ maxLen).
func ShouldUsePhoto(n News, maxLen int) bool {
	caption := FormatCaptionForPhoto(n, maxLen)
	count := utf8.RuneCountInString(caption)
	return count <= maxLen && count > 100
}
