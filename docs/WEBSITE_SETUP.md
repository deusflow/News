# 🌐 Налаштування сайту для dknews (Hugo + GitHub Pages)

## 📋 План реалізації

### Огляд
Додаємо статичний сайт на Hugo, який автоматично публікує новини паралельно з Telegram ботом.

**Результат:** `https://your-username.github.io/dknews`

---

## 🛠 ЕТАП 1: Підготовка Hugo проекту

### Крок 1.1 — Встановлення Hugo (macOS)

```bash
brew install hugo
```

Перевірка версії:
```bash
hugo version
```

Очікуваний результат:
```
hugo v0.xxx.x+extended darwin/arm64 ...
```

> ⚠️ Переконайся, що версія `extended` — це важливо для SCSS підтримки.

---

### Крок 1.2 — Структура Hugo проекту

Буде створена папка `website/` з такою структурою:

```
website/
├── config.toml              # Головна конфігурація Hugo
├── content/
│   └── posts/               # Тут будуть .md файли новин
├── layouts/
│   ├── _default/
│   │   ├── baseof.html      # Базовий шаблон
│   │   ├── list.html        # Список новин
│   │   └── single.html      # Одна новина
│   ├── partials/
│   │   ├── header.html      # Шапка сайту
│   │   ├── footer.html      # Підвал сайту
│   │   └── news-card.html   # Картка новини
│   └── index.html           # Головна сторінка
├── static/
│   ├── css/
│   │   └── style.css        # Твої стилі
│   ├── js/
│   │   └── main.js          # JavaScript (опційно)
│   └── images/              # Статичні картинки
└── archetypes/
    └── default.md           # Шаблон для нових постів
```

---

### Крок 1.3 — Основні концепції Hugo

| Термін | Пояснення |
|--------|-----------|
| `content/` | Markdown файли з контентом (новини) |
| `layouts/` | HTML шаблони для рендерингу |
| `static/` | CSS, JS, картинки — копіюються без змін |
| `config.toml` | Налаштування сайту (назва, URL, мова) |
| `partials/` | Переиспользовуємі HTML компоненти |
| Front Matter | YAML метадані на початку .md файлу |

**Приклад Front Matter:**
```yaml
---
title: "Данія виділила 2 мільярди на оборону"
date: 2026-01-26T14:30:00+01:00
categories: ["Політика"]
tags: ["оборона", "бюджет"]
image: "https://example.com/image.jpg"
source_url: "https://dr.dk/news/..."
---
```

---

## 🎨 ЕТАП 2: Створення кастомної теми

### Крок 2.1 — Базові шаблони

**`layouts/_default/baseof.html`** — скелет всіх сторінок:
```html
<!DOCTYPE html>
<html lang="{{ .Site.LanguageCode }}">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{ .Title }} | {{ .Site.Title }}</title>
    <link rel="stylesheet" href="/css/style.css">
</head>
<body>
    {{ partial "header.html" . }}
    <main>
        {{ block "main" . }}{{ end }}
    </main>
    {{ partial "footer.html" . }}
</body>
</html>
```

**`layouts/_default/single.html`** — сторінка однієї новини:
```html
{{ define "main" }}
<article class="news-article">
    <h1>{{ .Title }}</h1>
    <time>{{ .Date.Format "02.01.2006" }}</time>
    
    {{ with .Params.image }}
    <img src="{{ . }}" alt="{{ $.Title }}">
    {{ end }}
    
    <div class="content">
        {{ .Content }}
    </div>
    
    {{ with .Params.source_url }}
    <a href="{{ . }}" target="_blank">🔗 Читати оригінал</a>
    {{ end }}
</article>
{{ end }}
```

---

### Крок 2.2 — Стилі (CSS)

Розташування: `website/static/css/style.css`

Ти можеш використовувати:
- Чистий CSS
- Tailwind CSS (через CDN)
- SCSS (Hugo extended підтримує)

---

## ⚙️ ЕТАП 3: Go генератор контенту

### Крок 3.1 — Новий пакет

Файл: `internal/website/generator.go`

Функції:
- `GenerateNewsPost(news News) error` — створює .md файл
- `generateSlug(title string) string` — URL-friendly назва
- `formatFrontMatter(news News) string` — YAML метадані

