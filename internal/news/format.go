package news

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// GetMoodEmoji подбирает правильный эмодзи
func GetMoodEmoji(mood string) string {
	if strings.TrimSpace(mood) == "" {
		return "🔵"
	}
	switch strings.ToLower(mood) {
	case "positive":
		return "🟢"
	case "negative":
		return "🔴"
	case "shocking":
		return "⚡"
	case "urgent":
		return "🚨"
	default:
		return "🔵"
	}
}

// formatHeader создает красивую шапку с категорией
func formatHeader(n News) string {
	moodEmoji := GetMoodEmoji(n.Mood)
	cat := strings.ToUpper(n.Category)

	switch n.Category {
	case "visas", "work", "money":
		return "🇺🇦 <b>ВАЖЛИВО ДЛЯ УКРАЇНЦІВ</b>"
	case "society":
		return "📋 <b>ДЛЯ СОЦІАЛЬНОГО ЖИТТЯ</b>"
	case "war":
		return "⚔️ <b>ВІЙНА</b>"
	case "local":
		return "🏙️ <b>VIBORG</b>"
	case "education":
		return "🎓 <b>ОСВІТА</b>"
	case "crime":
		return "🚨 <b>CRIME</b>"
	default:
		if cat == "" {
			cat = "NYHED"
		}
	}
	return fmt.Sprintf("%s <b>%s</b>", moodEmoji, cat)
}

// removeTitleFromSummary удаляет заголовок из начала текста AI
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

// formatSmartBlock собирает текстовый блок с флагом
func formatSmartBlock(flag, title, summary string) string {
	var sb strings.Builder
	t := strings.TrimSpace(title)
	if t != "" && !strings.HasSuffix(t, ".") && !strings.HasSuffix(t, "!") && !strings.HasSuffix(t, "?") {
		t += "."
	}
	sb.WriteString(fmt.Sprintf("%s <b>%s</b>", flag, t))

	if summary != "" {
		cleanSummary := removeTitleFromSummary(summary, title)
		if cleanSummary != "" {
			sb.WriteString(" " + strings.TrimSpace(cleanSummary))
		}
	}
	return sb.String()
}

// FormatNewsWithImage - формирует длинный пост (лимит 4096)
func FormatNewsWithImage(n News, _, _ int) string {
	var b strings.Builder
	b.WriteString(formatHeader(n) + "\n")

	tldr := strings.TrimSpace(n.TLDR)
	if tldr != "" {
		b.WriteString("💬 <b>" + tldr + "</b>\n")
	}
	b.WriteString("\n")

	b.WriteString(formatSmartBlock("🇩🇰", n.Title, n.SummaryDanish))
	b.WriteString("\n\n")

	ukTitle := n.TitleUkrainian
	if ukTitle == "" {
		ukTitle = n.Title
	}
	b.WriteString(formatSmartBlock("🇺🇦", ukTitle, n.SummaryUkrainian))
	b.WriteString("\n\n")

	if n.Link != "" {
		b.WriteString("🔗 <a href=\"" + n.Link + "\">Læs mere / Читати оригінал</a>\n")
	}

	if len(n.Tags) > 0 {
		var tags []string
		for _, t := range n.Tags {
			t = strings.TrimSpace(strings.TrimPrefix(t, "#"))
			if t != "" {
				tags = append(tags, "#"+strings.ReplaceAll(t, " ", "_"))
			}
		}
		if len(tags) > 0 {
			b.WriteString("<i>" + strings.Join(tags, " ") + "</i>\n")
		}
	}

	fact := strings.TrimSpace(n.FunFact)
	if fact != "" {
		b.WriteString("\n━━━━━━━━━━━━━━━\n")
		b.WriteString("💡 <i>" + fact + "</i>")
	}

	return b.String()
}

// FormatCaptionForPhoto - формирует короткую подпись под фото (лимит 1024)
func FormatCaptionForPhoto(n News, maxLen int, _, _ int) string {
	if maxLen > 1024 {
		maxLen = 1024
	}

	ukTitle := n.TitleUkrainian
	if ukTitle == "" {
		ukTitle = n.Title
	}

	var sb strings.Builder

	sb.WriteString(formatHeader(n) + "\n")
	if n.TLDR != "" {
		sb.WriteString("💬 <b>" + n.TLDR + "</b>\n")
	}
	sb.WriteString("\n")

	sb.WriteString(formatSmartBlock("🇩🇰", n.Title, n.SummaryDanish))
	sb.WriteString("\n\n")
	sb.WriteString(formatSmartBlock("🇺🇦", ukTitle, n.SummaryUkrainian))

	if len(n.Tags) > 0 {
		var tags []string
		for _, t := range n.Tags {
			t = strings.TrimSpace(strings.TrimPrefix(t, "#"))
			if t != "" {
				tags = append(tags, "#"+strings.ReplaceAll(t, " ", "_"))
			}
		}
		if len(tags) > 0 {
			sb.WriteString("\n\n" + strings.Join(tags, " "))
		}
	}

	if n.FunFact != "" {
		sb.WriteString("\n\n━━━━━━━━━━━━━━━\n💡 <i>" + n.FunFact + "</i>")
	}

	result := sb.String()

	// Защита: жесткая обрезка, если не влезает
	if utf8.RuneCountInString(result) > maxLen {
		runes := []rune(result)
		return string(runes[:maxLen-3]) + "..."
	}

	return result
}

// ShouldUsePhoto проверяет, помещается ли текст под фото
func ShouldUsePhoto(n News, maxLen int, _, _, _ int) bool {
	caption := FormatCaptionForPhoto(n, maxLen, 0, 0)
	count := utf8.RuneCountInString(caption)
	return count <= maxLen && count > 100
}
