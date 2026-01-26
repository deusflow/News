# 🛠 Як працює DK News: Технічний огляд

## 📋 Зміст
1. [Загальна архітектура](#загальна-архітектура)
2. [Технології](#технології)
3. [Як працює бот](#як-працює-бот)
4. [Як працює сайт](#як-працює-сайт)
5. [GitHub Actions (автоматизація)](#github-actions)
6. [Потік даних](#потік-даних)

---

## 🏗 Загальна архітектура

```
┌─────────────────────────────────────────────────────────────────────┐
│                         GITHUB REPOSITORY                           │
│                                                                     │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────────────────┐ │
│  │   Go код    │    │   Hugo      │    │   GitHub Actions        │ │
│  │   (бот)     │    │   (сайт)    │    │   (автоматизація)       │ │
│  └─────────────┘    └─────────────┘    └─────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────┘
         │                    │                      │
         ▼                    ▼                      ▼
    ┌─────────┐         ┌─────────┐          ┌─────────────┐
    │Telegram │         │ GitHub  │          │   CRON      │
    │ Channel │         │  Pages  │          │ (розклад)   │
    └─────────┘         └─────────┘          └─────────────┘
```

---

## 🔧 Технології

### Backend (Бот)

| Технологія | Для чого |
|------------|----------|
| **Go (Golang)** | Мова програмування бота |
| **Gemini AI** | Переклад та обробка новин |
| **PostgreSQL** | База даних для кешу (щоб не дублювати новини) |
| **RSS** | Отримання новин з данських сайтів |
| **Telegram Bot API** | Відправка повідомлень в канал |

### Frontend (Сайт)

| Технологія | Для чого |
|------------|----------|
| **Hugo** | Генератор статичних сайтів (Go-based) |
| **HTML/CSS** | Шаблони та стилі |
| **GitHub Pages** | Безкоштовний хостинг |
| **Markdown** | Формат контенту (новин) |

### CI/CD (Автоматизація)

| Технологія | Для чого |
|------------|----------|
| **GitHub Actions** | Автоматичний запуск задач |
| **CRON** | Розклад запуску (5 раз на день) |
| **Git** | Контроль версій та тригер деплою |

---

## 🤖 Як працює бот

### Структура коду

```
internal/
├── app/app.go          # Головна логіка бота
├── config/config.go    # Конфігурація (env змінні)
├── gemini/gemini.go    # Інтеграція з AI
├── news/news.go        # Обробка новин
├── rss/rss.go          # Парсинг RSS стрічок
├── storage/postgres.go # База даних
├── telegram/telegram.go# Відправка в Telegram
└── website/generator.go# Генерація постів для сайту (НОВЕ)
```

### Послідовність виконання

```go
// cmd/dknews/main.go

func main() {
    // 1. Завантаження конфігурації з ENV
    cfg, err := config.Load()
    
    // 2. Ініціалізація додатку
    application, err := app.New(cfg, m)
    
    // 3. Запуск бота
    application.Run(ctx)
}
```

```go
// internal/app/app.go - Run()

func (a *App) Run(ctx context.Context) {
    // 1. Завантажити RSS фіди
    items, err := rss.FetchAllFeeds(a.feeds)
    
    // 2. Фільтрація + переклад через Gemini AI
    filtered, err := news.FilterAndTranslateWithOptions(ctx, items, ...)
    
    // 3. Відправка в Telegram
    sendSingleNews(filtered, a.cfg, a.cacheAdapter, a.metrics, a.websiteGenerator)
    
    // 4. Генерація поста для сайту (якщо ENABLE_WEBSITE=true)
    // Відбувається всередині sendSingleNews()
}
```

---

## 🌐 Як працює сайт

### Що таке Hugo?

**Hugo** — це генератор статичних сайтів. Він бере:
- Markdown файли (контент)
- HTML шаблони (layouts)
- CSS стилі (static)

І генерує готовий HTML сайт (папка `public/`).

### Структура Hugo проекту

```
website/
├── config.toml              # Налаштування сайту
├── content/
│   └── posts/               # Markdown файли новин
│       ├── 2026-01-26-news.md
│       └── ...
├── layouts/
│   ├── _default/
│   │   ├── baseof.html      # Базовий шаблон
│   │   ├── list.html        # Список новин
│   │   └── single.html      # Одна новина
│   ├── partials/
│   │   ├── header.html      # Шапка
│   │   └── footer.html      # Підвал
│   └── index.html           # Головна сторінка
└── static/
    └── css/style.css        # Стилі
```

### Як генерується пост

Коли бот відправляє новину, він також викликає `website.Generator`:

```go
// internal/website/generator.go

func (g *Generator) GeneratePost(post NewsPost) error {
    // 1. Створити slug з заголовка
    slug := generateSlug(post.Title)  // "Данія оголосила..." → "daniya-oholosyla"
    
    // 2. Сформувати ім'я файлу
    filename := "2026-01-26-daniya-oholosyla.md"
    
    // 3. Згенерувати Markdown з front matter
    content := g.generateMarkdown(post)
    
    // 4. Записати файл
    os.WriteFile(filepath, content, 0644)
}
```

Формат згенерованого файлу:

```markdown
---
title: "Данія оголосила про новий план"
date: 2026-01-26T14:30:00+01:00
categories: ["Politics"]
image: "https://..."
source_url: "https://dr.dk/..."
source_name: "DR"
tldr: "🌱 Коротко про новину"
---

## 🇺🇦 Українською

Текст новини...

## 🇩🇰 På dansk

Tekst på dansk...
```

---

## ⚡ GitHub Actions

### Що це?

**GitHub Actions** — це CI/CD платформа GitHub. Вона дозволяє автоматично виконувати задачі при певних подіях (push, cron, тощо).

### Файли workflows

```
.github/workflows/
├── news.yml           # Запуск бота
└── deploy-site.yml    # Деплой сайту
```

### news.yml — Бот

```yaml
name: Danish News Bot - Scheduled

# КОЛИ запускати:
on:
  schedule:
    - cron: '0 6 * * *'    # 06:00 UTC
    - cron: '0 9 * * *'    # 09:00 UTC
    - cron: '0 12 * * *'   # 12:00 UTC
    - cron: '0 15 * * *'   # 15:00 UTC
    - cron: '0 18 * * *'   # 18:00 UTC
  workflow_dispatch:        # Ручний запуск

# ЩО робити:
jobs:
  run-bot:
    runs-on: ubuntu-latest  # Віртуальна машина Linux
    
    env:
      TELEGRAM_TOKEN: ${{ secrets.TELEGRAM_TOKEN }}
      GEMINI_API_KEY: ${{ secrets.GEMINI_API_KEY }}
      ENABLE_WEBSITE: 'true'  # Генерувати пости для сайту
    
    steps:
      # 1. Завантажити код
      - uses: actions/checkout@v4
      
      # 2. Встановити Go
      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      
      # 3. Зібрати бота
      - run: go build -o bin/dknews ./cmd/dknews
      
      # 4. Запустити бота
      - run: ./bin/dknews
      
      # 5. Закомітити нові пости (якщо є)
      - name: Commit website posts
        run: |
          git add website/content/posts/
          git commit -m "🌐 Auto-generate website posts"
          git push
```

### deploy-site.yml — Сайт

```yaml
name: Deploy Website to GitHub Pages

# КОЛИ запускати:
on:
  push:
    branches: [main]
    paths: ['website/**']   # Тільки при змінах в website/
  workflow_dispatch:        # Ручний запуск

# ЩО робити:
jobs:
  build:
    runs-on: ubuntu-latest
    
    steps:
      # 1. Завантажити код
      - uses: actions/checkout@v4
      
      # 2. Встановити Hugo
      - uses: peaceiris/actions-hugo@v3
        with:
          hugo-version: 'latest'
      
      # 3. Зібрати сайт
      - run: hugo --gc --minify
        working-directory: website
      
      # 4. Завантажити результат
      - uses: actions/upload-pages-artifact@v3
        with:
          path: ./website/public

  deploy:
    needs: build
    
    steps:
      # Деплой на GitHub Pages
      - uses: actions/deploy-pages@v4
```

---

## 🔄 Потік даних (повний цикл)

```
┌─────────────────────────────────────────────────────────────────────┐
│                         CRON: 5 раз на день                         │
│                               │                                     │
│                               ▼                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │                    news.yml запускається                     │   │
│  │                                                              │   │
│  │  1. Checkout repo                                            │   │
│  │  2. Setup Go                                                 │   │
│  │  3. Build ./bin/dknews                                       │   │
│  │  4. Run ./bin/dknews                                         │   │
│  │       │                                                      │   │
│  │       ├──► RSS: Завантаження новин з DR, TV2, тощо          │   │
│  │       │                                                      │   │
│  │       ├──► Gemini AI: Переклад на українську та данську     │   │
│  │       │                                                      │   │
│  │       ├──► PostgreSQL: Перевірка чи не дублікат             │   │
│  │       │                                                      │   │
│  │       ├──► Telegram: Відправка в канал ✅                   │   │
│  │       │                                                      │   │
│  │       └──► Website: Генерація .md файлу                     │   │
│  │                                                              │   │
│  │  5. Git commit + push (новий .md файл)                       │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                               │                                     │
│                               ▼                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │              deploy-site.yml запускається                    │   │
│  │              (тригер: push в website/)                       │   │
│  │                                                              │   │
│  │  1. Checkout repo                                            │   │
│  │  2. Setup Hugo                                               │   │
│  │  3. hugo --gc --minify                                       │   │
│  │  4. Upload artifact (public/)                                │   │
│  │  5. Deploy to GitHub Pages ✅                                │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                               │                                     │
│                               ▼                                     │
│                    🌐 https://deusflow.github.io/News/             │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 🔑 Ключові концепції

### 1. Статичний сайт vs Динамічний

| Статичний (Hugo) | Динамічний (WordPress) |
|------------------|------------------------|
| HTML генерується заздалегідь | HTML генерується при кожному запиті |
| Швидкий (просто файли) | Повільніший (PHP + MySQL) |
| Безкоштовний хостинг | Потрібен сервер |
| Безпечний | Вразливий до атак |

### 2. CI/CD

**CI** (Continuous Integration) — автоматичне тестування/збірка при кожному коміті.

**CD** (Continuous Deployment) — автоматичний деплой після успішного CI.

### 3. GitHub Pages

Безкоштовний хостинг для статичних сайтів. Обмеження:
- Тільки статичний контент (HTML, CSS, JS)
- 1 GB розмір репозиторію
- 100 GB трафіку на місяць

### 4. Secrets

Чутливі дані (API ключі, токени) зберігаються в GitHub Secrets і передаються в workflow через `${{ secrets.NAME }}`.

---

## 📁 Файли проекту

```
dknews/
├── cmd/dknews/main.go           # Точка входу бота
├── internal/
│   ├── app/app.go               # Головна логіка
│   ├── config/config.go         # ENV конфігурація
│   ├── gemini/gemini.go         # Gemini AI клієнт
│   ├── news/news.go             # Обробка новин
│   ├── rss/rss.go               # RSS парсер
│   ├── storage/postgres.go      # PostgreSQL
│   ├── telegram/telegram.go     # Telegram API
│   └── website/generator.go     # Hugo генератор (НОВЕ)
├── website/                     # Hugo проект
│   ├── config.toml
│   ├── content/posts/
│   ├── layouts/
│   └── static/css/
├── .github/workflows/
│   ├── news.yml                 # Workflow бота
│   └── deploy-site.yml          # Workflow сайту
└── configs/
    ├── feeds.yaml               # RSS джерела
    └── keywords.yaml            # Ключові слова
```

---

## ❓ FAQ

**Q: Чому Hugo, а не React/Next.js?**
A: Hugo — простіший, швидший, не потребує Node.js. Ідеальний для контентних сайтів.

**Q: Скільки коштує?**
A: $0. GitHub Actions (2000 хв/міс безкоштовно) + GitHub Pages (безкоштовно).

**Q: Що якщо бот впаде?**
A: Сайт продовжить працювати — це просто статичні файли.

**Q: Що якщо деплой впаде?**
A: Бот продовжить працювати — вони незалежні.
