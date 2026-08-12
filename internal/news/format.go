package news

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	youtubeURLRegex = regexp.MustCompile(`https?://(?:www\.)?(?:youtube\.com/watch\?v=[^\s<]+|youtu\.be/[^\s<]+)`)
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
// formatHeader — строка ТЕМЫ (перша строка поста).
//
//	💻 ТЕХНОЛОГІЇ
//	🏙️ VIBORG
//	🇺🇦 ВАЖЛИВО ДЛЯ УКРАЇНЦІВ
//
// ──────────────────────────────────────────────────────────────────────
func formatHeader(n News) string {
	cat := ValidateCategory(n.Category)
	header := fmt.Sprintf("%s <b>%s</b>", CategoryEmoji(cat), CategoryLabel(cat))
	if n.IsExclusive {
		header += "\n💎 <b>Ексклюзив (знайдено лише тут)</b>"
	}
	return header
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

// normalizeVideoLinksForTelegram removes raw YouTube URLs from the middle of text.
// These URLs are then added separately as plain text at the end,
// allowing Telegram to generate native preview with 100% reliability.
func normalizeVideoLinksForTelegram(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	// Remove YouTube URLs from the body — they'll be appended separately
	text = youtubeURLRegex.ReplaceAllString(text, "")
	// Clean up extra spaces/newlines that might be left
	text = strings.Join(strings.Fields(text), " ")
	return text
}

// ExtractVideoURL returns the first YouTube URL found in news fields.
func ExtractVideoURL(n News) string {
	if strings.TrimSpace(n.VideoURL) != "" {
		return strings.TrimRight(strings.TrimSpace(n.VideoURL), ".,;:!?)")
	}
	searchSpace := []string{
		n.Title,
		n.Summary,
		n.SummaryDanish,
		n.SummaryUkrainian,
		n.Content,
	}
	for _, part := range searchSpace {
		if part == "" {
			continue
		}
		if u := youtubeURLRegex.FindString(part); u != "" {
			return strings.TrimRight(strings.TrimSpace(u), ".,;:!?)")
		}
	}
	return ""
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
	n.TLDR = sanitizeAIField(n.TLDR)
	n.FunFact = sanitizeAIField(n.FunFact)
	n.WhyItMatters = sanitizeAIField(n.WhyItMatters)
	n.SummaryDanish = sanitizeAIField(n.SummaryDanish)
	n.SummaryUkrainian = sanitizeAIField(n.SummaryUkrainian)

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
	dkTitle := strings.TrimSpace(n.TitleDanish)
	if dkTitle == "" {
		dkTitle = strings.TrimSpace(n.Title)
	}
	b.WriteString("🇩🇰 <b>" + dkTitle + "</b>")
	if dk := strings.TrimSpace(normalizeVideoLinksForTelegram(removeTitleFromSummary(n.SummaryDanish, dkTitle))); dk != "" {
		b.WriteString("\n" + dk)
	}
	b.WriteString("\n\n")

	// 4. Украинская версия: заголовок + body
	ukTitle := strings.TrimSpace(n.TitleUkrainian)
	if ukTitle == "" {
		ukTitle = strings.TrimSpace(n.Title)
	}
	b.WriteString("🇺🇦 <b>" + ukTitle + "</b>")
	if ua := strings.TrimSpace(normalizeVideoLinksForTelegram(removeTitleFromSummary(n.SummaryUkrainian, ukTitle))); ua != "" {
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

	// 8. YouTube URL (plain text at the end for native Telegram preview)
	if videoURL := ExtractVideoURL(n); videoURL != "" {
		b.WriteString("\n\n" + videoURL)
	}

	return b.String()
}

// ──────────────────────────────────────────────────────────────────────
// FormatLongreadTeaser — короткий тізер для фотографії.
// Формат:
// 💻 ТЕХНОЛОГІЇ
//
// 💬 TLDR-заголовок
//
// 👇 Детальний розбір читайте у наступному повідомленні
// ──────────────────────────────────────────────────────────────────────
func FormatLongreadTeaser(n News) string {
	n.TLDR = sanitizeAIField(n.TLDR)

	var b strings.Builder
	b.WriteString(formatHeader(n))
	b.WriteString("\n\n")

	if tldr := strings.TrimSpace(n.TLDR); tldr != "" {
		b.WriteString("💬 <b>" + tldr + "</b>")
		b.WriteString("\n\n")
	}

	b.WriteString("👇 <i>Детальний розбір читайте у наступному повідомленні</i>")

	return b.String()
}

// ──────────────────────────────────────────────────────────────────────
// FormatLongreadBody — повний лонгрід для відповіді (реплаю).
// Формат такий самий як FormatNewsWithImage, але без Header та TLDR,
// щоб уникнути дублювання інформації, яка вже є в тізері.
// ──────────────────────────────────────────────────────────────────────
func FormatLongreadBody(n News) string {
	n.FunFact = sanitizeAIField(n.FunFact)
	n.WhyItMatters = sanitizeAIField(n.WhyItMatters)
	n.SummaryDanish = sanitizeAIField(n.SummaryDanish)
	n.SummaryUkrainian = sanitizeAIField(n.SummaryUkrainian)

	var b strings.Builder

	// 1. Датская версия: заголовок + body
	dkTitle := strings.TrimSpace(n.TitleDanish)
	if dkTitle == "" {
		dkTitle = strings.TrimSpace(n.Title)
	}
	b.WriteString("🇩🇰 <b>" + dkTitle + "</b>")
	if dk := strings.TrimSpace(normalizeVideoLinksForTelegram(removeTitleFromSummary(n.SummaryDanish, dkTitle))); dk != "" {
		b.WriteString("\n" + dk)
	}
	b.WriteString("\n\n")

	// 2. Украинская версия: заголовок + body
	ukTitle := strings.TrimSpace(n.TitleUkrainian)
	if ukTitle == "" {
		ukTitle = strings.TrimSpace(n.Title)
	}
	b.WriteString("🇺🇦 <b>" + ukTitle + "</b>")
	if ua := strings.TrimSpace(normalizeVideoLinksForTelegram(removeTitleFromSummary(n.SummaryUkrainian, ukTitle))); ua != "" {
		b.WriteString("\n" + ua)
	}
	b.WriteString("\n\n")

	// 3. Почему это важно
	if w := strings.TrimSpace(n.WhyItMatters); w != "" {
		b.WriteString("🧭 <b>Чому це важливо:</b> ")
		b.WriteString(w)
		b.WriteString("\n\n")
	}

	// 4. Теги
	if tags := formatTags(n.Tags); tags != "" {
		b.WriteString(tags)
		b.WriteString("\n")
	}

	// 5. Факт
	if fact := strings.TrimSpace(n.FunFact); fact != "" {
		b.WriteString("\n━━━━━━━━━━━━━━━\n")
		b.WriteString("💡 <i>" + fact + "</i>")
	}

	// 6. YouTube URL
	if videoURL := ExtractVideoURL(n); videoURL != "" {
		b.WriteString("\n\n" + videoURL)
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
	n.TLDR = sanitizeAIField(n.TLDR)
	n.FunFact = sanitizeAIField(n.FunFact)
	n.WhyItMatters = sanitizeAIField(n.WhyItMatters)
	n.SummaryDanish = sanitizeAIField(n.SummaryDanish)
	n.SummaryUkrainian = sanitizeAIField(n.SummaryUkrainian)

	// Telegram API hard limit for photo captions is 1024 characters.
	// Do NOT raise this above 1024 — Telegram will reject the request with 400.
	if maxLen <= 0 || maxLen > 1024 {
		maxLen = 1024
	}

	dkTitle := strings.TrimSpace(n.TitleDanish)
	if dkTitle == "" {
		dkTitle = strings.TrimSpace(n.Title)
	}
	ukTitle := strings.TrimSpace(n.TitleUkrainian)
	if ukTitle == "" {
		ukTitle = strings.TrimSpace(n.Title)
	}
	dkBody := strings.TrimSpace(normalizeVideoLinksForTelegram(removeTitleFromSummary(n.SummaryDanish, dkTitle)))
	uaBody := strings.TrimSpace(normalizeVideoLinksForTelegram(removeTitleFromSummary(n.SummaryUkrainian, ukTitle)))
	why := strings.TrimSpace(n.WhyItMatters)

	// buildCore assembles the mandatory content block — never trimmed.
	var sb strings.Builder
	sb.WriteString(formatHeader(n))
	sb.WriteString("\n\n")
	if tldr := strings.TrimSpace(n.TLDR); tldr != "" {
		sb.WriteString("💬 <b>" + tldr + "</b>")
		sb.WriteString("\n\n")
	}
	sb.WriteString("🇩🇰 <b>" + dkTitle + "</b>")
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
			// Try to add YouTube URL if available
			if videoURL := ExtractVideoURL(n); videoURL != "" {
				fullWithVideo := full + "\n\n" + videoURL
				if utf8.RuneCountInString(fullWithVideo) <= maxLen {
					return fullWithVideo
				}
			}
			return full
		}
	}

	// Fact didn't fit — try with tags and why
	if utf8.RuneCountInString(withTags) <= maxLen {
		// Try to add YouTube URL if available
		if videoURL := ExtractVideoURL(n); videoURL != "" {
			withVideo := withTags + "\n\n" + videoURL
			if utf8.RuneCountInString(withVideo) <= maxLen {
				return withVideo
			}
		}
		return withTags
	}

	// Tags didn't fit — try with why only
	if utf8.RuneCountInString(withWhy) <= maxLen {
		// Try to add YouTube URL if available
		if videoURL := ExtractVideoURL(n); videoURL != "" {
			withVideo := withWhy + "\n\n" + videoURL
			if utf8.RuneCountInString(withVideo) <= maxLen {
				return withVideo
			}
		}
		return withWhy
	}

	// Why didn't fit either — return core only (always fits, checked above)
	// But try to add YouTube URL if it fits
	if videoURL := ExtractVideoURL(n); videoURL != "" {
		coreWithVideo := core + "\n\n" + videoURL
		if utf8.RuneCountInString(coreWithVideo) <= maxLen {
			return coreWithVideo
		}
	}
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

// sanitizeAIField cleans up common AI hallucinations from text fields:
// - Removes unwanted bolding (**)
// - Replaces common wrong flags with the Danish flag if they appear at the start
func sanitizeAIField(s string) string {
	s = strings.TrimSpace(s)
	// Remove markdown bolding and italics
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "*", "")

	// Fix wrong flags at the start (AI sometimes confuses Scandinavian flags)
	wrongFlags := []string{"🇸🇪", "🇳🇴", "🇫🇮", "🇮🇸"}
	for _, flag := range wrongFlags {
		if strings.HasPrefix(s, flag) {
			s = "🇩🇰" + strings.TrimPrefix(s, flag)
		}
	}
	return strings.TrimSpace(s)
}
