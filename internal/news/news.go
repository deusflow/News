package news

import (
	"dknews/internal/scraper"
	"dknews/internal/translate"
	"github.com/mmcdole/gofeed"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
)

// News is news struct with Ukrainian translation
type News struct {
	Title     string
	TitleUK   string // Only Ukrainian translation
	Content   string // Full article content
	ContentUK string // Full content in Ukrainian
	Link      string
	Published time.Time
	Category  string // "ukraine" or "denmark"
	Score     int    // News importance score
}

// Keywords for Ukraine news (high priority)
var ukraineKeywords = []string{
	"ukraine", "ukraina", "ukrainer", "українці", "украин",
	"hjælp ukraine", "help ukraine", "допомога україна",
	"flygtning", "refugee", "біженці", "біженець",
	"krig", "war", "støtte ukraine", "support ukraine", "підтримк україни",
	"våben ukraine", "weapon ukraine", "зброя україни",
	"missiler ukraine", "missiles ukraine", "ракети україні", "sundhed", "health", "здоров'я",
	"flygtninge krise", "refugee crisis", "криза біженців",
	"nato", "нато",
	"sanction", "санкці",
}

// Keywords for important Denmark news
var denmarkKeywords = []string{
	"danmark", "danish", "данія", "данська",
	"regering", "government", "правительств", "уряд",
	"politik", "politics", "политик", "політика",
	"økonomi", "economy", "экономик", "економіка",
	"minister", "министр", "міністр",
	"valg", "election", "выборы", "вибори",
	"eu", "europe", "европа", "європа",
	"samråd", "consultation", "консультація", "консультації",
	"corona", "covid", "visa", "візи",

	// визовые и беженские темы — базовые ключевые слова включены, дополнительные бусты ниже
	"refugee", "беженцы", "біженці", "asylum", "убежище", "притулок",
	"residence permit", "вид на жительство", "посвідка на проживання",
}

// Extra boost keywords for refugee/visa related stories to increase priority
var refugeeBoostKeywords = []string{
	"refugee", "бежен", "біжен",
	"flygtning", "refugee visa", "temporary protection", "тимчасовий захист",
}

var visaBoostKeywords = []string{
	"visa", "visa extension", "продление визы", "продовження візи",
	"residence permit", "вид на жительство", "залишитися в єс", "stay in eu",
}

// Words to exclude (not important topics)
var excludeKeywords = []string{
	"vejr", "weather", "погода",
	"musik", "music", "музыка", "музик",
	"film", "movie", "фільм", "кіно",
	"celebrity", "знаменит",
	"fodbold result", "football result", "результат футбол",
	"sport result", "спортив результат", "результат спорт",
	"tv program", "телепрогр", "телепрограма",
	"horoskop", "гороскоп",
	"madopskrift", "recipe", "рецепт",
}

// containsAny checks if string has any keyword (whole-word aware)
func containsAny(s string, keywords []string) bool {
	s = strings.ToLower(s)
	for _, kw := range keywords {
		pattern := `\\b` + regexp.QuoteMeta(strings.ToLower(kw)) + `\\b`
		matched, _ := regexp.MatchString(pattern, s)
		if matched {
			return true
		}
	}
	return false
}

// calculateNewsScore gets news importance
func calculateNewsScore(item *gofeed.Item) (string, int) {
	text := strings.ToLower(item.Title + " " + item.Description)

	// Exclude not important topics
	if containsAny(text, excludeKeywords) {
		return "", 0
	}

	// Ukraine news - highest priority
	if containsAny(text, ukraineKeywords) {
		score := 100
		// Extra points for important words
		if strings.Contains(text, "våben") || strings.Contains(text, "weapon") || strings.Contains(text, "missiler") {
			score += 20
		}
		if strings.Contains(text, "hjælp") || strings.Contains(text, "help") {
			score += 15
		}
		return "ukraine", score
	}

	// Important Denmark news
	if containsAny(text, denmarkKeywords) {
		score := 50
		// Extra points for politics and economy
		if containsAny(text, []string{"regering", "minister", "minister"}) {
			score += 20
		}
		if containsAny(text, []string{"politik", "økonomi"}) {
			score += 15
		}

		// Boost for refugee/visa related stories (they're important for the audience)
		if containsAny(text, refugeeBoostKeywords) {
			score += 20
		}
		if containsAny(text, visaBoostKeywords) {
			score += 15
		}

		return "denmark", score
	}

	return "", 0
}

