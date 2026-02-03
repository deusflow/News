# Конфиг-чэклист: как менять поведение бота и сайта безопасно

Цель: быстро понять, что и где менять, какой эффект это даст, какие пары параметров важно менять вместе, и в каких файлах находятся соответствующие настройки.

Формат записи:
- Имя настройки (где менять) — эффект, риски, связанные параметры, переменная окружения (если есть)

---

## 0) Быстрые сценарии (готовые рецепты)

- Хочу, чтобы почти всё делал Gemini, а Groq включался только как запасной
  - MAX_GEMINI_REQUESTS: 8–10 (env) — Gemini обрабатывает больше новостей за запуск
  - MAX_GROQ_REQUESTS: 0–3 (env) — Groq только fallback
  - Rate limiter для Gemini: 40s (internal/gemini/gemini.go) — супербезопасный лимит для Free Tier
  - Убрать искусственные задержки в news.go — уже убрано; скорость регулирует rate limiter в gemini.go

- Хочу меньше новостей за запуск
  - MAX_NEWS_LIMIT (env) или Config.MaxNewsLimit — снизить, например до 4
  - Влияет на общее время выполнения и число AI-запросов

- Хочу показывать только новости, полезные украинцам в Дании (SL1, семья, дети)
  - configs/keywords.yaml — усилить блоки ukrainians_in_denmark, family_life, positive_denmark; при необходимости ещё добавить слова
  - internal/news/news.go — приоритезация уже настроена: новости для украинцев в DK > семья/дети > позитивная DK > остальное

- Хочу включить генерацию сайта и архив в Supabase
  - ENABLE_WEBSITE=true (env) — включает экспорт новостей в Hugo контент
  - SUPABASE_URL + SUPABASE_SERVICE_KEY (env) — включает сохранение новостей в news_archive
  - Рекомендация: в Supabase добавить UNIQUE по source_url, иначе возможны дубли

---

## 1) Настройки AI и лимиты

- GeminiAPIKey, GeminiModel (internal/config/config.go, env: GEMINI_API_KEY, GEMINI_MODEL)
  - Что делает: ключ и модель Gemini
  - Где менять: env предпочтительно
  - Пара: MAX_GEMINI_REQUESTS, rate limiter в internal/gemini/gemini.go

- MaxGeminiRequests (config.go, env: MAX_GEMINI_REQUESTS)
  - Что делает: максимум запросов к Gemini за один запуск бота
  - Рекомендация: 8–10 для Free Tier + «тихий» режим работы
  - Пара: rateInterval (internal/gemini/gemini.go), MaxTotalAIRequests

- Rate limiter Gemini (internal/gemini/gemini.go: rateInterval)
  - Что делает: интервал между запросами к Gemini (безопасный троттлинг)
  - Рекомендация: 40s для Free Tier (≈1.5 RPM, далеко от лимита 10–15 RPM)
  - Меняется только в коде; будьте осторожны, это влияет на общее время запуска

- MaxGroqRequests (config.go, env: MAX_GROQ_REQUESTS)
  - Что делает: ограничение fallback-переводов через Groq
  - Рекомендация: 0–3, если хотим «только при фейле Gemini»
  - Пара: MaxTotalAIRequests (общее число AI-запросов)

- MaxTotalAIRequests (config.go, env: MAX_TOTAL_AI_REQUESTS)
  - Что делает: общий лимит запросов ко всем моделям
  - Если выставить слишком низкий, может обрезать поток новостей независимо от MaxGeminiRequests

- EnableBatching, BatchSize (config.go, env: ENABLE_BATCHING, BATCH_SIZE)
  - Что делает: батчинг новостей в один AI-запрос (экономия токенов)
  - Рекомендация: включено, BatchSize=2 (консервативно)
  - Пара: MaxGeminiRequests (батчинг снижает число запросов)

---

## 2) Объём новостей и сбор

