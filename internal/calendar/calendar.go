package calendar

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/storage"
	"github.com/deusflow/News/internal/telegram"
	_ "github.com/lib/pq"
)

// CalendarNewsItem represents an item used for generating the monthly calendar.
type CalendarNewsItem struct {
	Title    string
	Content  string
	Category string
	URL      string
}

// UkrainianMonthNames maps time.Month to nominative and genitive Ukrainian month names.
var ukrainianMonths = map[time.Month]struct {
	Nominative string // Наприклад: "Жовтень"
	Genitive   string // Наприклад: "жовтень" -> "жовтня"
}{
	time.January:   {Nominative: "Січень", Genitive: "січня"},
	time.February:  {Nominative: "Лютий", Genitive: "лютого"},
	time.March:     {Nominative: "Березень", Genitive: "березня"},
	time.April:     {Nominative: "Квітень", Genitive: "квітня"},
	time.May:       {Nominative: "Травень", Genitive: "травня"},
	time.June:      {Nominative: "Червень", Genitive: "червня"},
	time.July:      {Nominative: "Липень", Genitive: "липня"},
	time.August:    {Nominative: "Серпень", Genitive: "серпня"},
	time.September: {Nominative: "Вересень", Genitive: "вересня"},
	time.October:   {Nominative: "Жовтень", Genitive: "жовтня"},
	time.November:  {Nominative: "Листопад", Genitive: "листопада"},
	time.December:  {Nominative: "Грудень", Genitive: "грудня"},
}

// GetTargetMonth determines the upcoming month for the calendar based on current date.
// If run on or after the 20th of the month, target is next month; otherwise current month.
func GetTargetMonth(now time.Time) (time.Month, int, string, string) {
	target := now
	if now.Day() >= 20 {
		target = now.AddDate(0, 1, 0)
	}
	m := target.Month()
	y := target.Year()
	info := ukrainianMonths[m]
	return m, y, info.Nominative, info.Genitive
}

// GetDanishStandardEvents returns standard Danish recurring deadlines and dates for a specific month.
func GetDanishStandardEvents(month time.Month, year int) string {
	var events []string

	// 1. Boligstøtte (housing support)
	events = append(events, "1-й робочий день місяця: Виплата житлової субсидії (Boligstøtte від Udbetaling Danmark).")

	// 2. Børnepenge (Børne- og ungeydelse) — 20th of Jan, Apr, Jul, Oct
	switch month {
	case time.January:
		events = append(events, "20 січня: Виплата дитячих грошей (Børne- og ungeydelse / Børnepenge) за 1-й квартал (якщо вихідний — у попередню п'ятницю).")
	case time.April:
		events = append(events, "20 квітня: Виплата дитячих грошей (Børne- og ungeydelse / Børnepenge) за 2-й квартал.")
	case time.July:
		events = append(events, "20 липня: Виплата дитячих грошей (Børne- og ungeydelse / Børnepenge) за 3-й квартал.")
	case time.October:
		events = append(events, "20 жовтня: Виплата дитячих грошей (Børne- og ungeydelse / Børnepenge) за 4-й квартал.")
	}

	// 3. Tax milestones (Skat)
	switch month {
	case time.March:
		events = append(events, "Березень: Відкриття річного податкового звіту Skat (Årsopgørelse) — перевірка переплат або доплат податків за минулий рік.")
	case time.May:
		events = append(events, "1 травня: Дедлайн для перевірки та внесення правок до річного звіту Skat (Årsopgørelse) для більшості громадян.")
	case time.July:
		events = append(events, "1 липня: Розширений дедлайн подачі податкової декларації (для підприємців та осіб із закордонними доходами).")
	case time.November:
		events = append(events, "Листопад: Відкриття попереднього податкового розрахунку Skat на наступний рік (Forskudsopgørelse).")
	}

	// 4. Public Holidays & Daylight Saving
	switch month {
	case time.March:
		events = append(events, "Остання неділя березня: Перехід на літній час (годинник переводиться на 1 годину вперед).")
	case time.April:
		events = append(events, "Квітень (або кінець березня): Великодні свята в Данії (Påske) — Skærtorsdag, Langfredag, 2. Påskedag (державні вихідні, магазини зачинені).")
	case time.May:
		events = append(events, "Травень: Вознесіння (Kristi Himmelfartsdag) — державний вихідний у Данії.")
	case time.June:
		events = append(events, "5 червня: День Конституції Данії (Grundlovsdag) — багато служб та банків зачиняються раніше або вихідний; Трійця (2. Pinsedag).")
	case time.October:
		events = append(events, "Тиждень 42 (середина жовтня): Осінні шкільні канікули в Данії (Efterårsferie); остання неділя жовтня — перехід на зимовий час (годинник на 1 годину назад).")
	case time.December:
		events = append(events, "24–26 грудня: Різдво в Данії (Juleaften, 1. og 2. Juledag) — офіційні вихідні, транспорт за особливим розкладом; 31 грудня — Новий рік.")
	}

	// 5. Monthly payout of SU, Dagpenge and Wages
	events = append(events, "Останній банківський день місяця: Виплата студентської стипендії (SU), допомоги по безробіттю (Dagpenge від A-kasse), пенсій та стандартних місячних зарплат (Løn).")

	return strings.Join(events, "\n")
}