// cleanHTMLTags удаляет HTML теги из текста и значительно улучшает форматирование
func cleanHTMLTags(text string) string {
	// Простая очистка HTML тегов
	text = strings.ReplaceAll(text, "<br>", " ")
	text = strings.ReplaceAll(text, "<br/>", " ")
	text = strings.ReplaceAll(text, "<p>", "\n\n")
	text = strings.ReplaceAll(text, "</p>", "")

	// Удаляем остальные HTML теги
	inTag := false
	var result strings.Builder
	for _, char := range text {
		if char == '<' {
			inTag = true
		} else if char == '>' {
			inTag = false
		} else if !inTag {
			result.WriteRune(char)
		}
	}

	cleaned := strings.TrimSpace(result.String())

	// Убираем все мусорные фразы из различных источников
	cleaned = removeJunkPhrases(cleaned)

	// Улучшаем форматирование абзацев
	cleaned = formatParagraphs(cleaned)

	return cleaned
}

// removeJunkPhrases удаляет мусорные фразы из разных новостных сайтов
func removeJunkPhrases(text string) string {
	// Мусорные фразы из Ekstra Bladet
	junkPhrases := []string{
		"På Ekstra Bladet lægger vi stor vægt på at have en tæt dialog med jer læsere. Jeres input er guld",
		"værd, og mange historier ville ikke kunne lade sig gøre uden jeres tip. Men selv om vi også har",
		"tradition for at turde, når andre tier, værner vi om en sober og konstruktiv tone.",
		"Ekstra Bladet og evt. politianmeldt.",
		"På Ekstra Bladet lægger vi stor vægt på at have en tæt dialog med jer læsere.",
		"Jeres input er guld værd, og mange historier ville ikke kunne lade sig gøre uden jeres tip.",
		"Men selv om vi også har tradition for at turde, når andre tier, værner vi om en sober og konstruktiv tone.",

		// Мусорные фразы из DR
		"DR Nyheder følger Danmarks Radio",
		"Følg med på dr.dk",
		"Læs også:",
		"Se også:",
		"Hør mere:",
		"Video:",

		// Общие мусорные фразы
		"Læs mere på",
		"Klik her for at",
		"Følg os på",
		"Del artiklen",
		"Print artiklen",
		"Send til en ven",
		"Gem artiklen",
		"Cookie",
		"GDPR",
		"Privatlivspolitik",
	}

	for _, phrase := range junkPhrases {
		text = strings.ReplaceAll(text, phrase, "")
	}

	return text
}

// formatParagraphs улучшает форматирование абзацев
func formatParagraphs(text string) string {
	// Разбиваем на строки
	lines := strings.Split(text, "\n")
	var cleanLines []string
	var currentParagraph strings.Builder

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Пропускаем пустые строки и очень короткие
		if len(line) < 5 {
			// Если есть накопленный абзац, сохраняем его
			if currentParagraph.Len() > 0 {
				paragraph := strings.TrimSpace(currentParagraph.String())
				if len(paragraph) > 20 {
					cleanLines = append(cleanLines, paragraph)
				}
				currentParagraph.Reset()
			}
			continue
		}

		// Проверяем, не является ли строка мусором
		if isJunkLine(line) {
			continue
		}

		// Если строка заканчивается точкой, восклицательным или вопросительным знаком
		// это конец предложения/абзаца
		if strings.HasSuffix(line, ".") || strings.HasSuffix(line, "!") || strings.HasSuffix(line, "?") {
			if currentParagraph.Len() > 0 {
				currentParagraph.WriteString(" ")
			}
			currentParagraph.WriteString(line)

			// Сохраняем абзац
			paragraph := strings.TrimSpace(currentParagraph.String())
			if len(paragraph) > 20 {
				cleanLines = append(cleanLines, paragraph)
			}
			currentParagraph.Reset()
		} else {
			// Продолжение предложения
			if currentParagraph.Len() > 0 {
				currentParagraph.WriteString(" ")
			}
			currentParagraph.WriteString(line)
		}
	}

	// Сохраняем последний абзац если есть
	if currentParagraph.Len() > 0 {
		paragraph := strings.TrimSpace(currentParagraph.String())
		if len(paragraph) > 20 {
			cleanLines = append(cleanLines, paragraph)
		}
	}

	// Соединяем абзацы через двойной перенос
	result := strings.Join(cleanLines, "\n\n")

	// Финальная очистка
	result = finalCleanup(result)

	return result
}

// isJunkLine проверяет, является ли строка мусором
func isJunkLine(line string) bool {
	lower := strings.ToLower(line)

	// Проверяем на наличие мусорных слов
	junkIndicators := []string{
		"cookie", "gdpr", "privatlivspolitik", "abonnement",
		"læs mere", "klik her", "følg os", "del artikel",
		"print", "gem artikel", "send til", "advertisement",
		"reklame", "annonce", "sponsor",
	}

	for _, indicator := range junkIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}

	// Проверяем на повторяющиеся символы (часто мусор)
	if len(line) > 10 {
		first := line[:1]
		if strings.Count(line, first) > len(line)/2 {
			return true
		}
	}

	return false
}

