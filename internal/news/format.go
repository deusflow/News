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
		summaryRunes := []rune(summary)
		titleRunes := []rune(normalizedTitle)
		if len(summaryRunes) >= len(titleRunes) {
			rest := string(summaryRunes[len(titleRunes):])
			rest = strings.TrimLeft(rest, ".!?:;,:-–— \n\t")
			if rest != "" {
				return strings.TrimSpace(rest)
			}
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

	// 5. Почему это важно (короткий редакторский вывод)
	if w := strings.TrimSpace(n.WhyItMatters); w != "" {
		b.WriteString("🧭 <b>Чому це важливо:</b> ")
		b.WriteString(w)
		b.WriteString("\n\n")
	}

	// 6. Теги
	if tags := formatTags(n.Tags); tags != "" {
		b.WriteString(tags)
		b.WriteString("\n")
	}

	// 7. Факт (через разделитель)
	if fact := strings.TrimSpace(n.FunFact); fact != "" {
		b.WriteString("\n━━━━━━━━━━━━━━━\n")
		b.WriteString("💡 <i>" + fact + "</i>")
	}

	return b.String()
}

// ──────────────────────────────────────────────────────────────────────
// FormatCaptionForPhoto — подпись под фото (лимит 1024 символа Telegram).
//
// ВАЖНО: эта функция НИКОГДА не обрезает контент.
// Если текст не влезает в maxLen — возвращает "".
// ShouldUsePhoto проверит это и переключится на текстовый режим (4096 лимит),
// где новость будет показана полностью на обоих языках одинаковой длины.
//
// Приоритет при нехватке места:
//  1. Убрать fun_fact
//  2. Убрать теги
//  3. Если core (header+tldr+DK+UA) не влезает — вернуть "" → текстовый режим
//
// ──────────────────────────────────────────────────────────────────────
func FormatCaptionForPhoto(n News, maxLen int) string {
	// Telegram API hard limit for photo captions is 1024 characters.
	// Do NOT raise this above 1024 — Telegram will reject the request with 400.
	if maxLen <= 0 || maxLen > 1024 {
		maxLen = 1024
	}

	ukTitle := strings.TrimSpace(n.TitleUkrainian)
	if ukTitle == "" {
		ukTitle = strings.TrimSpace(n.Title)
	}
	dkBody := strings.TrimSpace(removeTitleFromSummary(n.SummaryDanish, n.Title))
	uaBody := strings.TrimSpace(removeTitleFromSummary(n.SummaryUkrainian, ukTitle))
	why := strings.TrimSpace(n.WhyItMatters)

	// buildCore assembles the mandatory content block — never trimmed.
	var sb strings.Builder
	sb.WriteString(formatHeader(n))
	sb.WriteString("\n\n")
	if tldr := strings.TrimSpace(n.TLDR); tldr != "" {
		sb.WriteString("💬 <b>" + tldr + "</b>")
		sb.WriteString("\n\n")
	}
	sb.WriteString("🇩🇰 <b>" + strings.TrimSpace(n.Title) + "</b>")
	if dkBody != "" {
		sb.WriteString("\n" + dkBody)
	}
	sb.WriteString("\n\n")
	sb.WriteString("🇺🇦 <b>" + ukTitle + "</b>")
	if uaBody != "" {
		sb.WriteString("\n" + uaBody)
	}
	core := sb.String()

	// If core alone doesn't fit — signal caller to use text mode instead.
	if utf8.RuneCountInString(core) > maxLen {
		return ""
	}

	// Try to add why_it_matters
	withWhy := core
	if why != "" {
		withWhy = core + "\n\n🧭 <b>Чому це важливо:</b> " + why
	}

	// Try to add tags
	tags := formatTags(n.Tags)
	withTags := withWhy
	if tags != "" {
		withTags = withWhy + "\n\n" + tags
	}

	// Try to add fun_fact
	if fact := strings.TrimSpace(n.FunFact); fact != "" {
		full := withTags + "\n\n━━━━━━━━━━━━━━━\n💡 <i>" + fact + "</i>"
		if utf8.RuneCountInString(full) <= maxLen {
			return full
		}
	}

	// Fact didn't fit — try with tags and why
	if utf8.RuneCountInString(withTags) <= maxLen {
		return withTags
	}

	// Tags didn't fit — try with why only
	if utf8.RuneCountInString(withWhy) <= maxLen {
		return withWhy
	}

	// Why didn't fit either — return core only (always fits, checked above)
	return core
}

// ShouldUsePhoto возвращает true только если:
//   - у новости есть ImageURL
//   - FormatCaptionForPhoto вернул непустую строку (весь контент влезает)
//
// Если контент не влезает — бот автоматически переключится на текстовый режим
// где лимит 4096 символов и новость будет показана полностью на обоих языках.
func ShouldUsePhoto(n News, maxLen int) bool {
	if n.ImageURL == "" {
		return false
	}
	caption := FormatCaptionForPhoto(n, maxLen)
	return caption != ""
}