func hasTextContent(s string) bool {
	re := regexp.MustCompile(`<[^>]*>`)
	stripped := re.ReplaceAllString(s, "")
	stripped = strings.ReplaceAll(stripped, "&nbsp;", "")
	stripped = strings.ReplaceAll(stripped, "&#8203;", "")
	return strings.TrimSpace(stripped) != ""
}

func truncateString(s string, max int) string {
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max]) + "..."
	}
	return s
}

// RunCalendar generates and publishes the monthly Danish calendar of dates and payouts.
func RunCalendar(ctx context.Context, cfg *config.Config, aiManager *ai.Manager, supabase *storage.SupabaseClient) error {
	logger.Info("Starting Monthly Calendar generation")

	now := time.Now()
	targetMonth, targetYear, nomMonth, genMonth := GetTargetMonth(now)
	logger.Info("Target month for calendar determined", "month", nomMonth, "year", targetYear)

	// 1. Сбор новостей за последние 30-35 дней
	newsItems, err := fetchNewsForCalendar(ctx, cfg, supabase)
	if err != nil {
		logger.Warn("Failed to fetch past news for calendar, will proceed with standard events only", "error", err)
	}

	// 2. Формируем контекст стандартных событий и новостей
	standardEvents := GetDanishStandardEvents(targetMonth, targetYear)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== СТАНДАРТНІ ДАТИ ТА ВИПЛАТИ ДАНІЇ НА %s %d ===\n", strings.ToUpper(nomMonth), targetYear))
	sb.WriteString(standardEvents)
	sb.WriteString("\n\n")

	if len(newsItems) > 0 {
		sb.WriteString("=== НОВИНИ, ЗАКОНИ ТА ЗМІНИ В ДАНІЇ ЗА ОСТАННІЙ МІСЯЦЬ ===\n")
		for i, n := range newsItems {
			sb.WriteString(fmt.Sprintf("%d. %s\nКатегорія: %s\nЗміст: %s\nURL: %s\n\n",
				i+1, n.Title, n.Category, truncateString(n.Content, 400), n.URL))
		}
	}

	content := sb.String()

	// 3. Промпт для AI
	systemPrompt := fmt.Sprintf(`Ти - провідний експерт та редактор новинного Telegram-каналу для українців у Данії.
Твоя задача: скласти практичний, точний та стильний "КАЛЕНДАР ДАТ І ВИПЛАТ" на наступний місяць: %s %d року.

У вхідних даних тобі надано:
1. Офіційні регулярні дати Данії (виплати Børnepenge, SU, житлової субсидії Boligstøtte, зарплати, податкові дедлайни Skat, свята).
2. Новини та законодавчі зміни, зафіксовані в Данії за останній місяць.

Твоя мета: вибрати найважливіші для українців у Данії дати, грошові виплати, дедлайни та зміни правил, що діятимуть або вступають у силу в цьому місяці (%s).

Вимоги до структури та дизайну поста:
1. Заголовок (великими літерами, помітний, з емодзі):
   📅 <b>КАЛЕНДАР НА %s %d: Що зміниться, дати та виплати</b>

2. Короткий лід-абзац (1-2 речення):
   <i>Стислий огляд того, до яких змін та фінансових дат варто підготуватися українцям у Данії цього місяця.</i>

3. Чіткі тематичні блоки (оформлюй пункти через булети •, дати виділяй жирним <b>...</b>):
   💰 <b>Виплати та фінансова допомога:</b>
   • <b>[Дата]:</b> назва виплати (дитячі гроші Børnepenge, якщо є в цьому місяці; SU / Dagpenge / зарплата в останній банківський день; Boligstøtte на початку місяця тощо).

   📋 <b>Закони, правила та ВНЖ:</b>
   • <b>З [дати] або протягом місяця:</b> конкретні зміни в правилах перебування, роботі, мовних курсах, тарифах чи соціалці, взяті з наданих новин (якщо таких змін у новинах немає, напиши про загальні актуальні правила або опусти блок).

   ⏰ <b>Важливі дедлайни та дати:</b>
   • <b>[Дата]:</b> важливі дедлайни Skat, шкільні канікули, державні вихідні дні (Helligdage), переведення годинника тощо.

4. Заклик у кінці поста:
   ---
   📌 <i>Збережіть собі та поділіться з друзями в Данії, щоб не пропустити важливі терміни!</i>

Правила безпеки та оформлення:
- НЕ ВИГАДУЙ неіснуючих дат чи законів. Спирайся виключно на надані факти та реальний календар Данії.
- Пиши живою, грамотною українською мовою.
- Тільки валідні HTML-теги: <b>, <i>, <a>. Усі теги мають бути коректно закриті.
- Загальний обсяг: орієнтовно 1200-2200 символів (ідеально для читання в Telegram).

УВАГА: Ти повинен повернути ВАЛІДНИЙ JSON! Весь готовий текст помісти у поле "summary". Інші поля залиш порожніми.`,
		nomMonth, targetYear, genMonth, strings.ToUpper(genMonth), targetYear)

	userPrompt := fmt.Sprintf("Склади щомісячний календар на %s %d року на основі цих даних:\n\n%s",
		nomMonth, targetYear, content)

	// 4. Генерация через AI
	resp, err := aiManager.Generate(ctx, "Monthly Calendar", content, systemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("failed to generate calendar via AI: %w", err)
	}

	calendarText := strings.TrimSpace(resp.Summary)
	if !hasTextContent(calendarText) {
		calendarText = strings.TrimSpace(resp.Ukrainian)
	}
	if !hasTextContent(calendarText) {
		calendarText = strings.TrimSpace(resp.Danish)
	}

	if !hasTextContent(calendarText) {
		return fmt.Errorf("AI returned empty calendar content")
	}

	logger.Info("Monthly calendar generated successfully", "length", len(calendarText))

	// 5. Публикация в Telegram
	_, err = telegram.SendMessageAllowPreview(cfg.Telegram.Token, cfg.Telegram.ChatID, calendarText)
	if err != nil {
		return fmt.Errorf("failed to send monthly calendar to telegram: %w", err)
	}

	logger.Info("Monthly Calendar published to Telegram successfully")
	return nil
}

