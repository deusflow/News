package news

// CategoryImages — статические качественные картинки с Unsplash для каждой категории.
// Это позволяет боту всегда публиковать красивые посты без использования платных API или ключей.
// Картинки подобраны вручную для максимального соответствия тематике Дании и новостей.
var CategoryImages = map[Category]string{
	CategoryVisas:     "https://images.unsplash.com/photo-1555848962-6e79363ec58f?w=1600&h=900&fit=crop", // Passport/Travel
	CategoryWork:      "https://images.unsplash.com/photo-1521737604893-d14cc237f11d?w=1600&h=900&fit=crop", // Office/Business
	CategoryMoney:     "https://images.unsplash.com/photo-1580519542036-ed47f3e42214?w=1600&h=900&fit=crop", // Coins/Finance
	CategorySociety:   "https://images.unsplash.com/photo-1517486808906-6ca8b3f04846?w=1600&h=900&fit=crop", // People/City
	CategoryWar:       "https://images.unsplash.com/photo-1503460867056-11f421f57ec0?w=1600&h=900&fit=crop", // Soldier/News
	CategoryLocal:     "https://images.unsplash.com/photo-1513622470522-26c308a371e5?w=1600&h=900&fit=crop", // Copenhagen city
	CategoryEducation: "https://images.unsplash.com/photo-1523050854058-8df90110c9f1?w=1600&h=900&fit=crop", // University/Books
	CategoryCrime:     "https://images.unsplash.com/photo-1589829085413-56de8ae18c73?w=1600&h=900&fit=crop", // Police/Justice
	CategoryTech:      "https://images.unsplash.com/photo-1518770660439-4636190af475?w=1600&h=900&fit=crop", // Tech/Circuit
	CategoryEconomy:   "https://images.unsplash.com/photo-1611974789855-9c2a0a7236a3?w=1600&h=900&fit=crop", // Economy/Stock chart
	CategoryFamily:    "https://images.unsplash.com/photo-1511895426328-dc8714191300?w=1600&h=900&fit=crop", // Family/Kids
	CategoryLifestyle: "https://images.unsplash.com/photo-1514933651103-005eec06c04b?w=1600&h=900&fit=crop", // Lifestyle/Cafe
	CategorySport:     "https://images.unsplash.com/photo-1461896836934-ffe607ba8211?w=1600&h=900&fit=crop", // Sports/Stadium
	CategoryEU:        "https://images.unsplash.com/photo-1555909241-1555776d6537?w=1600&h=900&fit=crop", // EU flags/Parliament
	CategoryPolitics:  "https://images.unsplash.com/photo-1529107386315-e1a2ed48a620?w=1600&h=900&fit=crop", // Parliament/Denmark flag
}

// GetCategoryImage возвращает ссылку на стоковое фото для заданной категории.
// Если категория неизвестна, возвращает дефолтное фото Дании.
func GetCategoryImage(c Category) string {
	if url, ok := CategoryImages[c]; ok {
		return url
	}
	// Дефолтное красивое фото Дании (Нюхавн)
	return "https://images.unsplash.com/photo-1513622470522-26c308a371e5?w=1600&h=900&fit=crop"
}
