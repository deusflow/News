package app

import (
	"dknews/internal/gemini"
	"dknews/internal/news"
	"dknews/internal/rss"
	"dknews/internal/telegram"
	"fmt"
	"log"
	"os"
	"strings"
)

// formatNewsMessage builds grouped message using AI summaries (Ukrainian priority, then Danish)
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

	count := 1

	// First Ukraine news (priority)
	if len(ukraineNews) > 0 {
		b.WriteString("🇺🇦 <b>УКРАЇНА В ДАНІЇ</b>\n\n")
		for _, n := range ukraineNews {
			if count > max {
				break
			}
			b.WriteString(formatSingleNews(n, count))
			count++
		}
	}

	// Then important Denmark news
	if len(denmarkNews) > 0 {
		if len(ukraineNews) > 0 {
			b.WriteString("\n🇩🇰 <b>ВАЖЛИВІ НОВИНИ ДАНІЇ</b>\n\n")
		}
		for _, n := range denmarkNews {
			if count > max {
				break
			}
			b.WriteString(formatSingleNews(n, count))
			count++
		}
	}

	b.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	b.WriteString("📱 Danish News Bot | Щодня о 8:00 UTC")

	return b.String()
}

// formatSingleNews now uses AI summaries instead of full translations
func formatSingleNews(n news.News, number int) string {
	var b strings.Builder

	// Set emoji by category
	emoji := "📰"
	if n.Category == "ukraine" {
		emoji = "🔥"
	}

	// Title with link
	b.WriteString(fmt.Sprintf("%s <b>%d.</b> <a href=\"%s\">%s</a>\n", emoji, number, n.Link, n.Title))

	// Ukrainian summary (primary)
	if n.SummaryUkrainian != "" {
		b.WriteString(fmt.Sprintf("🇺🇦 <i>%s</i>\n", limitText(n.SummaryUkrainian, 280)))
	}

	// Danish summary (secondary)
	if n.SummaryDanish != "" {
		b.WriteString(fmt.Sprintf("🇩🇰 %s\n", limitText(n.SummaryDanish, 220)))
	}

	// Optional original snippet
	if n.Content != "" {
		snippet := cleanAndLimitContent(n.Content, true)
		if snippet != "" {
			b.WriteString("📄 <b>Оригінал:</b> " + limitText(snippet, 300) + "\n")
		}
	}

	b.WriteString("➖➖➖➖➖➖➖➖➖➖\n\n")

	return b.String()
}

func limitText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndex(cut, " "); i > 40 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "..."
}

// Run запускает основной процесс приложения с инициализацией Gemini
func Run() {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("GEMINI_API_KEY не установлен")
	}
	gmClient, err := gemini.NewClient(apiKey)
	if err != nil {
		log.Fatalf("Ошибка инициализации Gemini: %v", err)
	}
	defer gmClient.Close()
	news.SetGeminiClient(gmClient)

	feeds, err := rss.LoadFeeds("configs/feeds.yaml")
	if err != nil {
		log.Fatalf("Ошибка загрузки списка RSS: %v", err)
	}
	items, err := rss.FetchAllFeeds(feeds)
	if err != nil {
		log.Fatalf("Ошибка парсинга RSS: %v", err)
	}
	fmt.Printf("Собрано новостей: %d\n", len(items))

	filtered, err := news.FilterAndTranslate(items)
	if err != nil {
		log.Fatalf("Ошибка фильтрации/обработки: %v", err)
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
		log.Fatal("TELEGRAM_TOKEN не установлен")
	}
	if chatID == "" {
		log.Fatal("TELEGRAM_CHAT_ID не установлен")
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
		log.Printf("Сообщение %d символов, сокращаем до 1 новости", len(msg))
		msg = formatNewsMessage(newsList, 1)
	}

	log.Printf("Отправляем сообщение длиной %d символов", len(msg))

	err := telegram.SendMessage(token, chatID, msg)
	if err != nil {
		log.Fatalf("Ошибка отправки в Telegram: %v", err)
	}

	log.Println("Новости успешно отправлены в Telegram!")
}

// formatSingleNewsMessage адаптирован для саммари
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

	if n.SummaryUkrainian != "" {
		b.WriteString("🇺🇦 <i>" + limitText(n.SummaryUkrainian, 380) + "</i>\n\n")
	}
	if n.SummaryDanish != "" {
		b.WriteString("🇩🇰 " + limitText(n.SummaryDanish, 320) + "\n\n")
	}

	// Показываем оригинальный текст (только первые 2-3 предложения)
	if n.Content != "" {
		orig := cleanAndLimitContent(n.Content, true)
		if len(orig) > 80 {
			b.WriteString("📄 <b>Оригінал:</b> " + limitText(orig, 500) + "\n\n")
		}
	}

	// Красивый футер
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	b.WriteString("📱 <i>Danish News Bot</i>")

	return b.String()
}

// cleanAndLimitContent kept for original snippet extraction
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

// isIrrelevantSentence reused for filtering original content noise
func isIrrelevantSentence(sentence string) bool {
	lower := strings.ToLower(sentence)

	// Фразы, указывающие на другие статьи или нерелевантный контент
	irrelevant := []string{"læs også", "se også", "følg med på", "dr nyheder har"}
	for _, ph := range irrelevant {
		if strings.Contains(lower, ph) {
			return true
		}
	}

	return false
}
