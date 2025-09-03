package gemini

import (
	"context"
	"fmt"
	"log"
	"regexp"
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
Аналізуй цю новину та виконай наступні завдання:

НОВИНА:
Заголовок: %s
Содержание: %s

ЗАВДАННЯ:

Створи стислу версію новини (до 1500 символів).

Переклади цю новину на данську (природно, без дослівності).

Переклади цю новину на українську (природно).

ВИМОГИ:

Не перекладати імена власні брендів/організацій.

Уникати вводних слів типу «Новина про те, що…».

Формат відповіді суворо за шаблоном нижче.

СУТЬ: <коротка суть як тема>

UKRAINIAN: <переклад на українську>

DANSK: <переклад на данську>


Приклад:

СУТЬ: Новий продукт Y від компанії X, який революціонізує галузь Z.

UKRAINIAN: Компанія X презентувала свій новий продукт Y, який уже називають справжнім технологічним проривом. За словами керівництва компанії, цей продукт здатний не лише вдосконалити існуючі процеси у галузі Z, але й повністю змінити уявлення про те, як має працювати ця сфера в майбутньому.

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

	var summaryBuilder, danishBuilder, ukrainianBuilder strings.Builder

	// Patterns for labels (case-insensitive, optional colon, allow Cyrillic variants)
	labelPatterns := []struct {
		name  string
		regex *regexp.Regexp
	}{
		{"summary", regexp.MustCompile(`(?i)^(СУТЬ|Суть)\s*: ?`)},
		{"danish", regexp.MustCompile(`(?i)^(DANSK|ДАНСЬКА)\s*: ?`)},
		{"ukrainian", regexp.MustCompile(`(?i)^(UKRAINIAN|УКРАЇНСЬКА|УКРАИНСКИЙ|УКРАЇНСЬКА МОВА)\s*: ?`)},
	}

	current := ""

	appendText := func(section string, text string) {
		if text == "" {
			return
		}
		switch section {
		case "summary":
			if summaryBuilder.Len() > 0 {
				summaryBuilder.WriteString(" ")
			}
			summaryBuilder.WriteString(text)
		case "danish":
			if danishBuilder.Len() > 0 {
				danishBuilder.WriteString(" ")
			}
			danishBuilder.WriteString(text)
		case "ukrainian":
			if ukrainianBuilder.Len() > 0 {
				ukrainianBuilder.WriteString(" ")
			}
			ukrainianBuilder.WriteString(text)
		}
	}

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		matchedLabel := false
		for _, lp := range labelPatterns {
			if lp.regex.MatchString(line) {
				// New section
				content := lp.regex.ReplaceAllString(line, "")
				current = lp.name
				appendText(current, strings.TrimSpace(content))
				matchedLabel = true
				break
			}
		}
		if matchedLabel {
			continue
		}

		// Continuation line for current section
		if current != "" {
			appendText(current, line)
		}
	}

	summary := strings.TrimSpace(summaryBuilder.String())
	danish := strings.TrimSpace(danishBuilder.String())
	ukrainian := strings.TrimSpace(ukrainianBuilder.String())

	// Fallback: older label names (legacy) if nothing parsed
	if summary == "" || danish == "" || ukrainian == "" {
		legacySummaryPrefix := "СУТЬ:"
		legacyDanishPrefix := "ДАТСКИЙ:"
		legacyUkrainianPrefix := "УКРАИНСКИЙ:"
		if summary == "" || danish == "" || ukrainian == "" {
			for _, line := range lines {
				l := strings.TrimSpace(line)
				if summary == "" && strings.HasPrefix(l, legacySummaryPrefix) {
					summary = strings.TrimSpace(strings.TrimPrefix(l, legacySummaryPrefix))
				}
				if danish == "" && strings.HasPrefix(l, legacyDanishPrefix) {
					danish = strings.TrimSpace(strings.TrimPrefix(l, legacyDanishPrefix))
				}
				if ukrainian == "" && strings.HasPrefix(l, legacyUkrainianPrefix) {
					ukrainian = strings.TrimSpace(strings.TrimPrefix(l, legacyUkrainianPrefix))
				}
			}
		}
	}

	// Additional fallback: naive split into three large blocks if still missing
	if summary == "" || danish == "" || ukrainian == "" {
		log.Printf("Warning: fallback parsing triggered. Raw response: %s", response)
		chunks := []string{}
		acc := strings.Builder{}
		for _, raw := range lines {
			l := strings.TrimSpace(raw)
			if l == "" {
				continue
			}
			acc.WriteString(l)
			acc.WriteString(" ")
			if len(acc.String()) > 1200 { // heuristic split
				chunks = append(chunks, strings.TrimSpace(acc.String()))
				acc.Reset()
			}
		}
		if acc.Len() > 0 {
			chunks = append(chunks, strings.TrimSpace(acc.String()))
		}
		for _, c := range chunks {
			if summary == "" {
				summary = c
				continue
			}
			if danish == "" {
				danish = c
				continue
			}
			if ukrainian == "" {
				ukrainian = c
				continue
			}
		}
	}

	// Detect accidental swap (Gemini sometimes flips labels). Simple heuristic by alphabet presence.
	if looksUkrainian(danish) && looksDanish(ukrainian) {
		log.Printf("Info: swapping Danish/Ukrainian blocks (detected inversion)")
		danish, ukrainian = ukrainian, danish
	}
	// Another swap case: Danish missing Danish letters but Ukrainian text contains none Ukrainian letters -> skip

	if summary == "" || danish == "" || ukrainian == "" {
		return nil, fmt.Errorf("could not parse Gemini response: missing required fields (summary=%t danish=%t ukrainian=%t)", summary != "", danish != "", ukrainian != "")
	}

	return &NewsTranslation{Summary: summary, Danish: danish, Ukrainian: ukrainian}, nil
}

func looksUkrainian(s string) bool {
	// Count distinctive Ukrainian letters
	ukChars := "іїєґЙйЖжШшЩщЮюЯяІіЄєҐґ"
	count := 0
	for _, r := range s {
		if strings.ContainsRune(ukChars, r) {
			count++
		}
		if count > 3 {
			return true
		}
	}
	return false
}

func looksDanish(s string) bool {
	daChars := "æøåÆØÅ"
	count := 0
	for _, r := range s {
		if strings.ContainsRune(daChars, r) {
			count++
		}
		if count > 2 {
			return true
		}
	}
	return false
}
