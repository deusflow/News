package app

import (
	"dknews/internal/news"
	"dknews/internal/rss"
	"dknews/internal/telegram"
	"fmt"
	"log"
	"os"
	"strings"
)

// formatNewsMessage makes better message for Telegram (only Danish + Ukrainian)
func formatNewsMessage(newsList []news.News, max int) string {
	var b strings.Builder

	// Group news by category
	ukraineNews := []news.News{}
	denmarkNews := []news.News{}
	for _, n := range newsList {
		if len(ukraineNews)+len(denmarkNews) >= max {
			break
		}

		if n.Category == "ukraine" {
			ukraineNews = append(ukraineNews, n)
		} else if n.Category == "denmark" {
			denmarkNews = append(denmarkNews, n)
		}
	}

	b.WriteString("🇩🇰 <b>Новини Данії</b> 🇺🇦\n")
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	newsCount := 1

	// First Ukraine news (priority)
	if len(ukraineNews) > 0 {
		b.WriteString("🇺🇦 <b>УКРАЇНА В ДАНІЇ</b>\n\n")
		for _, n := range ukraineNews {
			if newsCount > max {
				break
			}
			b.WriteString(formatSingleNews(n, newsCount))
			newsCount++
		}
	}

	// Then important Denmark news
	if len(denmarkNews) > 0 {
		if len(ukraineNews) > 0 {
			b.WriteString("\n🇩🇰 <b>ВАЖЛИВІ НОВИНИ ДАНІЇ</b>\n\n")
		}
		for _, n := range denmarkNews {
			if newsCount > max {
				break
			}
			b.WriteString(formatSingleNews(n, newsCount))
			newsCount++
		}
	}

	b.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	b.WriteString("📱 Danish News Bot | Щодня о 8:00 UTC")

	return b.String()
}

// formatSingleNews formats one news only with Ukrainian translation
func formatSingleNews(n news.News, number int) string {
	var b strings.Builder

	// Set emoji by category
	emoji := "📰"
	if n.Category == "ukraine" {
		emoji = "🔥"
	}

	// Title with link
	b.WriteString(fmt.Sprintf("%s <b>%d.</b> <a href=\"%s\">%s</a>\n", emoji, number, n.Link, n.Title))

	// Ukrainian translation of title (show only if has translation)
	if n.TitleUK != "" && n.TitleUK != n.Title {
		b.WriteString(fmt.Sprintf("🇺🇦 <i>%s</i>\n", n.TitleUK))
	}

	b.WriteString("\n")

	// Show full content original (limit for Telegram)
	if n.Content != "" {
		content := n.Content
		// Remove extra spaces and junk
		content = strings.ReplaceAll(content, "\n\n\n", "\n\n")
		content = strings.TrimSpace(content)

		// Limit length for Telegram
		if len(content) > 600 {
			// Find last full sentence
			sentences := strings.Split(content[:600], ".")
			if len(sentences) > 1 {
				// Remove last incomplete sentence
				content = strings.Join(sentences[:len(sentences)-1], ".") + "."
			} else {
				content = content[:600] + "..."
			}
		}
		b.WriteString(fmt.Sprintf("📄 <b>Оригінал:</b>\n%s\n\n", content))
	}

	// Ukrainian translation of full content
	if n.ContentUK != "" && n.ContentUK != n.Content {
		contentUK := n.ContentUK
		// Remove extra spaces
		contentUK = strings.ReplaceAll(contentUK, "\n\n\n", "\n\n")
		contentUK = strings.TrimSpace(contentUK)

		// Limit length
		if len(contentUK) > 600 {
			sentences := strings.Split(contentUK[:600], ".")
			if len(sentences) > 1 {
				contentUK = strings.Join(sentences[:len(sentences)-1], ".") + "."
			} else {
				contentUK = contentUK[:600] + "..."
			}
		}
		b.WriteString(fmt.Sprintf("🇺🇦 <b>Українською:</b>\n%s\n\n", contentUK))
	}

	b.WriteString("➖➖➖➖➖➖➖➖➖➖\n\n")

	return b.String()
}

