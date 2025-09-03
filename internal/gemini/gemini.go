package gemini

import (
	"context"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

type Client struct {
	client *genai.Client
}

type NewsTranslation struct {
	Summary   string
	Danish    string
	Ukrainian string
}

func NewClient(apiKey string) (*Client, error) {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	return &Client{client: client}, nil
}

func (c *Client) Close() {
	if c.client != nil {
		c.client.Close()
	}
}

func (c *Client) TranslateAndSummarizeNews(title, content string) (*NewsTranslation, error) {
	ctx := context.Background()
	model := c.client.GenerativeModel("gemini-1.5-flash")

	// Sanitize & limit content size (avoid over-long prompts)
	content = strings.ReplaceAll(content, "\r", "")
	content = strings.TrimSpace(content)
	// Collapse excessive whitespace
	content = strings.Join(strings.Fields(content), " ")
	maxChars := 6000
	if utf8.RuneCountInString(content) > maxChars {
		// cut on rune boundary then try to end at sentence
		runes := []rune(content)
		trimmed := string(runes[:maxChars])
		if idx := strings.LastIndex(trimmed, ". "); idx > 1200 { // keep some meaningful size
			trimmed = trimmed[:idx+1]
		}
		content = trimmed + "\n[TRUNCATED]"
	}

	prompt := fmt.Sprintf(`
Анализируй эту новость и выполни следующие задачи:

НОВОСТЬ:
Заголовок: %s
Содержание: %s

ЗАДАЧИ:
1. Створи стислу версію новини (до 1500 символів)
2. Переклади цю новина на Данську (естественно, без дословности)
3. Переклади цю новину на Українську(естественно)

ТРЕБОВАНИЯ:
- Не переводить имена собственные брендов/организаций.
- Избегай вводных слов типа "Новость о том, что".
- Формат строго по шаблону ниже.

ФОРМАТ Відповіді (НА УКРАЇНСЬКІЙ МОВІ):
СУТЬ: <коротка суть як тема>
DANSK: < переклад на данську>
UKRAINIAN: <переклад  на Українську>

Приклад:
СУТЬ: Новий продукт Y від компанії X, який революціонізує галузь Z.

УКРАЇНСЬКА: Компанія X презентувала свій новий продукт Y, який уже називають справжнім технологічним проривом. За словами керівництва компанії, цей продукт здатний не лише вдосконалити існуючі процеси у галузі Z, але й повністю змінити уявлення про те, як має працювати ця сфера в майбутньому.

Особливістю продукту Y є поєднання новітніх наукових досліджень, штучного інтелекту та сучасних дизайнерських підходів. Генеральний директор компанії X підкреслив, що ця розробка стала результатом багаторічної праці та тісної співпраці з провідними експертами галузі.

Продукт Y обіцяє зменшити витрати, підвищити ефективність та створити нові робочі місця. Уже зараз експерти прогнозують, що протягом кількох років використання цього продукту стане стандартом у галузі Z.

Компанія X планує розпочати масове виробництво Y найближчими місяцями, а перші клієнти вже отримали можливість протестувати новинку. Реакція ринку виявилася надзвичайною: багато компаній заявили про готовність якнайшвидше інтегрувати Y у свої процеси.

Аналітики впевнені, що ми стоїмо на порозі нової ери розвитку галузі Z, і саме продукт Y може стати ключовим елементом цього процесу.

DANSK: Firmaet X har lanceret sit nyeste produkt, Y, som allerede bliver omtalt som et teknologisk gennembrud. Ifølge ledelsen i virksomheden vil produktet ikke blot forbedre de eksisterende processer i branche Z, men også ændre hele opfattelsen af, hvordan sektoren skal fungere i fremtiden.
Produkt Y kombinerer banebrydende forskning, kunstig intelligens og innovative designmetoder. Administrerende direktør i firma X fortæller, at løsningen er resultatet af mange års udvikling og tæt samarbejde med de førende eksperter på området.
Med Y kan virksomheder reducere omkostninger, øge effektiviteten og skabe nye arbejdspladser. Eksperter forudser allerede nu, at produktet inden for få år bliver en standard i branche Z.
Firma X planlægger at starte masseproduktion af Y i de kommende måneder, og de første kunder har allerede fået mulighed for at teste produktet. Markedets reaktion har været imponerende: mange virksomheder viser stor interesse for at implementere Y så hurtigt som muligt.
Analytikere peger på, at vi står foran en ny æra i udviklingen af branche Z, hvor produktet Y kan blive en afgørende drivkraft.

.
`, title, content)

	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return nil, fmt.Errorf("failed to generate content: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no response from Gemini")
	}

	response := fmt.Sprintf("%v", resp.Candidates[0].Content.Parts[0])
	return parseGeminiResponse(response)
}

func parseGeminiResponse(response string) (*NewsTranslation, error) {
	lines := strings.Split(response, "\n")

	var summary, danish, ukrainian string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "СУТЬ:") {
			summary = strings.TrimSpace(strings.TrimPrefix(line, "СУТЬ:"))
		} else if strings.HasPrefix(line, "ДАТСКИЙ:") {
			danish = strings.TrimSpace(strings.TrimPrefix(line, "ДАТСКИЙ:"))
		} else if strings.HasPrefix(line, "УКРАИНСКИЙ:") {
			ukrainian = strings.TrimSpace(strings.TrimPrefix(line, "УКРАИНСКИЙ:"))
		}
	}

	// Fallback parsing if the format is different
	if summary == "" || danish == "" || ukrainian == "" {
		log.Printf("Warning: Could not parse Gemini response properly. Raw response: %s", response)

		parts := strings.Split(response, "\n")
		if len(parts) >= 3 {
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part != "" {
					if summary == "" {
						summary = part
					} else if danish == "" {
						danish = part
					} else if ukrainian == "" {
						ukrainian = part
						break
					}
				}
			}
		}
	}

	if summary == "" || danish == "" || ukrainian == "" {
		return nil, fmt.Errorf("could not parse Gemini response: missing required fields")
	}

	return &NewsTranslation{
		Summary:   summary,
		Danish:    danish,
		Ukrainian: ukrainian,
	}, nil
}