- MaxNewsLimit (config.go, env: MAX_NEWS_LIMIT)
  - Что делает: максимум новостей за один прогон
  - Пара: MaxGeminiRequests, чтобы не попасть в ситуацию «новостей больше, чем запросов к Gemini»

- NewsMaxAge (config.go)
  - Что делает: максимальный возраст новости, которую считаем актуальной
  - Пара: MaxNewsLimit (меньше старых новостей — меньше шума)

- ScrapeConcurrency, ScrapeMaxArticles (config.go, env: SCRAPE_CONCURRENCY, SCRAPE_MAX_ARTICLES)
  - Что делает: скорость загрузки и разбор полных статей
  - Риск: слишком большая конкуренция может перегружать источники и бота (но AI-лимиты независимы)

---

## 3) Ключевые слова и приоритезация

- configs/keywords.yaml
  - Блоки: ukrainians_in_denmark, family_life, positive_denmark, refugee_boost, ukraine_war, viborg, economy, construction, leisure, exclude
  - Что делает: влияет на счёт новости (calculateNewsScore в internal/news/news.go)
  - Где менять: только YAML; перезапуск бота перезагружает keywords
  - Пара: internal/news/news.go (логика приоритезации уже настроена)

- internal/news/news.go → calculateNewsScore
  - Приоритеты уже смещены в пользу «украинцы в Дании», семьи/детей и позитивных новостей
  - Если меняете логику приоритезации — делайте это осознанно, это влияет на общую ленту

---

## 4) Публикация, формат и лимиты текста

- PostingPolicy (config.go, env: POSTING_POLICY)
  - Значения: hybrid | photo-only | text-only | two-messages (reserved)
  - Что делает: стратегия постинга в Telegram
  - Пары: PhotoCaptionMaxRunes, TextSentencesPerLangMin/Max

- PhotoCaptionMaxRunes, PhotoMinPerLangRunes, PhotoSentencesPerLang (config.go, env: PHOTO_* )
  - Что делает: размер и структура подписи к фото
  - Пара: MinSummaryTotalRunes — не ставьте слишком низко, иначе контент будет «пустым»

- TextSentencesPerLangMin/Max (config.go, env: TEXT_SENTENCES_PER_LANG_*)
  - Что делает: диапазон для количества предложений в текстовом режиме

- LanguagePriority (config.go, env: LANGUAGE_PRIORITY)
  - Значения: uk | da | auto
  - Что делает: приоритет языка вывода в некоторых стратегиях

---

## 5) Кэш/дубликаты/БД

- CacheFilePath, CacheTTLHours, DuplicateWindow (config.go, env: CACHE_*, DUPLICATE_WINDOW_HOURS)
  - Что делает: локальный кэш отправленных новостей и окно антидублей
  - Пара: DatabaseTTL, UsePostgres (если используете БД)

- UsePostgres, DatabaseURL, DatabaseTTL (config.go, env: USE_POSTGRES, DATABASE_URL, DATABASE_TTL_HOURS)
  - Что делает: переключение на PostgreSQL для кеша/антидублей
  - Пара: CacheTTLHours — рекомендую согласовать TTL

- SupabaseURL, SupabaseServiceKey, EnableSupabase (config.go, env: SUPABASE_URL, SUPABASE_SERVICE_KEY)
  - Что делает: включает сохранение в Supabase.news_archive (для сайта)
  - Пары: UNIQUE(source_url) в Supabase, scripts/generate-pages.sh (проверка дубликатов по base slug)

---

## 6) Сайт и генерация

- EnableWebsite (config.go, env: ENABLE_WEBSITE)
  - Что делает: включает выпуск Hugo-контента
  - Пара: WebsiteContentDir (env: WEBSITE_CONTENT_DIR), GitHub Actions workflow

- scripts/generate-pages.sh
  - Генерация markdown из Supabase; уже есть защита от дублей по base slug
  - Если slug-политика меняется — проверяйте, чтобы новые файлы не плодили дублей

---

## 7) Telegram