func fetchNewsForCalendar(ctx context.Context, cfg *config.Config, supabase *storage.SupabaseClient) ([]CalendarNewsItem, error) {
	var items []CalendarNewsItem

	// 1. Попытка получить из PostgreSQL (за последние 35 дней)
	if cfg.Database.UsePostgres && cfg.Database.URL != "" {
		logger.Info("Attempting to fetch news for calendar from PostgreSQL")
		db, err := sql.Open("postgres", cfg.Database.URL)
		if err == nil {
			defer db.Close()
			query := `
				SELECT s.title, COALESCE(s.title_ukrainian, ''), s.link, COALESCE(s.category, ''), COALESCE(t.ukrainian_translation, t.summary, '') 
				FROM sent_news s 
				LEFT JOIN translation_cache t ON s.content_hash = t.content_hash 
				WHERE s.sent_at >= NOW() - INTERVAL '35 days' 
				ORDER BY s.sent_at DESC 
				LIMIT 40
			`
			rows, queryErr := db.QueryContext(ctx, query)
			if queryErr == nil {
				defer rows.Close()
				for rows.Next() {
					var title, titleUA, link, category, content string
					if scanErr := rows.Scan(&title, &titleUA, &link, &category, &content); scanErr == nil {
						displayTitle := title
						if titleUA != "" {
							displayTitle = titleUA
						}
						if strings.TrimSpace(content) == "" {
							content = displayTitle
						}
						items = append(items, CalendarNewsItem{
							Title:    displayTitle,
							Content:  content,
							Category: category,
							URL:      link,
						})
					}
				}
				if len(items) > 0 {
					logger.Info("Successfully fetched news from PostgreSQL for calendar", "count", len(items))
					return items, nil
				}
			} else {
				logger.Warn("PostgreSQL query failed for calendar", "error", queryErr)
			}
		} else {
			logger.Warn("Failed to open PostgreSQL connection for calendar", "error", err)
		}
	}

	// 2. Попытка получить из Supabase
	if supabase != nil {
		logger.Info("Attempting to fetch news for calendar from Supabase")
		activeNews, err := supabase.GetActiveNews(ctx, 100)
		if err == nil && len(activeNews) > 0 {
			thirtyFiveDaysAgo := time.Now().Add(-35 * 24 * time.Hour)
			for _, n := range activeNews {
				if n.PublishedAt.After(thirtyFiveDaysAgo) {
					items = append(items, CalendarNewsItem{
						Title:    n.TitleUkrainian,
						Content:  n.TLDR,
						Category: n.Category,
						URL:      n.SourceURL,
					})
				}
			}
			if len(items) > 0 {
				logger.Info("Successfully fetched news from Supabase for calendar", "count", len(items))
				return items, nil
			}
		}
		if err != nil {
			logger.Warn("Failed to fetch news from Supabase for calendar", "error", err)
		}
	}

	// 3. Fallback: свежие новости из RSS
	if cfg.RSS.FeedsConfigPath != "" {
		feeds, err := rss.LoadFeeds(cfg.RSS.FeedsConfigPath)
		if err == nil && len(feeds) > 0 {
			feedItems, fetchErr := rss.FetchAllFeeds(ctx, feeds)
			if fetchErr == nil && len(feedItems) > 0 {
				limit := 20
				if len(feedItems) < limit {
					limit = len(feedItems)
				}
				for i := 0; i < limit; i++ {
					item := feedItems[i]
					items = append(items, CalendarNewsItem{
						Title:    item.Title,
						Content:  item.Description,
						Category: "society",
						URL:      item.Link,
					})
				}
				logger.Info("Successfully fetched news from RSS feeds for calendar", "count", len(items))
				return items, nil
			}
		}
	}

	return nil, fmt.Errorf("no news found for calendar in PostgreSQL, Supabase or RSS")
}