// finalCleanup выполняет финальную очистку текста
func finalCleanup(text string) string {
	// Убираем множественные пробелы
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}

	// Убираем множественные переносы строк
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}

	// Убираем пробелы в начале и конце
	text = strings.TrimSpace(text)

	// Ограничиваем общую длину, но сохраняем целые абзацы
	if len(text) > 5000 {
		paragraphs := strings.Split(text, "\n\n")
		var selectedParagraphs []string
		totalLength := 0

		for _, paragraph := range paragraphs {
			if totalLength+len(paragraph) < 4800 {
				selectedParagraphs = append(selectedParagraphs, paragraph)
				totalLength += len(paragraph) + 2 // +2 для \n\n
			} else {
				break
			}
		}

		if len(selectedParagraphs) > 0 {
			text = strings.Join(selectedParagraphs, "\n\n")
		}
	}

	return text
}

// FilterAndTranslate фильтрует, извлекает полный контент и переводит новости
func FilterAndTranslate(items []*gofeed.Item) ([]News, error) {
	seen := make(map[string]struct{})
	var candidates []News

	log.Printf("Начинаем фильтрацию из %d новостей", len(items))

	for _, item := range items {
		// Ограничиваем обработку только свежими новостями (последние 24 часа)
		if item.PublishedParsed != nil && time.Since(*item.PublishedParsed) > 24*time.Hour {
			continue
		}

		// Дедупликация по ссылке
		if _, ok := seen[item.Link]; ok {
			continue
		}
		seen[item.Link] = struct{}{}

		// Вычисляем категорию и важность
		category, score := calculateNewsScore(item)
		if score == 0 {
			continue // Пропускаем неважные новости
		}

		publishedTime := time.Now()
		if item.PublishedParsed != nil {
			publishedTime = *item.PublishedParsed
		}

		candidates = append(candidates, News{
			Title:     item.Title,
			Content:   item.Description, // Пока краткое описание, полный контент добавим после
			Link:      item.Link,
			Published: publishedTime,
			Category:  category,
			Score:     score,
		})

		log.Printf("Добавлена новость [%s, score: %d]: %s", category, score, item.Title)
	}

	// Сортируем по важности (score) и времени
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score // По убыванию важности
		}
		return candidates[i].Published.After(candidates[j].Published) // По времени (новые первыми)
	})

	// Увеличиваем количество обрабатываемых новостей для множественных запусков
	maxNews := 8 // Увеличиваем до 8 новостей для выбора
	if len(candidates) < maxNews {
		maxNews = len(candidates)
	}

	// Извлекаем полный контент статей
	urls := make([]string, maxNews)
	for i := 0; i < maxNews; i++ {
		urls[i] = candidates[i].Link
	}

	log.Printf("Извлекаем полный контент %d статей...", maxNews)
	fullArticles := scraper.ExtractArticlesInBackground(urls)

	result := make([]News, 0, maxNews)

	// Переводим отобранные новости
	for i := 0; i < maxNews; i++ {
		news := candidates[i]

		// Используем полный контент если удалось извлечь
		if fullArticle, exists := fullArticles[news.Link]; exists {
			news.Content = fullArticle.Content
			log.Printf("✅ Используем полный контент (%d символов) для: %s", len(news.Content), news.Title)
		} else {
			log.Printf("⚠️ Используем краткое описание для: %s", news.Title)
		}

		log.Printf("Переводим новость %d/%d на украинский: %s", i+1, maxNews, news.Title)

		// Оптимизация: один запрос на перевод для заголовка + контента
		separator := "\n\n---SPLIT---\n\n"
		combined := news.Title + separator + news.Content
		translatedCombined, err := translate.TranslateText(combined, "da", "uk")
		if err == nil {
			parts := strings.SplitN(translatedCombined, "---SPLIT---", 2)
			if len(parts) == 2 {
				news.TitleUK = strings.TrimSpace(parts[0])
				news.ContentUK = strings.TrimSpace(parts[1])
			} else {
				// На случай, если разделитель удалился/изменился
				news.TitleUK, _ = translate.TranslateText(news.Title, "da", "uk")
				news.ContentUK, _ = translate.TranslateText(news.Content, "da", "uk")
			}
		} else {
			// Фоллбек к прежней логике
			news.TitleUK, _ = translate.TranslateText(news.Title, "da", "uk")
			news.ContentUK, _ = translate.TranslateText(news.Content, "da", "uk")
		}

		result = append(result, news)
	}

	log.Printf("Обработано %d новостей с полным контентом и украинскими переводами", len(result))
	return result, nil
}
