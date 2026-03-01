package news

import "strings"

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
var categoryEmoji = map[Category]string{
	CategoryVisas:     "🇺🇦",
	CategoryWork:      "💼",
	CategoryMoney:     "💰",
	CategorySociety:   "📋",
	CategoryWar:       "⚔️",
	CategoryLocal:     "🏙️",
	CategoryEducation: "🎓",
	CategoryCrime:     "🚨",
	CategoryTech:      "💻",
	CategoryEconomy:   "📊",
	CategoryFamily:    "👨‍👩‍👧‍👦",
	CategoryLifestyle: "🎭",
	CategorySport:     "⚽",
	CategoryEU:        "🇪🇺",
}

// categoryLabel — текст header-а новости (украинский, заглавными буквами).
var categoryLabel = map[Category]string{
	CategoryVisas:     "ВАЖЛИВО ДЛЯ УКРАЇНЦІВ",
	CategoryWork:      "РОБОТА",
	CategoryMoney:     "ГРОШІ",
	CategorySociety:   "СУСПІЛЬСТВО",
	CategoryWar:       "ВІЙНА",
	CategoryLocal:     "VIBORG",
	CategoryEducation: "ОСВІТА",
	CategoryCrime:     "CRIME",
	CategoryTech:      "ТЕХНОЛОГІЇ",
	CategoryEconomy:   "ЕКОНОМІКА",
	CategoryFamily:    "СІМ'Я",
	CategoryLifestyle: "LIFESTYLE",
	CategorySport:     "SPORT",
	CategoryEU:        "EU",
}

// ValidCategories — полный whitelist допустимых категорий.
// Используется при валидации ответа AI и RSS-категорий.
var ValidCategories = func() map[string]Category {
	m := make(map[string]Category, len(categoryEmoji))
	for c := range categoryEmoji {
		m[string(c)] = c
	}
	return m
}()

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
