package news

import (
	"dknews/internal/rss"
	"dknews/internal/scraper"
	"dknews/internal/translate"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
)

// News is news struct with Ukrainian translation
type News struct {
	Title            string
	TitleUK          string // Only Ukrainian translation
	Content          string // Full article content
	ContentUK        string // Full content in Ukrainian
	Link             string
	Published        time.Time
	Category         string   // Source category (ukraine, denmark, visas, etc.)
	Score            int      // News importance score
	SourceName       string   // Name of the source
	SourceLang       string   // Original language of the source
	SourceCategories []string // All categories from source
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

	// визовые и беженские темы — базовые ключевые слова включены, дополнительные б��сты ниже
	"refugee", "беженцы", "біженці", "asylum", "убежище", "притулок",
	"residence permit", "вид на жительство", "посвідка на проживання",

	// Добавляем более общие ключевые слова для тестирования
	"nyheder", "news", "новости", "новини",
	"verden", "world", "світ", "мир",
	"samfund", "society", "суспільство", "общество",

	// Еще более общие слова для захвата большего колва новостей
	"danske", "danish", "viborg", "датське",
	"københav", "copenhagen", "копенгаген", "копенгага",
	"aarhus", "odense", "aalborg",
	"region", "kommune", "borgere", "citizens",
	"beslutning", "decision", "рішення", "решение",
	"lov", "law", "закон", "право",
	"nye", "new", "новий", "новый",
	"stor", "large", "великий", "большой",
}

// Extra boost keywords for refugee/visa related stories to increase priority
var refugeeBoostKeywords = []string{
	"refugee", "viborg",
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
func calculateNewsScore(item *rss.FeedItem) (string, int) {
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

	// Important Denmark news - ОЧЕНЬ мягкие критерии для тест
	if containsAny(text, denmarkKeywords) {
		score := 30
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

	// ВРЕМЕННО: Захватываем ВСЕ новости для тестирования системы перевода
	// Если в заголовке или описании есть хоть какие-то слова - пропускаем
	if len(strings.Fields(text)) > 3 {
		return "general", 20 // Минимальный балл для общих новостей
	}

	return "", 0
}

// FilterAndTranslate фильтрует, извлекает полный контент и переводит новости
func FilterAndTranslate(items []*rss.FeedItem) ([]News, error) {
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

		// Вычисляем категорию и важност��
		category, score := calculateNewsScore(item)
		if score == 0 {
			continue // Пропускаем неважные новости
		}

		publishedTime := time.Now()
		if item.PublishedParsed != nil {
			publishedTime = *item.PublishedParsed
		}

		candidates = append(candidates, News{
			Title:            item.Title,
			Content:          item.Description, // Пока краткое описание, полный контент добавим после
			Link:             item.Link,
			Published:        publishedTime,
			Category:         category,
			Score:            score,
			SourceName:       item.Source.Name,
			SourceLang:       item.Source.Lang,
			SourceCategories: item.Source.Categories,
		})

		log.Printf("Добавлена новость [%s, score: %d, source: %s]: %s", category, score, item.Source.Name, item.Title)
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

		log.Printf("Переводим новость %d/%d на украин��кий: %s", i+1, maxNews, news.Title)

		// Определяем исходный язык для перевода
		sourceLang := "da" // По умолчанию датский
		if news.SourceLang != "" {
			sourceLang = news.SourceLang
		}

		// Оптимизация: один запрос на перевод для заголовка + контента
		separator := "\n\n---SPLIT---\n\n"
		combined := news.Title + separator + news.Content
		translatedCombined, err := translate.TranslateText(combined, sourceLang, "uk")
		if err == nil {
			parts := strings.SplitN(translatedCombined, "---SPLIT---", 2)
			if len(parts) == 2 {
				news.TitleUK = strings.TrimSpace(parts[0])
				news.ContentUK = strings.TrimSpace(parts[1])
			} else {
				// На случай, если разделитель удалился/изменилс��
				news.TitleUK, _ = translate.TranslateText(news.Title, sourceLang, "uk")
				news.ContentUK, _ = translate.TranslateText(news.Content, sourceLang, "uk")
			}
		} else {
			// Фоллбек к прежней логике
			news.TitleUK, _ = translate.TranslateText(news.Title, sourceLang, "uk")
			news.ContentUK, _ = translate.TranslateText(news.Content, sourceLang, "uk")
		}

		result = append(result, news)
	}

	log.Printf("Обработано %d новостей с полным контентом и украинскими переводами", len(result))
	return result, nil
}