// Run запускает основной процесс приложения
func Run() {
	// Опционально можно добавить фильтрацию по категориям
	// Например: rss.FilterFeedsByCategories(feeds, []string{"ukraine", "visas", "technology"})

	feeds, err := rss.LoadFeeds("configs/feeds.yaml")
	if err != nil {
		log.Fatalf("Ошибка загрузки списка RSS: %v", err)
	}

	// Можно добавить фильтрацию по категориям если нужно
	// feeds = rss.FilterFeedsByCategories(feeds, []string{"ukraine", "denmark", "visas"})

	items, err := rss.FetchAllFeeds(feeds)
	if err != nil {
		log.Fatalf("Ошибка парсинга RSS: %v", err)
	}

	fmt.Printf("Собрано новостей: %d\n", len(items))

	filtered, err := news.FilterAndTranslate(items)
	if err != nil {
		log.Fatalf("Ошибка фильтрации/перевода: %v", err)
	}
	fmt.Printf("Релевантних новин: %d\n", len(filtered))

	// Показываем превью первых 2 новостей в консоли
	for i, n := range filtered {
		if i >= 2 {
			break
		}
		fmt.Println("---")
		fmt.Println(news.FormatNews(n))
	}

	if len(filtered) == 0 {
		log.Println("Нет релевантных новостей для отправки.")
		return
	}

	token := os.Getenv("TELEGRAM_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")

	if token == "" {
		log.Fatal("TELEGRAM_TOKEN не установлен. Установите переменную окружения.")
	}
	if chatID == "" {
		log.Fatal("TELEGRAM_CHAT_ID не установлен. Установите переменную окружения.")
	}

	// Проверяем режим работы - одна новость или несколько
	mode := os.Getenv("BOT_MODE")

	if mode == "single" {
		// Режим одной новости - отправляем только первую важную новость
		sendSingleNews(filtered, token, chatID)
	} else {
		// Режим нескольких новостей - отправляем 2-3 новости одним сообщением
		sendMultipleNews(filtered, token, chatID)
	}
}

// sendSingleNews отправляет одну новость
func sendSingleNews(newsList []news.News, token, chatID string) {
	if len(newsList) == 0 {
		log.Println("Нет новостей для отправки.")
		return
	}

	// Берем первую (самую важную) новость
	selectedNews := newsList[0]

	// Формируем сообщение для одной новости
	msg := formatSingleNewsMessage(selectedNews, 1)

	log.Printf("Отправляем одну новость длиной %d символов", len(msg))

	err := telegram.SendMessage(token, chatID, msg)
	if err != nil {
		log.Fatalf("Ошибка отправки в Telegram: %v", err)
	}

	log.Printf("Новость успешно отправлена: %s", selectedNews.Title)
}

// sendMultipleNews отправляет несколько новостей одним сообщением
func sendMultipleNews(newsList []news.News, token, chatID string) {
	// Формируем сообщение (берем топ-2 новости для лучшей читаемости)
	msg := formatNewsMessage(newsList, 2)

	// Проверяем длину сообщения (Telegram лимит ~4096 символов)
	if len(msg) > 4000 {
		log.Printf("Сообщение слишком длинное (%d символов), берем только 1 новость", len(msg))
		msg = formatNewsMessage(newsList, 1)
	}

	log.Printf("Отправляем сообщение длиной %d символов", len(msg))

	err := telegram.SendMessage(token, chatID, msg)
	if err != nil {
		log.Fatalf("Ошибка отправки в Telegram: %v", err)
	}

	log.Println("Новости успешно отправлены в Telegram!")
}

