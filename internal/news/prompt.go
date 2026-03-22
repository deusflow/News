package news

import (
	"fmt"
	"strings"

	"github.com/deusflow/News/internal/storage"
)

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
  • DO NOT repeat the title — it is shown above this text separately.
  • 3 sentences: main fact → context → consequence.
  • Include specific numbers, dates, names where available.

"ukrainian": Same news in Ukrainian. MAX %d chars.
  • Exactly the same facts as Danish — no additions, no omissions.
  • DO NOT repeat the title.
  • 3 sentences: same structure as Danish.

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
  • Explain systemic impact for residents in Denmark: what changes, who is affected, why now.
  • No slogans, no pathos, no moral judgement.
  • This is NOT a retelling of the headline; it is the practical consequence.
  ✓ Good: "Черга на місця практики зростає, тому випускники довше залишаються без першої роботи."

"fun_fact": ONE fact about Denmark. STRICT MAX %d chars. Start with ONE emoji.
  • Ukrainian language.
  • Must be UNRELATED to this news topic.
  • Must feel current and surprising — NOT clichés like "Denmark is happiest country"
    or "LEGO is from Denmark". Prefer facts about Danish law, society, tech, daily life.
  ✓ Good: "🧾 У Данії заборонено давати дитині ім'я, якого немає у державному списку з 7000 імен"
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

// GenerateWeeklyDigestPrompt builds a strict JSON prompt for weekly digest.
// It asks AI to synthesize consequences, not copy-paste source bullets.
func GenerateWeeklyDigestPrompt(items []storage.DigestNewsItem) string {
	var b strings.Builder
	for i, item := range items {
		fmt.Fprintf(&b, "%d) [%s] %s | source=%s | date=%s", i+1,
			strings.TrimSpace(item.Category),
			strings.TrimSpace(item.Title),
			strings.TrimSpace(item.Source),
			item.PublishedTime.UTC().Format("2006-01-02"))
		if strings.TrimSpace(item.WhyItMatters) != "" {
			fmt.Fprintf(&b, " | why=%s", strings.TrimSpace(item.WhyItMatters))
		}
		b.WriteString("\n")
	}

	return fmt.Sprintf(`You are a senior editor for a Ukrainian-language Denmark news channel.

TASK:
From the weekly list below, pick exactly 7 MOST important and surprising events.
Selection criteria (priority order):
1) Scale and real impact on people in Denmark/EU
2) Practical consequences for daily life (law, money, work, transport, migration, safety)
3) Unexpectedness / shock value

IMPORTANT:
- Do NOT copy source text.
- Rewrite in your own words.
- For each item: 1 short thesis sentence about what happened + 1 short consequence sentence.
- Focus on consequences, not rhetoric.

WEEKLY DATA:
%s

Output ONLY valid JSON with these fields:
{
  "danish": "4-6 concise Danish lines summary for Danish-speaking followers",
  "ukrainian": "Exactly 7 bullet lines (1-7). Each line has: what happened + practical consequence",
  "title_ukrainian": "Головне за тиждень у Данії та ЄС",
  "mood": "neutral",
  "category": "politics",
  "tags": ["дайджест", "данія", "єс"],
  "tldr": "📌 Топ-7 подій тижня: що сталося і як це вплине на життя",
  "why_it_matters": "Один короткий висновок: яка системна зміна для людей у Данії.",
  "fun_fact": "💡 Короткий новий факт про Данію без кліше"
}
`, b.String())
}
