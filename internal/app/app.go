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
		log.Fatalf("Ошибка фильтрации/перевода: %v", err)
	}
	fmt.Printf("Релевантных новостей: %d\n", len(filtered))

	// Показываем превью первых 2 новостей в консоли
	for i, n := range filtered {
		if i >= 2 {
			break
		}
		fmt.Println("---")
		fmt.Printf("[%s, score: %d] %s\n", n.Category, n.Score, n.Title)
		if n.TitleUK != "" {
			fmt.Printf("UK: %s\n", n.TitleUK)
		}
		fmt.Printf("Контент: %d символов\n", len(n.Content))
		fmt.Printf("%s\n", n.Link)
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

// formatSingleNewsMessage форматирует сообщение для одной новости
func formatSingleNewsMessage(n news.News, number int) string {
	var b strings.Builder

	// Заголовок сообщения
	b.WriteString("🇩🇰 <b>Новини Данії</b> 🇺🇦\n")
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	// Определяем эмодзи и категорию
	emoji := "📰"
	categoryText := "🇩🇰 <b>ВАЖЛИВІ НОВИНИ ДАНІЇ</b>\n\n"

	if n.Category == "ukraine" {
		emoji = "🔥"
		categoryText = "🇺🇦 <b>УКРАЇНА В ДАНІЇ</b>\n\n"
	}

	b.WriteString(categoryText)

	// Заголовок с ссылкой
	b.WriteString(fmt.Sprintf("%s <a href=\"%s\">%s</a>\n", emoji, n.Link, n.Title))

	// Украинский перевод заголовка
	if n.TitleUK != "" && n.TitleUK != n.Title {
		b.WriteString(fmt.Sprintf("🇺🇦 <i>%s</i>\n", n.TitleUK))
	}

	b.WriteString("\n")

	// Показываем полный контент оригинала (больше места для одной новости)
	if n.Content != "" {
		content := n.Content
		content = strings.ReplaceAll(content, "\n\n\n", "\n\n")
		content = strings.TrimSpace(content)

		// Для одной новости можем позволить больше текста
		if len(content) > 1200 {
			sentences := strings.Split(content[:1200], ".")
			if len(sentences) > 1 {
				content = strings.Join(sentences[:len(sentences)-1], ".") + "."
			} else {
				content = content[:1200] + "..."
			}
		}
		b.WriteString(fmt.Sprintf("📄 <b>Оригінал:</b>\n%s\n\n", content))
	}

	// Украинский перевод полного контента
	if n.ContentUK != "" && n.ContentUK != n.Content {
		contentUK := n.ContentUK
		contentUK = strings.ReplaceAll(contentUK, "\n\n\n", "\n\n")
		contentUK = strings.TrimSpace(contentUK)

		if len(contentUK) > 1200 {
			sentences := strings.Split(contentUK[:1200], ".")
			if len(sentences) > 1 {
				contentUK = strings.Join(sentences[:len(sentences)-1], ".") + "."
			} else {
				contentUK = contentUK[:1200] + "..."
			}
		}
		b.WriteString(fmt.Sprintf("🇺🇦 <b>Українською:</b>\n%s\n\n", contentUK))
	}

	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	b.WriteString("📱 Danish News Bot | Щодня кілька разів")

	return b.String()
}