// formatSingleNewsMessage форматирует красивое сообщение для одной новости
func formatSingleNewsMessage(n news.News, number int) string {
	var b strings.Builder

	// Красивый заголовок
	b.WriteString("🇩🇰 <b>Danish News</b> 🇺🇦\n")
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	// Определяем категорию и эмодзи
	emoji := "📰"
	categoryText := "🇩🇰 <b>НОВИНИ ДАНІЇ</b>"

	if n.Category == "ukraine" {
		emoji = "🔥"
		categoryText = "🇺🇦 <b>УКРАЇНА В ДАНІЇ</b>"
	}

	b.WriteString(categoryText + "\n\n")

	// Заголовок новости с ссылкой
	b.WriteString(fmt.Sprintf("%s <a href=\"%s\">%s</a>\n\n", emoji, n.Link, n.Title))

	// Украинский перевод заголовка (только если качественный)
	if n.TitleUK != "" && n.TitleUK != n.Title && len(n.TitleUK) > 15 && !strings.Contains(strings.ToLower(n.TitleUK), "д -р") {
		b.WriteString(fmt.Sprintf("🇺🇦 <i>%s</i>\n\n", n.TitleUK))
	}

	// Показываем оригинальный текст (только первые 2-3 предложения)
	if n.Content != "" {
		content := cleanAndLimitContent(n.Content, true)
		if len(content) > 80 {
			b.WriteString(fmt.Sprintf("📄 <b>Оригінал:</b>\n%s\n\n", content))
		}
	}

	// Украинский перевод (улучшенный)
	if n.ContentUK != "" && n.ContentUK != n.Content && len(n.ContentUK) > 80 {
		contentUK := cleanAndLimitContent(n.ContentUK, false)
		// Проверяем качество перевода
		if !isLowQualityTranslation(contentUK) {
			b.WriteString(fmt.Sprintf("🇺🇦 <b>Українською:</b>\n%s\n\n", contentUK))
		}
	}

	// Красивый футер
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	b.WriteString("📱 <i>Danish News Bot</i>")

	return b.String()
}

// cleanAndLimitContent очищает и ограничивает контент для красивого отображения
func cleanAndLimitContent(content string, isOriginal bool) string {
	// Убираем HTML-теги и лишние пробелы
	content = strings.ReplaceAll(content, "<", "&lt;")
	content = strings.ReplaceAll(content, ">", "&gt;")
	content = strings.TrimSpace(content)

	// Разделяем на предложения
	sentences := strings.Split(content, ".")
	var cleanSentences []string

	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)

		// Пропускаем очень короткие и пустые предложения
		if len(sentence) < 15 {
			continue
		}

		// Для оригинала - фильтруем нерелевантные предложения
		if isOriginal && isIrrelevantSentence(sentence) {
			continue
		}

		cleanSentences = append(cleanSentences, sentence)

		// Ограничиваем до 3 предложений для краткости
		if len(cleanSentences) >= 3 {
			break
		}
	}

	result := strings.Join(cleanSentences, ". ")
	if result != "" && !strings.HasSuffix(result, ".") {
		result += "."
	}

	// Финальная проверка длины
	if len(result) > 500 {
		result = result[:500] + "..."
	}

	return result
}

// isIrrelevantSentence проверяет, относится ли предложение к основной теме статьи
func isIrrelevantSentence(sentence string) bool {
	lowerSentence := strings.ToLower(sentence)

	// Фразы, указывающие на другие статьи или нерелевантный контент
	irrelevantPhrases := []string{
		"den russiske præsident", "vladimir putin", "kim jong-un",
		"nordkoreas leder", "kinas hovedstad", "beijing",
		"militærparade", "anden verdenskrig", "jeffrey epstein",
		"amerikanske kongres", "føderale efterforskning",
		"sexforbryder", "dokumenter fra",
		"læs også", "se også", "følg med på",
		"dr nyheder har", "indtil videre ikke",
	}

	for _, phrase := range irrelevantPhrases {
		if strings.Contains(lowerSentence, phrase) {
			return true
		}
	}

	return false
}

// isLowQualityTranslation проверяет качество перевода
func isLowQualityTranslation(translation string) bool {
	lowerTranslation := strings.ToLower(translation)

	// Признаки плохого перевода
	badTranslationSigns := []string{
		"д -р", "д-р новини", "житловому житлі",
		"дорученнями слід дотримуватися",
		"влаштували поліцію", "не влаштували",
	}

	for _, sign := range badTranslationSigns {
		if strings.Contains(lowerTranslation, sign) {
			return true
		}
	}

	// Если слишком много повторяющихся слов
	words := strings.Fields(translation)
	if len(words) > 10 {
		wordCount := make(map[string]int)
		for _, word := range words {
			if len(word) > 3 {
				wordCount[strings.ToLower(word)]++
			}
		}

		// Если какое-то слово повторяется больше 3 раз - подозрительно
		for _, count := range wordCount {
			if count > 3 {
				return true
			}
		}
	}

	return false
}
