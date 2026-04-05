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

━━━ STYLE ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
- Tone: journalistic, neutral, factual, dynamic. No opinions or emotions.
- Proper nouns UNCHANGED: names, brands, orgs, cities, countries, events.
  Examples: "Tinderbox", "NATO", "Fredericia", "Orbán" — never translate or alter.

━━━ LENGTH RULES (CRITICAL — Telegram will visibly cut text if exceeded!) ━━━
- "danish" body: STRICT MAX %d characters
- "ukrainian" body: STRICT MAX %d characters
- Both bodies MUST be approximately equal length (within ±15 percent of each other).
  If Danish = 280 chars → Ukrainian must be 238–322 chars. NOT 280 vs 500!
- Count characters before writing. Shorten the longer version if needed.

━━━ TASKS — return valid JSON only ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

"danish": News body in Danish. MAX %d chars.
	• DO NOT repeat the title.
	• DO NOT open with a pronoun (Han/Hun/De) without naming the person first.
	• Structure (3–5 sentences):
	1. Core fact with specific details (who, what, when, where + number/date)
	2. Background: WHY this happened or what led to it
	3. Impact: who is concretely affected and how
	4. (optional) Reaction or next development
	5. (optional) Broader Danish or EU context
	• If the source text lacks enough facts to fill sentences 1–3 specifically,
		write what you can but set has_enough_context to false.

"ukrainian": Same news in Ukrainian. MAX %d chars.
  • Mirror the SAME facts as Danish — same structure, same depth.
  • Ukrainian readers may not know Danish political context — add 1 short
    clarifying phrase if a name/institution needs it (e.g. "Folketing — данський парламент").
  • DO NOT add facts not present in Danish version.

"title_ukrainian": Ukrainian headline. MAX 85 chars.
  • Translate the TITLE only — short newspaper front-page style.
  • Proper nouns unchanged.

"mood": ONE of exactly: "positive" | "negative" | "neutral" | "shocking" | "urgent"

"category": ONE value from this list ONLY — %s
  Read the rules below — wrong category is a critical error.

  "war"       → ANY armed conflict, military strike, weapons, soldiers, casualties, ceasefire.
                Ukraine war, Gaza, Iran attack, Middle East, NATO defence spending.
                ✓ US strikes Iran  ✓ Russia attacks Ukraine  ✓ NATO summit on defence
                ✗ DO NOT use "eu" or "society" for conflict news.

  "eu"        → EU institutions ONLY: EP votes, Commission decisions, EU sanctions, EU budget.
                ✓ EP votes on migration  ✓ EU sanctions Russia
                ✗ DO NOT use for war news or general European politics.

  "politics"  → Danish domestic politics and governance: parliament (Folketing), government
                decisions, laws and reforms, party agreements, elections, defence policy,
                welfare reform, public service, state budget (finanslov), pension reform,
                climate policy. Use when a decision or change DIRECTLY affects life in Denmark.
                ✓ New law on taxes  ✓ Folketing votes on defence spending
                ✓ Government reform of healthcare  ✓ Party agreement on housing
                ✗ DO NOT use for war/military (use "war") or EU institutions (use "eu").

  "society"   → Default for social topics: Danish public life, integration, protests,
                immigration debate, Ukrainian refugees in Denmark.
                Use this when no other category fits clearly.

  "economy"   → Danish macroeconomics: GDP, inflation, trade, corporate news, interest rates.
                ✗ NOT personal finance (use "money").

  "money"     → Personal finance: benefits, taxes, salaries, cost of living, subsidies.

  "tech"      → Technology only: AI, IT, cybersecurity, software, startups, digitalisation.
                ✗ DO NOT use for political news that merely mentions technology.

  "local"     → Any specific Danish city or municipality: Copenhagen, Aarhus, Viborg,
                Odense, Aalborg, Esbjerg, or any named Danish region/municipality.
                MUST include sufficient context (what happened and why it matters at least somewhat nationally).
                If a local story lacks concrete facts or national relevance, assign category "local" but set impact_score penalty.

  "visas"     → Residence permits, asylum, Ukrainian TPS status, deportation, border control.
  "work"      → Jobs, employment, work permits, labour market statistics.
  "education" → Schools, universities, erhvervsskole, courses, student grants.
  "crime"     → Crime, police investigation, court rulings, prison sentences.
  "family"    → Children, daycare, parental leave, family benefits.
  "lifestyle" → Culture, festivals, food, travel, entertainment (non-sport).
  "sport"     → Competitive sports, leagues, athletes, tournaments.

"tags": 2–4 Ukrainian tags. Each tag: 1–2 words max, NO # symbol.
  ✓ Good: "оборона", "Іран", "НАТО"
  ✗ Bad: "збройний конфлікт між США та Іраном"

"tldr": ONE Ukrainian headline. STRICT MAX %d chars. Start with ONE emoji.
  This is a headline — 10–14 words maximum.
  ✓ Good: "💥 США завдали удар по Ірану, загинув Хаменеї"
  ✗ Too long: "💥 Атака США на Іран, що вбила Хаменеї, не отримала підтримки союзників"

"why_it_matters": ONE Ukrainian sentence. STRICT MAX %d chars.
  Answer ONE of:
  (a) What changes in taxes, benefits, rights, or daily life in Denmark
  (b) Why this matters for Ukraine, the war, or EU refugee policy
  (c) What upcoming decision residents should watch
  FORBIDDEN: vague impact ("може вплинути"), opinion, restatement of headline.
  If none apply → write exactly: 'Не впливає на повсякденне життя'

"fun_fact": ONE fact about Denmark. STRICT MAX %d chars. Start with ONE emoji.
  • Ukrainian language.
  • Must be RELATED to the selected "category" of this news.
    Example: politics → Danish governance/laws; money/economy → taxes, salaries, prices;
    work → labor market; education → schools/universities; tech → digital services/AI;
    war/eu/visas → defence, EU policy, migration rules.
  • Must add useful context, not repeat the headline details.
  • Must feel current and surprising — avoid clichés like "Denmark is happiest country"
    or "LEGO is from Denmark".
  ✓ Good: "🧾 У Данії більшість податкових змін набирають чинності з початку фінансового року, тому дедлайни часто оголошують заздалегідь"
  ✗ Bad: "🇩🇰 Данія — найщасливіша країна світу"

━━━ PROHIBITIONS ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
- No translator notes: "(Примітка: ...)", "це означає", "тобто"
- No hashtags inside danish/ukrainian/tldr fields
- Do NOT start danish or ukrainian with the title or its paraphrase
- Output ONLY valid JSON — no markdown, no explanations outside JSON

Output valid JSON only.
`, title, content,
		DefaultBudget.DanishChars, DefaultBudget.UkrainianChars,
		DefaultBudget.DanishChars, DefaultBudget.UkrainianChars,
		BuildValidCategoryList(),
		DefaultBudget.TLDRChars, DefaultBudget.WhyItMattersChars, DefaultBudget.FunFactChars)
}
