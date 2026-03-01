package news

import (
	"sort"
	"strings"
)

// Category — строго типизированная категория новости.
// Всегда используй константы ниже — никаких magic strings в коде.
type Category string

const (
	CategoryVisas     Category = "visas"
	CategoryWork      Category = "work"
	CategoryMoney     Category = "money"
	CategorySociety   Category = "society"
	CategoryWar       Category = "war"
	CategoryLocal     Category = "local"
	CategoryEducation Category = "education"
	CategoryCrime     Category = "crime"
	CategoryTech      Category = "tech"
	CategoryEconomy   Category = "economy"
	CategoryFamily    Category = "family"
	CategoryLifestyle Category = "lifestyle"
	CategorySport     Category = "sport"
	CategoryEU        Category = "eu"

	// CategoryDefault используется когда AI или keywords не дали валидной категории.
	CategoryDefault Category = "society"
)

// categoryEmoji — иконка темы, отображается в header новости перед label-ом.
//
// Правило выбора emoji: уникальный, не пересекается с mood-emoji (🟢🔴⚡🚨🔵)
// и не вызывает ложных ассоциаций с другими категориями.
var categoryEmoji = map[Category]string{
	CategoryVisas:     "🇺🇦",
	CategoryWork:      "💼",
	CategoryMoney:     "💰",
	CategorySociety:   "🗣️",
	CategoryWar:       "⚔️",
	CategoryLocal:     "🏙️",
	CategoryEducation: "🎓",
	CategoryCrime:     "🔍", // было 🚨 — конфликт с mood "urgent"
	CategoryTech:      "💻",
	CategoryEconomy:   "📊",
	CategoryFamily:    "👨‍👩‍👧‍👦",
	CategoryLifestyle: "🎭",
	CategorySport:     "⚽",
	CategoryEU:        "🇪🇺",
}

// categoryLabel — текст header-а новости (украинский, заглавними літерами).
// Всі лейбли — українською для консистентності.
var categoryLabel = map[Category]string{
	CategoryVisas:     "ВАЖЛИВО ДЛЯ УКРАЇНЦІВ",
	CategoryWork:      "РОБОТА",
	CategoryMoney:     "ГРОШІ",
	CategorySociety:   "СУСПІЛЬСТВО",
	CategoryWar:       "ВІЙНА",
	CategoryLocal:     "МІСТО",
	CategoryEducation: "ОСВІТА",
	CategoryCrime:     "КРИМІНАЛ", // було "CRIME"
	CategoryTech:      "ТЕХНОЛОГІЇ",
	CategoryEconomy:   "ЕКОНОМІКА",
	CategoryFamily:    "СІМ'Я",
	CategoryLifestyle: "СТИЛЬ ЖИТТЯ", // було "LIFESTYLE"
	CategorySport:     "СПОРТ",       // було "SPORT"
	CategoryEU:        "ЄВРОСОЮЗ",    // було "EU"
}

// ValidCategories — повний whitelist допустимих категорій.
// Використовується при валідації відповіді AI та RSS-категорій.
var ValidCategories = func() map[string]Category {
	m := make(map[string]Category, len(categoryEmoji))
	for c := range categoryEmoji {
		m[string(c)] = c
	}
	return m
}()

// BuildValidCategoryList returns a sorted quoted comma-separated list of all
// valid category strings for embedding in AI prompts.
// Generated from ValidCategories — always in sync with categories.go.
func BuildValidCategoryList() string {
	keys := make([]string, 0, len(ValidCategories))
	for k := range ValidCategories {
		keys = append(keys, `"`+k+`"`)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// ValidateCategory нормализует строку к Category.
// Если значение не входит в whitelist — возвращает CategoryDefault.
// Никогда не паникует, всегда возвращает валидную категорию.
func ValidateCategory(raw string) Category {
	c := Category(strings.ToLower(strings.TrimSpace(raw)))
	if _, ok := categoryEmoji[c]; ok {
		return c
	}
	return CategoryDefault
}

// CategoryEmoji возвращает emoji для категории (публичный accessor).
func CategoryEmoji(c Category) string {
	if e, ok := categoryEmoji[c]; ok {
		return e
	}
	return "📰"
}

// CategoryLabel возвращает текстовый label для категории (публичный accessor).
func CategoryLabel(c Category) string {
	if l, ok := categoryLabel[c]; ok {
		return l
	}
	return "НОВИНИ"
}
