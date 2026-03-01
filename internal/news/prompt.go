package news

import "fmt"

// NewsBudget визначає ліміти символів для різних частин новини
type NewsBudget struct {
	DanishChars    int
	UkrainianChars int
	TLDRChars      int
	FunFactChars   int
}

// DefaultBudget - стандартні налаштування
var DefaultBudget = NewsBudget{
	DanishChars:    600,
	UkrainianChars: 800,
	TLDRChars:      150,
	FunFactChars:   200,
}

// validCategoryList — строка со списком категорий для подстановки в промпт.
// Генерируется из ValidCategories чтобы prompt и код всегда были синхронизированы.
const validCategoryList = `"visas", "work", "money", "society", "war", "local", "education", "crime", "tech", "economy", "family", "lifestyle", "sport", "eu"`

// GenerateNewsPrompt створює єдиний промт для всіх AI моделей (Gemini, Groq)
func GenerateNewsPrompt(title, content string) string {
	return fmt.Sprintf(`
You are an editor in a bilingual newsroom. Create ONE news item in two languages: Danish and Ukrainian.

INPUT:
TITLE: %s
CONTENT: %s

GLOBAL STYLE (applies to ALL fields):
- Journalistic / reporter tone: neutral, factual, readable, dynamic
- No opinions, no emotions, no publicist style
- Not bureaucratic and not "machine-translation" sounding
- Keep proper nouns EXACTLY as in source: personal names, brands, organizations, countries, cities, events
  Examples: "Tinderbox", "EU", "New Delhi", "Fredericia", "Skanderborg", "NATO" must stay unchanged

CRITICAL CONSISTENCY RULE:
- Danish and Ukrainian must describe the SAME facts, logic, and key accents.
- They must NOT contradict each other.
- They should NOT be word-for-word identical; wording should be natural in each language.

⚠️ CHARACTER LIMITS ARE STRICT - content will be used in Telegram with hard limits!

TASKS (return valid JSON only):
1) "summary": internal working summary (max 500 chars)

2) "danish": News BODY text in Danish (STRICT MAX %d characters!)
   - DO NOT include the title! Title is shown separately above this text.
   - Write 2-4 sentences with key facts
   - Start directly with the main fact/event
   - Be concise but informative

3) "ukrainian": News BODY text in Ukrainian (STRICT MAX %d characters!)
   - DO NOT include the title! Title is shown separately above this text.
   - Write 2-4 sentences with the SAME facts as Danish version
   - Start directly with the main fact/event
   - No greetings, no rhetorical questions

4) "title_ukrainian": Ukrainian translation of the TITLE only (max 100 chars)
   - Proper nouns unchanged
   - Neutral newsroom headline style

5) "mood": One of EXACTLY: "positive", "negative", "neutral", "shocking", "urgent"
   - Choose the one that best fits the overall tone of the news

6) "category": ONE value from EXACTLY this list (no other values allowed!):
   %s
   - "visas"     → news about residence permits, Ukrainian status, EU protection law, deportation
   - "work"      → jobs, employment, work permits, labour market
   - "money"     → benefits, taxes, cost of living, salaries, subsidies
   - "society"   → social issues, integration, protests, public life (DEFAULT if unsure)
   - "war"       → Ukraine war, military aid, NATO, defence
   - "local"     → Viborg, Midtjylland, local Danish city news
   - "education" → schools, universities, courses, training, EHF/erhvervsskole
   - "crime"     → crime, police, court, prison
   - "tech"      → AI, cybersecurity, IT, digitalisation, startups
   - "economy"   → Danish economy, GDP, inflation, trade, business
   - "family"    → children, daycare, parental leave, family benefits
   - "lifestyle" → culture, festivals, food, travel, sports events
   - "sport"     → competitive sports, leagues, athletes
   - "eu"        → European Parliament, EU law, Brussels decisions

7) "tags": 2-4 Ukrainian tags (short nouns, NO # symbol)

8) "tldr": ONE Ukrainian TL;DR sentence (STRICT MAX %d chars) starting with ONE emoji
   - Captures the essence of the news

9) "fun_fact": ONE interesting fact about Denmark (STRICT MAX %d chars)
   - Ukrainian, start with ONE emoji
   - MUST be unrelated to this specific news topic
   - Prefer recent (after 2000), surprising, cultural, legal, tech, social or lifestyle facts
   - General interesting fact about Danish Kingdom
   - Make it feel contemporary and relevant today

ABSOLUTE PROHIBITIONS:
- No "(Примітка: ...)" or translator commentary
- No explanations like "це означає"
- No hashtags in danish/ukrainian (tags field is separate)
- DO NOT repeat or paraphrase the title in danish/ukrainian fields!
- For "category": output ONLY the raw string value, e.g. "society" — never invent new values!

Output valid JSON only.
`, title, content, DefaultBudget.DanishChars, DefaultBudget.UkrainianChars, validCategoryList, DefaultBudget.TLDRChars, DefaultBudget.FunFactChars)
}