- TelegramToken, TelegramChatID (env: TELEGRAM_TOKEN, TELEGRAM_CHAT_ID)
  - Обязательные параметры для постинга

- ChannelUsername (config.go, env: CHANNEL_USERNAME)
  - Что делает: сборка URL-кнопок на канал

- EnableInlineButtons, InlineButtonMode (env: ENABLE_INLINE_BUTTONS, INLINE_BUTTON_MODE)
  - Что делает: кнопки под постом; режимы: callback/url

---

## 8) Важные пары параметров (меняются вместе)

- MaxGeminiRequests ↔ rateInterval (internal/gemini/gemini.go)
  - Больше запросов → следить, чтобы интервал оставался безопасным (40s для Free Tier)

- MaxNewsLimit ↔ MaxGeminiRequests ↔ MaxTotalAIRequests
  - Не делайте так, чтобы новостей было сильно больше, чем лимитов AI

- EnableWebsite ↔ SupabaseURL/SupabaseServiceKey ↔ GitHub Actions
  - Генерация сайта из Supabase требует настроенных Actions и ключей

- CacheTTLHours ↔ DatabaseTTL
  - Синхронизируйте, чтобы поведение антидублей было предсказуемым

- keywords.yaml ↔ calculateNewsScore (news.go)
  - Меняя приоритеты, убедитесь, что новая логика действительно отражает нужные темы

---

## 9) Где менять — резюме

- БОльшая часть оперативных настроек — через переменные окружения (env)
  - Примеры: MAX_GEMINI_REQUESTS, MAX_NEWS_LIMIT, ENABLE_WEBSITE, SUPABASE_URL, SUPABASE_SERVICE_KEY, USE_POSTGRES
- Тонкая логика и алгоритмы — в коде
  - Примеры: rateInterval в internal/gemini/gemini.go, приоритезация в internal/news/news.go
- Тематические фильтры — в configs/keywords.yaml

---

## 10) Мини-шпаргалка по env (ключевые)

- GEMINI_API_KEY — ключ Gemini (обязательно)
- GEMINI_MODEL — модель (по умолчанию gemini-flash-latest)
- MAX_GEMINI_REQUESTS — сколько новостей обрабатывает Gemini за запуск
- MAX_GROQ_REQUESTS — сколько может обработать Groq (fallback)
- MAX_TOTAL_AI_REQUESTS — общий лимит AI-запросов
- MAX_NEWS_LIMIT — сколько новостей берём за запуск
- SCRAPE_CONCURRENCY — параллелизм загрузки статей
- ENABLE_WEBSITE — включить генерацию сайта
- WEBSITE_CONTENT_DIR — путь к контенту Hugo
- SUPABASE_URL, SUPABASE_SERVICE_KEY — включают Supabase-архив
- USE_POSTGRES, DATABASE_URL — использовать PostgreSQL для кеша/антидублей
- CACHE_TTL_HOURS, DATABASE_TTL_HOURS, DUPLICATE_WINDOW_HOURS — TTL и окно антидублей
- POSTING_POLICY, PHOTO_*, TEXT_SENTENCES_PER_LANG_* — формат постинга
- ENABLE_INLINE_BUTTONS, INLINE_BUTTON_MODE, CHANNEL_USERNAME — кнопки и ссылки

---

## 11) Безопасные значения по умолчанию (Free Tier)

- rateInterval (Gemini): 40s (internal/gemini/gemini.go)
- MAX_GEMINI_REQUESTS: 8–10 (env)
- MAX_GROQ_REQUESTS: 0–3 (env)
- MAX_NEWS_LIMIT: 6–8 (env)
- SCRAPE_CONCURRENCY: 6–8 (env)

Это даёт ~5–7 минут на прогон и остаётся очень далеко от лимитов.

---

Если нужна «режим экономии» ещё жёстче — просто уменьшайте MAX_GEMINI_REQUESTS и/или MAX_NEWS_LIMIT.