### Крок 3.2 — Формат вихідного файлу

```markdown
---
title: "Заголовок новини"
date: 2026-01-26T14:30:00+01:00
categories: ["Політика"]
tags: ["тег1", "тег2"]
mood: "neutral"
tldr: "🏛️ Коротко про новину"
image: "https://..."
source_url: "https://..."
source_name: "DR"
draft: false
---

## 🇺🇦 Українською

Текст новини українською мовою...

## 🇩🇰 På dansk

Tekst på dansk...

---

💡 **Цікавий факт:** {{ .FunFact }}
```

---

## 🔗 ЕТАП 4: Інтеграція з ботом

### Крок 4.1 — Зміни в `app.go`

Додаємо виклик генератора **після** успішної відправки в Telegram:

```go
// Після telegram.SendMessage()
if err == nil {
    if genErr := website.GenerateNewsPost(n); genErr != nil {
        logger.Warn("Failed to generate website post", "error", genErr)
    }
}
```

> ⚠️ Генерація сайту НЕ впливає на роботу бота — якщо генерація впаде, бот продовжить працювати.

---

## 🚀 ЕТАП 5: GitHub Actions деплой

### Крок 5.1 — Новий workflow

Файл: `.github/workflows/deploy-site.yml`

```yaml
name: Deploy Hugo Site

on:
  push:
    branches: [main]
    paths:
      - 'website/content/**'
      - 'website/layouts/**'
      - 'website/static/**'

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - name: Setup Hugo
        uses: peaceiris/actions-hugo@v3
        with:
          hugo-version: 'latest'
          extended: true
      
      - name: Build
        run: hugo --minify
        working-directory: website
      
      - name: Deploy to GitHub Pages
        uses: peaceiris/actions-gh-pages@v4
        with:
          github_token: ${{ secrets.GITHUB_TOKEN }}
          publish_dir: ./website/public
```

### Крок 5.2 — Налаштування GitHub Pages

1. Йди в **Settings** → **Pages**
2. Source: **Deploy from a branch**
3. Branch: **gh-pages** / **(root)**
4. Зберегти

---

## 🧪 ЕТАП 6: Локальне тестування

### Запуск Hugo dev-сервера

```bash
cd website
hugo server -D
```

Відкрий: `http://localhost:1313`

### Генерація статичного сайту

```bash
cd website
hugo --minify
```

Результат буде в папці `website/public/`

---

## 📁 Фінальна структура проекту

```
dknews/
├── cmd/dknews/main.go
├── internal/
│   ├── app/app.go              # + виклик website.GenerateNewsPost()
│   ├── website/                # 🆕 НОВИЙ пакет
│   │   └── generator.go
│   └── ... (інші пакети без змін)
├── website/                    # 🆕 Hugo проект
│   ├── config.toml
│   ├── content/posts/
│   ├── layouts/
│   └── static/
├── .github/workflows/
│   ├── news.yml               # Бот (без змін в логіці)
│   └── deploy-site.yml        # 🆕 Деплой сайту
└── docs/
    └── WEBSITE_SETUP.md       # Ця інструкція
```

---

## ✅ Чекліст

- [ ] Hugo встановлено (`hugo version`)
- [ ] Створено `website/` структуру
- [ ] Створено `internal/website/generator.go`
- [ ] Додано виклик генератора в `app.go`
- [ ] Створено `deploy-site.yml` workflow
- [ ] Налаштовано GitHub Pages
- [ ] Протестовано локально (`hugo server`)
- [ ] Перший деплой успішний

---

## 🔗 Корисні посилання

- [Hugo Documentation](https://gohugo.io/documentation/)
- [Hugo Templates](https://gohugo.io/templates/)
- [GitHub Pages](https://pages.github.com/)
- [peaceiris/actions-hugo](https://github.com/peaceiris/actions-hugo)

---

## ❓ FAQ

**Q: Чи зламається бот якщо Hugo впаде?**
A: Ні. Генерація сайту — окрема операція з обробкою помилок.

**Q: Скільки це коштує?**
A: $0. GitHub Pages безкоштовний для публічних репозиторіїв.

**Q: Чи можна змінити дизайн пізніше?**
A: Так. Просто редагуй файли в `website/layouts/` та `website/static/css/`.
