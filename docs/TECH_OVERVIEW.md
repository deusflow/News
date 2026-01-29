# DaUa News — Технологии проекта (кратко)

Краткий обзор основных технологий, которые используются в боте, сайте и автоматизации.

## 1. Язык и бэкенд
- **Go**
  - Основной язык проекта.
  - Бот, логика фильтрации новостей, форматирование текста, работа с API.

## 2. Источники новостей
- **RSS-ленты (датские СМИ)**
  - Модуль `internal/rss`.
  - Получение свежих новостей из нескольких источников.
- **HTML-скрейпер**
  - Модуль `internal/scraper`.
  - Дотягивание полного текста и картинок (og:image), если в RSS мало данных.

## 3. AI и переводы
- **Google Gemini** (`internal/gemini`)
  - Перевод и суммаризация новостей.
  - Генерация: датський текст, український текст, mood, хештеги, TL;DR, fun fact.
- **Translate fallback-стек** (`internal/translate`)
  - Groq, Cohere, Mistral, Google Translate.
  - Используются по очереди, если основной сервис недоступен.
- **SanitizeAIText**
  - Чистит AI-тексты от дисклеймеров и служебных фраз.

## 4. Хранение данных
- **PostgreSQL** (`internal/storage/postgres.go`)
  - Таблица `sent_news` — защита от повторных отправок в Telegram.
  - Таблица `translation_cache` — кэш AI-переводов.
  - Таблица `failed_items` — "мёртвые" сообщения, которые не удалось отправить.
- **Supabase (Postgres + REST API)** (`internal/storage/supabase.go`)
  - Таблица `news_archive` — архив новостей для сайта.
  - Сохранение новости только после успешной отправки в Telegram.
  - Проверка дублей по заголовку (normalize + сравнение слов) с таймаутом 2s.

## 5. Телеграм-бот
- **Telegram Bot API** (`internal/telegram`)
  - Отправка сообщений и фото в канал.
  - Инлайн-кнопка "Читати оригінал / Læs mere".
- **Форматирование текста** (`internal/news`)
  - Двухъязычный формат (🇩🇰 + 🇺🇦), TL;DR, теги.
  - Безопасная обрезка текста по рунам (UTF-8), без panic.

## 6. Статический сайт
- **Hugo** (`website/`)
  - Генерация статического сайта из Markdown-постов.
  - Свой layout без темы: `layouts/_default`, `layouts/partials`.
- **Custom CSS** (`website/static/css/style.css`)
  - Дизайн в стиле Crunchyroll + розовый акцент.
  - Хедер с мягкой волной (анимация), карточки новостей, Trending, пагинация по дням.
- **Weather Widget (Copenhagen)**
  - API: Open-Meteo (без ключа).
  - Показ: температура, ощущается как, ветер, влажность, иконка погоды.

## 7. Архитектура сайта
- **Supabase → Markdown → Hugo**
  - Скрипт `scripts/generate-pages.sh` вытягивает активные новости из Supabase.
  - Создаёт Markdown-посты в `website/content/posts`.
  - Hugo собирает сайт и отдаёт готовый HTML.
- **SEO** (`layouts/partials/seo.html`)
  - meta description/keywords.
  - Open Graph (og:title, og:image, og:description).
  - Twitter Card.
  - JSON-LD (NewsArticle) для Google.

## 8. CI/CD и автоматизация
- **GitHub Actions** (`.github/workflows/*.yml`)
  - `news.yml` — по расписанию:
    - Сборка Go-бота.
    - Тест подключения к PostgreSQL.
    - Запуск бота (5 раз в день).
    - Коммит новых постов сайта (если они создались).
  - `deploy-site.yml` — деплой сайта:
    - Установка Hugo.
    - Запуск `scripts/generate-pages.sh` (Supabase → Markdown).
    - `hugo --gc --minify`.
    - Деплой на GitHub Pages.
- **GitHub Pages**
  - Хостинг сайта: `https://deusflow.github.io/News/`.

## 9. Надёжность и защита от ошибок
- **Безопасная обрезка текста**
  - `trimToNearestSentenceOrWord` и `fallbackSummary` работают по рунам.
  - Нет `slice bounds out of range` даже с эмодзи и датскими буквами.
- **Защита от дублей**
  - В Telegram — через `sent_news` в PostgreSQL.
  - В Supabase — через `IsDuplicateNews` + таймаут 2 секунды.
- **Rate limiting**
  - Gemini — ограничение количества запросов (таймер в `gemini.Client`).
- **DLQ (dead letter queue)**
  - Неотправленные сообщения сохраняются в Postgres для разборов.
- **Retry Mechanism (Supabase)**
  - Автоматический повтор запросов при ошибках 500, 502, 503, 504, 429.
  - До 3 попыток с экспоненциальной задержкой (2s → 4s → 8s, макс 10s).
  - Логирование каждой попытки для отладки.
  - Защита от временной недоступности Supabase (Bad Gateway, Service Unavailable).

Кратко: Go-бот собирает и обрабатывает новости (RSS + AI), Telegram — основной канал доставки, Supabase — архив для сайта, Hugo + GitHub Pages — статический фронтенд с кастомным анимированным дизайном и погодным виджетом.
