package news

import "fmt"

// NewsBudget визначає ліміти символів для різних частин новини
type NewsBudget struct {
	DanishChars       int
	UkrainianChars    int
	TLDRChars         int
	FunFactChars      int
	WhyItMattersChars int
}

// DefaultBudget — character limits for each part of a news item.
//
// Budget breakdown for photo caption (1024 hard Telegram limit):
//
//	header:    ~25 chars   ("⚔️ ВІЙНА\n\n")
//	TLDR:      ~95 chars   ("💬 <b>...</b>\n\n")
//	DK title:  ~65 chars   ("🇩🇰 <b>Title</b>\n")
//	DK body:   320 chars
//	separator:  ~2 chars   ("\n\n")
//	UA title:  ~75 chars   ("🇺🇦 <b>Title</b>\n")
//	UA body:   320 chars
//	─────────────────────
//	Total:     ~902 chars  ✅ fits in 1024 with ~122 chars spare for tags
var DefaultBudget = NewsBudget{
	DanishChars:       320,
	UkrainianChars:    320,
	TLDRChars:         90,
	FunFactChars:      150,
	WhyItMattersChars: 140,
}

// GenerateNewsPrompt створює єдиний промт для всіх AI моделей (Gemini, Groq).
// Список категорій генерується динамічно з categories.go — завжди синхронізований.
func GenerateNewsPrompt(title, content string) string {
	return fmt.Sprintf(`You are an editor for a bilingual Telegram news channel for Ukrainian speakers in Denmark.
Your task: create ONE news item in two languages — Danish and Ukrainian — from the source below.

INPUT:
TITLE: %s
CONTENT: %s

━━━ STYLE & LENGTH ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
- Tone: journalistic, neutral, factual, dynamic. No opinions.
- Proper nouns UNCHANGED: names, brands, orgs, cities, countries. (e.g., "Folketing", "NATO").
- "danish" body: STRICT MAX %d characters.
- "ukrainian" body: STRICT MAX %d characters.
- Both bodies MUST be approx equal length (±15%%).

━━━ TASKS (RETURN VALID JSON ONLY) ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

"danish": News body in Danish. MAX %d chars.
  • AVOID vague generic sentences. EXACT facts (who, what, when, where, WHY, WHAT exactly).
  • Structure (3-5 sentences):
    1. Core fact with specific details (DO NOT paraphrase the title, give new information right away).
    2. Background/Details: what exactly led to this.
    3. Impact: who is affected and how.
  • DO NOT start with pronouns (Han/Hun) without naming the person.

"ukrainian": Same news in Ukrainian. MAX %d chars.
  • Mirror EXACT facts, deep context, and structure of Danish version.
  • Provide EXACT reason/subject; NO empty phrases like "Це вплине на життя".
  • Add 1 short clarifying phrase if a Danish political term needs it (e.g., "Folketing — данський парламент").

"title_danish": Danish headline. MAX 85 chars.

"title_ukrainian": Ukrainian headline. MAX 85 chars.

"mood": ONE of: "positive" | "negative" | "neutral" | "shocking" | "urgent"

"category": ONE value from this list ONLY — %s
  "war"       → Armed conflicts, global security, military, NATO.
  "eu"        → EU institutions, treaties, EU sanctions/budget.
  "politics"  → Danish governance, Folketing, laws, reforms, state budget, climate policy.
  "society"   → Danish public life, protests, integration, Ukrainian refugees in DK.
  "economy"   → Danish macroeconomics, GDP, inflation, trade, interest rates.
  "money"     → Personal finance, taxes, salaries, subsidies, cost of living.
  "tech"      → IT, AI, software, digitalization, cybersecurity.
  "local"     → Specific Danish city/region news WITH national relevance.
  "visas"     → Residence permits, asylum, deportation, citizenship.
  "work"      → Labor market, jobs, work permits.
  "education" → Universities, schools, courses, student grants.
  "crime"     → Police, court rulings, prison, fraud.
  "family"    → Childcare, parental leave, family benefits.
  "lifestyle" → Culture, festivals, food, entertainment.
  "sport"     → Sports leagues, athletes, tournaments.

"tags": 2–4 Ukrainian tags. Each tag: 1–2 words max, NO # symbol. (e.g. "податки", "робота").

"tldr": ONE Ukrainian headline. STRICT MAX %d chars. Start with ONE emoji. 10-14 words. (MUST be distinct from the main title, focus on the broader picture).

"why_it_matters": ONE Ukrainian sentence. STRICT MAX %d chars.
  • Must explain concrete impact (e.g., taxes, laws, rights) OR importance for Ukrainians in DK.
  • If no direct impact, explain what to watch out for.
  • If purely trivial, write exactly: 'Не впливає на повсякденне життя'
  • FORBIDDEN: vague impact ("може вплинути").

"fun_fact": ONE fact about Denmark in Ukrainian. STRICT MAX %d chars. Start with ONE emoji.
  • MUST match the selected "category" (e.g. tech -> IT services, money -> tax rules).
  • Add context, do not repeat the news. Avoid clichés like "Denmark is happiest".

━━━ PROHIBITIONS ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
- NO translator notes ("Примітка:").
- NO hashtags inside danish/ukrainian/tldr fields.
- DO NOT start the body ("danish" or "ukrainian") by paraphrasing or repeating the title. The first sentence MUST continue the story with new details.
- DO NOT make TLDR, Title, and the first sentence of the body say the exact same thing.
- Output ONLY valid JSON, no markdown outside JSON.
`, title, content,
		DefaultBudget.DanishChars, DefaultBudget.UkrainianChars,
		DefaultBudget.DanishChars, DefaultBudget.UkrainianChars,
		BuildValidCategoryList(),
		DefaultBudget.TLDRChars, DefaultBudget.WhyItMattersChars, DefaultBudget.FunFactChars)
}
