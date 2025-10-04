# 🔐 Настройка GitHub Actions Secrets

## ✅ Что было обновлено в workflow

Файл `.github/workflows/news.yml` теперь полностью поддерживает PostgreSQL для предотвращения дубликатов.

### Основные изменения:

1. ✅ Добавлена переменная `DATABASE_URL` для PostgreSQL
2. ✅ Добавлена переменная `USE_POSTGRES=true`
3. ✅ Добавлен тест подключения к БД перед запуском
4. ✅ Файловый кеш оставлен как fallback
5. ✅ Улучшено логирование

## 🔑 КРИТИЧЕСКИ ВАЖНО: Добавьте секрет DATABASE_URL

### Шаг 1: Перейдите в настройки GitHub

1. Откройте ваш репозиторий на GitHub
2. Перейдите в **Settings** (Настройки)
3. В левом меню выберите **Secrets and variables** → **Actions**
4. Нажмите **New repository secret**

### Шаг 2: Добавьте DATABASE_URL

**Name:** `DATABASE_URL`

**Value:** 
```
postgresql://neondb_owner:npg_jCx5hGv1VHJX@ep-morning-recipe-advcp4vl-pooler.c-2.us-east-1.aws.neon.tech/neondb?sslmode=require
```

### Шаг 3: Проверьте остальные секреты

Убедитесь, что у вас есть все необходимые секреты:

- ✅ `TELEGRAM_TOKEN` - токен вашего Telegram бота
- ✅ `TELEGRAM_CHAT_ID` - ID канала (@news_about_dk)
- ✅ `GEMINI_API_KEY` - ключ Google Gemini API
- ✅ `DATABASE_URL` - **НОВЫЙ!** PostgreSQL connection string
- ⚠️ `GROQ_API_KEY` - опционально
- ⚠️ `COHERE_API_KEY` - опционально
- ⚠️ `MISTRALAI_API_KEY` - опционально

## 🎯 Как работает обновленный workflow

### 1. Подготовка
```yaml
- Checkout code
- Setup Go 1.23
- Download dependencies (включая github.com/lib/pq)
```

### 2. Кеширование (fallback)
```yaml
- Restore sent_news.json from cache (если PostgreSQL недоступна)
```

### 3. Сборка
```yaml
- Build bot with PostgreSQL support
```

### 4. Тест PostgreSQL
```yaml
- Test database connection
- Show connection status
```

### 5. Запуск бота
```yaml
- Run bot with PostgreSQL (primary)
- Fallback to file cache if DB fails
```

### 6. Логирование
```yaml
- Upload run logs as artifact
- Upload cache file (if used)
```

## 🔍 Проверка работы

### Посмотрите логи запуска

В GitHub Actions вы должны увидеть:

```
🔌 Testing PostgreSQL connection...
✅ Successfully connected to PostgreSQL!

📊 Database Statistics:
  Total items: X
  Active items: X

🚀 Starting Danish News Bot...
Database mode: PostgreSQL (primary) + File cache (fallback)
✅ PostgreSQL cache initialized successfully
```

### Если PostgreSQL недоступна

Workflow не упадет, а автоматически переключится на файловый кеш:

```
⚠️ PostgreSQL test failed, will use fallback cache
⚠️ Failed to connect to PostgreSQL, falling back to file cache
Using file-based cache
```

## 🚨 Что делать при проблемах

### Проблема: "DATABASE_URL not found"

**Решение:** Добавьте секрет `DATABASE_URL` в настройках репозитория

### Проблема: "Failed to connect to PostgreSQL"

**Возможные причины:**
1. Неверный connection string в секрете
2. База данных недоступна
3. Проблемы с сетью

**Решение:** 
- Проверьте правильность DATABASE_URL
- Workflow автоматически переключится на файловый кеш
- Дубликаты всё равно будут предотвращены (но менее надежно)

### Проблема: Workflow падает с ошибкой компиляции

**Решение:** 
- Убедитесь, что `go.mod` содержит `github.com/lib/pq v1.10.9`
- В workflow уже есть `go mod download` - должно работать автоматически

## 📊 Преимущества нового workflow

### До обновления:
- ❌ Только файловый кеш (не защищает от параллельных запусков)
- ❌ Нет защиты от race conditions
- ❌ Дубликаты при одновременных запусках

### После обновления:
- ✅ PostgreSQL как основное хранилище (защита от дубликатов)
- ✅ Атомарные операции в БД
- ✅ Можно запускать workflow параллельно без дубликатов
- ✅ Graceful fallback на файловый кеш
- ✅ Детальное логирование

## 🧪 Тестирование

### Ручной запуск workflow

1. Перейдите в **Actions** на GitHub
2. Выберите **Danish News Bot - Scheduled**
3. Нажмите **Run workflow**
4. Выберите ветку (обычно `main`)
5. Нажмите **Run workflow**

### Проверка логов

После запуска:
1. Откройте запущенный workflow
2. Разверните шаг **Test PostgreSQL connection**
3. Убедитесь, что видите `✅ Successfully connected to PostgreSQL!`
4. Разверните шаг **Run bot**
5. Проверьте, что нет сообщений о дубликатах

## 📅 Расписание запусков

Workflow запускается автоматически:
- 05:30 UTC (07:30 по датскому времени)
- 07:30 UTC (09:30 по датскому времени)
- 10:00 UTC (12:00 по датскому времени)
- 12:00 UTC (14:00 по датскому времени)
- 14:00 UTC (16:00 по датскому времени)
- 17:00 UTC (19:00 по датскому времени)
- 19:00 UTC (21:00 по датскому времени)

**Важно:** Теперь даже если несколько запусков произойдут одновременно, дубликатов не будет благодаря PostgreSQL!

## ✅ Чек-лист перед первым запуском

- [ ] Добавлен секрет `DATABASE_URL` в GitHub
- [ ] Проверены все остальные секреты (TELEGRAM_TOKEN, GEMINI_API_KEY)
- [ ] Закоммичены изменения в `news.yml`
- [ ] Запущен тестовый workflow вручную
- [ ] Проверены логи на наличие `✅ Successfully connected to PostgreSQL!`
- [ ] Проверено, что новости отправляются без дубликатов

---

**Готово! Теперь ваш GitHub Actions workflow защищен от дубликатов!** 🚀

